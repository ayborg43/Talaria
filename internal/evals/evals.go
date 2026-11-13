// Package evals is a small set of the person's own questions, run against a model to see how it does before it is
// trusted with real work. Each question says what a good answer contains, so a run is a number (8 of 10) and a list of
// what was missed, not a guess. It costs a few small calls only when a run is asked for.
package evals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"talaria/internal/provider"
)

// Limits: a test set is a handful of questions, not a benchmark.
const (
	MaxCases     = 40
	MaxPrompt    = 2000
	MaxCheck     = 200
	keepRuns     = 6 // runs kept per profile
	replyKept    = 400
	perCaseLimit = 90 * time.Second
)

// Case is one question and what a good answer looks like. At least one of Expect, Any, Avoid or Rubric makes it checkable; a
// case with none is run and shown, but never counted as passed or failed.
type Case struct {
	ID     string   `json:"id"`
	Prompt string   `json:"prompt"`
	Expect []string `json:"expect,omitempty"` // every one of these must appear (any case)
	Any    []string `json:"any,omitempty"`    // at least one of these must appear
	Avoid  []string `json:"avoid,omitempty"`  // none of these may appear
	// Rubric is a sentence a second model judges the answer against ("gives a number of days and says it assumed a non-leap year").
	Rubric string `json:"rubric,omitempty"`
}

// Checkable says whether the case has anything to check an answer against.
func (c Case) Checkable() bool { return len(c.Expect)+len(c.Any)+len(c.Avoid) > 0 || c.Rubric != "" }

// Result is how one case went.
type Result struct {
	CaseID    string   `json:"caseId"`
	Prompt    string   `json:"prompt"`
	Reply     string   `json:"reply,omitempty"`
	Pass      bool     `json:"pass"`
	Unchecked bool     `json:"unchecked,omitempty"` // nothing to check the answer against
	Missed    []string `json:"missed,omitempty"`
	Ms        int64    `json:"ms"`
	Error     string   `json:"error,omitempty"`
}

// Run is one pass over the cases with one model.
type Run struct {
	ID      string    `json:"id"`
	Profile string    `json:"profile"`
	Model   string    `json:"model"`
	At      time.Time `json:"at"`
	Passed  int       `json:"passed"`
	Counted int       `json:"counted"` // the cases that could be checked
	Total   int       `json:"total"`
	AvgMs   int64     `json:"avgMs"`
	Judge   string    `json:"judge,omitempty"`
	Results []Result  `json:"results"`
}

// Line is the run in a few words: "8 of 10 on your questions".
func (r Run) Line() string {
	if r.Counted == 0 {
		return fmt.Sprintf("ran %d of your questions, none of them checkable", r.Total)
	}
	return fmt.Sprintf("%d of %d on your test questions (%.1fs on average)", r.Passed, r.Counted, float64(r.AvgMs)/1000)
}

// Store keeps the cases and the recent runs in one small file.
type Store struct {
	Path string
	mu   sync.Mutex
	data struct {
		Cases []Case `json:"cases"`
		Runs  []Run  `json:"runs"`
	}
	loaded bool
}

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.Path == "" {
		return
	}
	if raw, err := os.ReadFile(s.Path); err == nil {
		json.Unmarshal(raw, &s.data)
	}
}

func (s *Store) save() {
	if s.Path == "" {
		return
	}
	raw, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return
	}
	tmp := s.Path + ".tmp"
	if os.MkdirAll(filepath.Dir(s.Path), 0o700) == nil && os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, s.Path)
	}
}

func tidy(list []string) []string {
	var out []string
	for _, v := range list {
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > MaxCheck {
			v = string(r[:MaxCheck])
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Add keeps a new case. It is refused when there is no question, too many cases, or the question is too long.
func (s *Store) Add(c Case) (Case, error) {
	c.Prompt = strings.TrimSpace(c.Prompt)
	if c.Prompt == "" {
		return Case{}, errors.New("write the question")
	}
	if utf8.RuneCountInString(c.Prompt) > MaxPrompt {
		return Case{}, fmt.Errorf("a question can be up to %d characters", MaxPrompt)
	}
	c.Expect, c.Any, c.Avoid = tidy(c.Expect), tidy(c.Any), tidy(c.Avoid)
	if r := []rune(strings.TrimSpace(c.Rubric)); len(r) > 300 {
		c.Rubric = string(r[:300])
	} else {
		c.Rubric = strings.TrimSpace(c.Rubric)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if len(s.data.Cases) >= MaxCases {
		return Case{}, fmt.Errorf("a test set holds up to %d questions", MaxCases)
	}
	b := make([]byte, 4)
	rand.Read(b)
	c.ID = hex.EncodeToString(b)
	s.data.Cases = append(s.data.Cases, c)
	s.save()
	return c, nil
}

// Remove drops a case. Runs already made keep their results.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	n := len(s.data.Cases)
	s.data.Cases = slices.DeleteFunc(s.data.Cases, func(c Case) bool { return c.ID == id })
	if len(s.data.Cases) == n {
		return false
	}
	s.save()
	return true
}

// Cases lists the questions.
func (s *Store) Cases() []Case {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return slices.Clone(s.data.Cases)
}

// Runs lists the recent runs, newest first.
func (s *Store) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	out := slices.Clone(s.data.Runs)
	slices.Reverse(out)
	return out
}

// Latest is the newest run for a profile, if there is one.
func (s *Store) Latest(profile string) (Run, bool) {
	if s == nil {
		return Run{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	for i := len(s.data.Runs) - 1; i >= 0; i-- {
		if s.data.Runs[i].Profile == profile {
			return s.data.Runs[i], true
		}
	}
	return Run{}, false
}

// Keep stores a finished run, forgetting the oldest beyond what is kept per profile.
func (s *Store) Keep(r Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.data.Runs = append(s.data.Runs, r)
	n := 0
	for i := len(s.data.Runs) - 1; i >= 0; i-- {
		if s.data.Runs[i].Profile == r.Profile {
			if n++; n > keepRuns {
				s.data.Runs = slices.Delete(s.data.Runs, i, i+1)
			}
		}
	}
	s.save()
}

// Check says whether a reply meets a case's plain checks, and what it missed. The rubric is not looked at here.
func Check(c Case, reply string) (pass bool, missed []string) {
	low := strings.ToLower(reply)
	for _, e := range c.Expect {
		if !strings.Contains(low, strings.ToLower(e)) {
			missed = append(missed, "is missing \""+e+"\"")
		}
	}
	if len(c.Any) > 0 && !slices.ContainsFunc(c.Any, func(a string) bool { return strings.Contains(low, strings.ToLower(a)) }) {
		missed = append(missed, "has none of: "+strings.Join(c.Any, ", "))
	}
	for _, a := range c.Avoid {
		if strings.Contains(low, strings.ToLower(a)) {
			missed = append(missed, "says \""+a+"\"")
		}
	}
	return len(missed) == 0, missed
}

const answerPrompt = "Answer the question directly and briefly. You have no tools in this test."

const judgePrompt = "You judge an answer against a requirement. Reply with YES if the answer meets the requirement, otherwise NO, then a short reason."

// Execute puts every case to a model, one after another (a run costs a few small calls and is never run in parallel). judge, if
// set, is a second model that checks the cases that have a rubric. It stops early if ctx ends, returning what was done.
func Execute(ctx context.Context, cases []Case, p provider.Provider, model string, judge provider.Provider, judgeModel string) Run2 {
	var out Run2
	for _, c := range cases {
		if ctx.Err() != nil {
			break
		}
		res := Result{CaseID: c.ID, Prompt: c.Prompt}
		cctx, cancel := context.WithTimeout(ctx, perCaseLimit)
		began := time.Now()
		msg, err := p.Chat(cctx, provider.Request{Model: model, Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: answerPrompt}, {Role: provider.RoleUser, Content: c.Prompt},
		}})
		cancel()
		res.Ms = time.Since(began).Milliseconds()
		if err != nil {
			res.Error = provider.Short(err)
			res.Missed = []string{"no answer: " + res.Error}
		} else {
			res.Reply = msg.Content
			res.Pass, res.Missed = Check(c, msg.Content)
			if c.Rubric != "" {
				switch {
				case judge == nil:
					if len(c.Expect)+len(c.Any)+len(c.Avoid) == 0 {
						res.Unchecked = true
					}
				default:
					jctx, jc := context.WithTimeout(ctx, 40*time.Second)
					j, jerr := judge.Chat(jctx, provider.Request{Model: judgeModel, Messages: []provider.Message{
						{Role: provider.RoleSystem, Content: judgePrompt},
						{Role: provider.RoleUser, Content: "Requirement: " + c.Rubric + "\n\nQuestion: " + c.Prompt + "\n\nAnswer: " + msg.Content},
					}})
					jc()
					if jerr != nil {
						res.Missed = append(res.Missed, "the judge did not answer")
						res.Pass = false
					} else if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(j.Content)), "YES") {
						res.Missed = append(res.Missed, "judged not to meet: "+c.Rubric)
						res.Pass = false
					}
				}
			}
			if !c.Checkable() {
				res.Unchecked, res.Pass = true, false
			}
		}
		if r := []rune(res.Reply); len(r) > replyKept {
			res.Reply = string(r[:replyKept]) + "…"
		}
		out = append(out, res)
	}
	return out
}

// Run2 is a list of results; its Summary turns them into a Run.
type Run2 []Result

// Summary totals the results into a Run for the profile.
func (rs Run2) Summary(profile, model, judge string) Run {
	r := Run{Profile: profile, Model: model, At: time.Now(), Judge: judge, Total: len(rs), Results: rs}
	var ms int64
	for _, x := range rs {
		ms += x.Ms
		if x.Unchecked {
			continue
		}
		r.Counted++
		if x.Pass {
			r.Passed++
		}
	}
	if len(rs) > 0 {
		r.AvgMs = ms / int64(len(rs))
	}
	b := make([]byte, 4)
	rand.Read(b)
	r.ID = hex.EncodeToString(b)
	return r
}
