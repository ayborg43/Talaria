package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxJobs   = 50
	maxPrompt = 4000
)

// Job kinds.
const (
	KindTask  = "task"
	KindGoal  = "goal"
	KindWatch = "watch"
)

// DefaultGateway is the gateway a job belongs to when none is named, which is
// every job written before there was more than one.
const DefaultGateway = "telegram"

// Job is one scheduled agent task.
type Job struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	// Gateway names the chat service the result is delivered through, and ChatID
	// the chat on it. A chat is only ever one service's, so a Telegram user ID
	// and a Discord one can never be mistaken for each other.
	Gateway    string `json:"gateway,omitempty"`
	ChatID     int64  `json:"chat_id"`
	AllowRisky bool   `json:"allow_risky"` // may run tools that normally need approval
	// Kind says what the job is for: "" is a scheduled prompt, KindTask a
	// background task the user started from a chat, KindGoal a check-in on a goal.
	Kind string `json:"kind,omitempty"`
	// Goal is the goal a KindGoal job checks in on; its prompt is written fresh
	// from the goal's state at each run.
	Goal string `json:"goal,omitempty"`
	// Watch is the watch a KindWatch job polls.
	Watch string `json:"watch,omitempty"`
	// TimeoutSec overrides how long one run may take; 0 means the scheduler's own limit.
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// Profile and Environment say which model profile the job runs on and where
	// its shell and file tools run. "" means the gateway's default at the time of
	// each run, which is how a job created on the default behaves.
	Profile     string    `json:"profile,omitempty"`
	Environment string    `json:"environment,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	NextRun     time.Time `json:"next_run"`
	LastRun     time.Time `json:"last_run,omitzero"`
	LastStatus  string    `json:"last_status,omitempty"`
}

// Store keeps jobs in one JSON file. It rereads the file on every call, so
// edits made by another process are picked up on the next scheduler tick.
type Store struct {
	Path string
	Now  func() time.Time // defaults to time.Now; tests override

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) load() ([]Job, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.Path, err)
	}
	for i := range f.Jobs {
		if f.Jobs[i].Gateway == "" {
			f.Jobs[i].Gateway = DefaultGateway
		}
	}
	return f.Jobs, nil
}

// save writes via a temp file and rename so a crash never leaves half a file.
func (s *Store) save(jobs []Job) error {
	raw, err := json.MarshalIndent(struct {
		Jobs []Job `json:"jobs"`
	}{jobs}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "jobs-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil { // Close before Chmod/Rename for portability
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // prompts may be private
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

func (s *Store) List() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	return jobs, err
}

// Add validates and stores a job, assigning its ID and first run time.
func (s *Store) Add(j Job) (Job, error) {
	j.Schedule = strings.Join(strings.Fields(j.Schedule), " ")
	j.Prompt = strings.TrimSpace(j.Prompt)
	if j.Gateway == "" {
		j.Gateway = DefaultGateway
	}
	switch {
	case j.Prompt == "":
		return Job{}, errors.New("prompt is empty")
	case len(j.Prompt) > maxPrompt:
		return Job{}, fmt.Errorf("prompt too long (%d bytes, max %d)", len(j.Prompt), maxPrompt)
	case j.ChatID == 0:
		return Job{}, errors.New("job has no delivery chat")
	case !plainName(j.Profile) || !plainName(j.Environment) || !plainName(j.Gateway) || !plainName(j.Goal):
		return Job{}, errors.New("gateway, profile, environment and goal must be short plain names")
	case j.Kind != "" && j.Kind != KindTask && j.Kind != KindGoal && j.Kind != KindWatch:
		return Job{}, fmt.Errorf("unknown job kind %q", j.Kind)
	case j.Kind == KindGoal && j.Goal == "":
		return Job{}, errors.New("a goal job needs a goal")
	case j.Kind == KindWatch && j.Watch == "":
		return Job{}, errors.New("a watch job needs a watch")
	case j.TimeoutSec < 0:
		return Job{}, errors.New("timeout must not be negative")
	}
	sch, err := ParseSchedule(j.Schedule)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	next, ok := sch.Next(now)
	if !ok {
		return Job{}, fmt.Errorf("schedule %q never fires (a time in the past, or a date that does not exist)", j.Schedule)
	}
	if j.Name = strings.Join(strings.Fields(j.Name), " "); j.Name == "" {
		j.Name = snippet(j.Prompt, 40)
	}
	j.CreatedAt, j.NextRun = now, next

	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return Job{}, err
	}
	if len(jobs) >= maxJobs {
		return Job{}, fmt.Errorf("too many jobs (max %d): remove one first", maxJobs)
	}
	j.ID = newID(jobs)
	return j, s.save(append(jobs, j))
}

// AddNow stores a job to run as soon as the scheduler next looks, once. It is
// how a background task starts: the scheduler claims it, runs it and delivers
// its result like any other job.
func (s *Store) AddNow(j Job) (Job, error) {
	now := s.now()
	j.Schedule = "@at " + now.UTC().Format(time.RFC3339)
	j.Prompt = strings.TrimSpace(j.Prompt)
	switch {
	case j.Prompt == "":
		return Job{}, errors.New("prompt is empty")
	case len(j.Prompt) > maxPrompt:
		return Job{}, fmt.Errorf("prompt too long (%d bytes, max %d)", len(j.Prompt), maxPrompt)
	case j.ChatID == 0:
		return Job{}, errors.New("job has no delivery chat")
	case !plainName(j.Profile) || !plainName(j.Environment) || !plainName(j.Gateway):
		return Job{}, errors.New("gateway, profile and environment must be short plain names")
	case j.TimeoutSec < 0:
		return Job{}, errors.New("timeout must not be negative")
	}
	if j.Gateway == "" {
		j.Gateway = DefaultGateway
	}
	if j.Name = strings.Join(strings.Fields(j.Name), " "); j.Name == "" {
		j.Name = snippet(j.Prompt, 40)
	}
	j.CreatedAt, j.NextRun = now, now

	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return Job{}, err
	}
	if len(jobs) >= maxJobs {
		return Job{}, fmt.Errorf("too many jobs (max %d): remove one first", maxJobs)
	}
	j.ID = newID(jobs)
	return j, s.save(append(jobs, j))
}

// GoalCheckinTimeout is how long one check-in on a goal may work.
const GoalCheckinTimeout = 30 * time.Minute

// ScheduleGoal makes the goal's check-ins happen every interval, replacing any
// schedule it had. It is safe to call again: a goal has at most one.
func (s *Store) ScheduleGoal(goalID, gateway string, chatID int64, name string, every time.Duration, allowRisky bool, profile, environment string) error {
	if every < MinInterval {
		return fmt.Errorf("check-ins must be at least %s apart", MinInterval)
	}
	if err := s.UnscheduleGoal(goalID); err != nil {
		return err
	}
	_, err := s.Add(Job{
		Name: name, Kind: KindGoal, Goal: goalID, Gateway: gateway, ChatID: chatID, AllowRisky: allowRisky,
		Profile: profile, Environment: environment,
		Schedule: "@every " + every.String(), Prompt: "check in on goal " + goalID,
		TimeoutSec: int(GoalCheckinTimeout / time.Second),
	})
	return err
}

// UnscheduleGoal stops a goal's check-ins.
func (s *Store) UnscheduleGoal(goalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	keep := jobs[:0]
	for _, j := range jobs {
		if !(j.Kind == KindGoal && j.Goal == goalID) {
			keep = append(keep, j)
		}
	}
	if len(keep) == len(jobs) {
		return nil
	}
	return s.save(keep)
}

// WatchPollTimeout is how long one poll of a watch, and the look at what changed, may take.
const WatchPollTimeout = 10 * time.Minute

// MinWatchInterval is the shortest time between polls.
const MinWatchInterval = 5 * time.Minute

// ScheduleWatch makes a watch poll every interval, replacing any schedule it had.
func (s *Store) ScheduleWatch(watchID, gateway string, chatID int64, name string, every time.Duration) error {
	if every < MinWatchInterval {
		return fmt.Errorf("a watch polls at most every %s", MinWatchInterval)
	}
	if err := s.UnscheduleWatch(watchID); err != nil {
		return err
	}
	_, err := s.Add(Job{
		Name: name, Kind: KindWatch, Watch: watchID, Gateway: gateway, ChatID: chatID,
		Schedule: "@every " + every.String(), Prompt: "poll watch " + watchID, TimeoutSec: int(WatchPollTimeout / time.Second),
	})
	return err
}

// UnscheduleWatch stops a watch's polling.
func (s *Store) UnscheduleWatch(watchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	keep := jobs[:0]
	for _, j := range jobs {
		if !(j.Kind == KindWatch && j.Watch == watchID) {
			keep = append(keep, j)
		}
	}
	if len(keep) == len(jobs) {
		return nil
	}
	return s.save(keep)
}

// NextPoll says when a watch will next be polled, if it is scheduled.
func (s *Store) NextPoll(watchID string) (time.Time, bool) {
	jobs, err := s.List()
	if err != nil {
		return time.Time{}, false
	}
	for _, j := range jobs {
		if j.Kind == KindWatch && j.Watch == watchID {
			return j.NextRun, true
		}
	}
	return time.Time{}, false
}

// NextCheckin says when a goal's next check-in is due, if it has one.
func (s *Store) NextCheckin(goalID string) (time.Time, bool) {
	jobs, err := s.List()
	if err != nil {
		return time.Time{}, false
	}
	for _, j := range jobs {
		if j.Kind == KindGoal && j.Goal == goalID {
			return j.NextRun, true
		}
	}
	return time.Time{}, false
}

// Remove deletes a job. If chatID is non-zero, only jobs of that chat on that
// gateway can be removed.
func (s *Store) Remove(id, gateway string, chatID int64) error {
	if gateway == "" {
		gateway = DefaultGateway
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if j.ID == id && (chatID == 0 || (j.ChatID == chatID && j.Gateway == gateway)) {
			return s.save(append(jobs[:i], jobs[i+1:]...))
		}
	}
	return fmt.Errorf("no job with id %q", id)
}

// ClaimDue returns the jobs due at now and, in the same write, moves each
// recurring job to its next future run (so downtime causes at most one
// catch-up run) and deletes one-shot jobs. Runs are therefore at-most-once.
//
// With a gateway named, only that gateway's jobs are claimed; the others are
// left exactly as they are for whoever serves them, so a job for a service that
// is not running waits for it rather than being claimed and lost.
func (s *Store) ClaimDue(now time.Time, gateway string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil || len(jobs) == 0 {
		return nil, err
	}

	var due []Job
	keep := jobs[:0]
	changed := false
	for _, j := range jobs {
		if j.NextRun.After(now) || (gateway != "" && j.Gateway != gateway) {
			keep = append(keep, j)
			continue
		}
		changed = true
		sch, err := ParseSchedule(j.Schedule)
		if err != nil { // hand-edited into nonsense: park it instead of losing it
			j.LastStatus = "invalid schedule: " + err.Error()
			j.NextRun = now.AddDate(100, 0, 0)
			keep = append(keep, j)
			continue
		}
		due = append(due, j)
		if next, ok := sch.Next(now); ok {
			j.NextRun = next
			keep = append(keep, j)
		}
	}
	if !changed {
		return nil, nil
	}
	return due, s.save(keep)
}

// Finish records the outcome of a run, if the job still exists.
func (s *Store) Finish(id string, at time.Time, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.load()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			jobs[i].LastRun, jobs[i].LastStatus = at, status
			return s.save(jobs)
		}
	}
	return nil // one-shot jobs are already gone
}

func newID(existing []Job) string {
	for {
		var b [4]byte
		rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		taken := false
		for _, j := range existing {
			taken = taken || j.ID == id
		}
		if !taken {
			return id
		}
	}
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// plainName accepts an empty name, or a short one without control characters.
func plainName(s string) bool {
	if len(s) > 100 {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
