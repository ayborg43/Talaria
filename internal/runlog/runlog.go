// Package runlog keeps a short record of what each background run did: the tools it used, the files it made, what it
// reported, and what the user told it along the way. The Activity view shows it, so a task is something you can open
// and read instead of a line that says "finished".
//
// It is deliberately small. A run keeps at most a few dozen one-line steps and a few thousand characters of result;
// only the last thirty runs are kept; a run in progress lives in memory and is written down when it ends. A step holds
// the tool's name and what it was asked, after secrets are scrubbed, and never the page or file it read.
package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Statuses.
const (
	Running   = "running"
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Limits keep one run, and the whole log, a few kilobytes.
const (
	MaxRuns       = 30
	MaxSteps      = 40
	keepFirst     = 5 // when a run has more steps than MaxSteps, its first few and its latest are kept
	MaxDetail     = 140
	MaxResult     = 4000
	MaxPrompt     = 600
	MaxSteerText  = 1000
	MaxSteersLive = 5 // unapplied notes a running task may be holding
)

// Step is one thing a run did.
type Step struct {
	At     time.Time `json:"at"`
	Tool   string    `json:"tool"`
	Detail string    `json:"detail,omitempty"`
}

// File is something a run made, for the app to offer to open.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime,omitempty"`
}

// Steer is something the user told a run.
type Steer struct {
	At      time.Time `json:"at"`
	Text    string    `json:"text"`
	Applied bool      `json:"applied,omitempty"` // the run has read it
}

// Run is one run of a job.
type Run struct {
	ID      string    `json:"id"`
	JobID   string    `json:"job_id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // task, goal or "" for a scheduled prompt
	Gateway string    `json:"gateway"`
	ChatID  int64     `json:"chat_id"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	Status  string    `json:"status"`
	Model   string    `json:"model,omitempty"`
	Prompt  string    `json:"prompt,omitempty"`
	Steps   []Step    `json:"steps,omitempty"`
	Skipped int       `json:"skipped,omitempty"` // steps dropped from the middle of a long run
	Files   []File    `json:"files,omitempty"`
	Result  string    `json:"result,omitempty"`
	Steers  []Steer   `json:"steers,omitempty"`
	// Parent is the run this one follows up.
	Parent string `json:"parent,omitempty"`
}

// Summary is a run as the list shows it.
type Summary struct {
	ID      string    `json:"id"`
	JobID   string    `json:"job_id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	Steps   int       `json:"steps"`
	Files   int       `json:"files"`
	Doing   string    `json:"doing,omitempty"` // the latest step, for a run in progress
}

// Log is the record of runs. The zero value keeps them in memory only; set Path to keep the finished ones.
type Log struct {
	Path string

	mu     sync.Mutex
	loaded bool
	done   []Run // oldest first
	live   map[string]*Run
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func oneLine(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }

func (l *Log) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	if l.live == nil {
		l.live = map[string]*Run{}
	}
	if l.Path == "" {
		return
	}
	if raw, err := os.ReadFile(l.Path); err == nil {
		_ = json.Unmarshal(raw, &l.done) // a damaged record is not worth failing a run over
	}
}

func (l *Log) save() {
	if l.Path == "" {
		return
	}
	if len(l.done) > MaxRuns {
		l.done = l.done[len(l.done)-MaxRuns:]
	}
	raw, err := json.Marshal(l.done)
	if err != nil {
		return
	}
	tmp := l.Path + ".tmp"
	if os.MkdirAll(filepath.Dir(l.Path), 0o700) != nil || os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, l.Path)
}

// Begin records that a job has started to run, and returns the run's id.
func (l *Log) Begin(jobID, name, kind, gateway string, chatID int64, prompt, model, parent string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	now := time.Now()
	id := fmt.Sprintf("%s-%d", jobID, now.UnixMilli())
	l.live[id] = &Run{ID: id, JobID: jobID, Name: oneLine(name, 80), Kind: kind, Gateway: gateway, ChatID: chatID, Started: now, Status: Running,
		Model: model, Prompt: clip(prompt, MaxPrompt), Parent: parent}
	return id
}

// Step adds one thing a live run did. A long run keeps its first steps and its latest, and counts the ones between.
func (l *Log) Step(id, tool, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.live[id]
	if r == nil {
		return
	}
	r.Steps = append(r.Steps, Step{At: time.Now(), Tool: tool, Detail: oneLine(detail, MaxDetail)})
	if len(r.Steps) > MaxSteps {
		r.Steps = slices.Delete(r.Steps, keepFirst, keepFirst+1)
		r.Skipped++
	}
}

// AddFile notes a file a live run made.
func (l *Log) AddFile(id string, f File) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.live[id]; r != nil && f.ID != "" && len(r.Files) < 10 {
		r.Files = append(r.Files, File{ID: f.ID, Name: oneLine(f.Name, 80), MIME: f.MIME})
	}
}

// End closes a run with how it went and what it reported, and writes it down.
func (l *Log) End(id, status, result string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.live[id]
	if r == nil {
		return
	}
	delete(l.live, id)
	r.Ended, r.Status, r.Result = time.Now(), status, clip(result, MaxResult)
	l.done = append(l.done, *r)
	l.save()
}

// Steer queues something the user told a running job, for it to read before its next step. It says whether a run
// of that job is in progress to take it.
func (l *Log) Steer(jobOrRunID, text string) bool {
	text = oneLine(text, MaxSteerText)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	for _, r := range l.live {
		if r.ID != jobOrRunID && r.JobID != jobOrRunID {
			continue
		}
		pending := 0
		for _, s := range r.Steers {
			if !s.Applied {
				pending++
			}
		}
		if text == "" || pending >= MaxSteersLive {
			return false
		}
		r.Steers = append(r.Steers, Steer{At: time.Now(), Text: text})
		return true
	}
	return false
}

// Take hands a live run the notes it has not read yet, and marks them read.
func (l *Log) Take(id string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.live[id]
	if r == nil {
		return nil
	}
	var out []string
	for i := range r.Steers {
		if !r.Steers[i].Applied {
			r.Steers[i].Applied = true
			out = append(out, r.Steers[i].Text)
		}
	}
	return out
}

func summarise(r *Run) Summary {
	s := Summary{ID: r.ID, JobID: r.JobID, Name: r.Name, Kind: r.Kind, Status: r.Status, Started: r.Started, Ended: r.Ended, Steps: len(r.Steps) + r.Skipped, Files: len(r.Files)}
	if r.Status == Running && len(r.Steps) > 0 {
		last := r.Steps[len(r.Steps)-1]
		s.Doing = strings.TrimSpace(last.Tool + " " + last.Detail)
	}
	return s
}

// List is a chat's runs, those in progress first and then the finished ones, newest first.
func (l *Log) List(gateway string, chatID int64, limit int) []Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	var running, ended []Summary
	for _, r := range l.live {
		if r.Gateway == gateway && r.ChatID == chatID {
			running = append(running, summarise(r))
		}
	}
	slices.SortFunc(running, func(a, b Summary) int { return b.Started.Compare(a.Started) })
	for i := len(l.done) - 1; i >= 0; i-- {
		if r := &l.done[i]; r.Gateway == gateway && r.ChatID == chatID {
			ended = append(ended, summarise(r))
		}
	}
	out := append(running, ended...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Get finds one run, by its id or by the id of its job (the latest run of that job). It is a copy.
func (l *Log) Get(gateway string, chatID int64, id string) (Run, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	copyOf := func(r *Run) Run {
		c := *r
		c.Steps, c.Files, c.Steers = slices.Clone(r.Steps), slices.Clone(r.Files), slices.Clone(r.Steers)
		return c
	}
	var best *Run
	consider := func(r *Run) {
		if r.Gateway != gateway || r.ChatID != chatID || (r.ID != id && r.JobID != id) {
			return
		}
		if best == nil || r.Started.After(best.Started) {
			best = r
		}
	}
	for _, r := range l.live {
		consider(r)
	}
	for i := range l.done {
		consider(&l.done[i])
	}
	if best == nil {
		return Run{}, false
	}
	return copyOf(best), true
}
