package cron

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxDoneTasks is how many finished background tasks are remembered, across
// every chat: enough to retry what just failed, small enough to stay a few
// kilobytes on disk and nothing in memory.
const maxDoneTasks = 20

// DoneTask is a background task that has ended, kept so it can be run again.
type DoneTask struct {
	Job    Job       `json:"job"`
	Status string    `json:"status"` // ok, cancelled, or "error: ..."
	At     time.Time `json:"at"`
}

// Failed says the task did not finish its work: it errored or was cancelled.
func (d DoneTask) Failed() bool { return d.Status != "ok" && !strings.HasPrefix(d.Status, "ok ") }

func (s *Store) donePath() string { return filepath.Join(filepath.Dir(s.Path), "tasks_done.json") }

func (s *Store) loadDone() []DoneTask {
	raw, err := os.ReadFile(s.donePath())
	if err != nil {
		return nil
	}
	var out []DoneTask
	if json.Unmarshal(raw, &out) != nil {
		return nil // a damaged record of finished tasks is not worth failing a job over
	}
	return out
}

func (s *Store) saveDone(list []DoneTask) error {
	if len(list) > maxDoneTasks {
		list = list[len(list)-maxDoneTasks:]
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := s.donePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.donePath())
}

// recordDone remembers a task that has just left the job list. The caller
// holds the store's lock.
func (s *Store) recordDone(j Job, status string, at time.Time) {
	j.ClaimedAt, j.LeaseUntil, j.Attempts = time.Time{}, time.Time{}, 0
	_ = s.saveDone(append(s.loadDone(), DoneTask{Job: j, Status: snippet(status, 300), At: at}))
}

// DoneTasks lists a chat's finished background tasks, newest first.
func (s *Store) DoneTasks(gateway string, chatID int64) []DoneTask {
	if gateway == "" {
		gateway = DefaultGateway
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []DoneTask
	all := s.loadDone()
	for i := len(all) - 1; i >= 0; i-- {
		if d := all[i]; d.Job.Gateway == gateway && d.Job.ChatID == chatID {
			out = append(out, d)
		}
	}
	return out
}

// RetryTask starts a finished task again, as it was: the same instructions,
// model, environment and permissions the user approved the first time. The
// old record goes once the new task is stored, so one failure cannot be
// retried into several copies.
func (s *Store) RetryTask(id, gateway string, chatID int64, running map[string]bool, max int) (Job, error) {
	if gateway == "" {
		gateway = DefaultGateway
	}
	s.mu.Lock()
	all := s.loadDone()
	at := -1
	for i, d := range all {
		if d.Job.ID == id && d.Job.Gateway == gateway && d.Job.ChatID == chatID {
			at = i
		}
	}
	s.mu.Unlock()
	if at < 0 {
		return Job{}, errors.New("no finished task with that id is kept: only the last few are")
	}
	old := all[at].Job
	j, err := s.AddTask(Job{Name: old.Name, Kind: KindTask, Prompt: old.Prompt, Gateway: gateway, ChatID: chatID,
		AllowRisky: old.AllowRisky, Profile: old.Profile, Environment: old.Environment, TimeoutSec: old.TimeoutSec}, running, max)
	if err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all = s.loadDone()
	for i, d := range all {
		if d.Job.ID == id && d.Job.Gateway == gateway && d.Job.ChatID == chatID {
			all = append(all[:i], all[i+1:]...)
			break
		}
	}
	_ = s.saveDone(all)
	return j, nil
}

// RetryChatTask runs one of a chat's finished tasks again, at once, within the
// same limit on how many may be going as task_start has.
func RetryChatTask(s *Store, ctl Control, gateway string, chatID int64, id string) (Job, error) {
	if gateway == "" {
		gateway = DefaultGateway
	}
	j, err := s.RetryTask(id, gateway, chatID, runningTasks(ctl, gateway, chatID), maxTasksPerChat)
	if err != nil {
		return Job{}, err
	}
	if ctl != nil {
		ctl.Wake()
	}
	return j, nil
}
