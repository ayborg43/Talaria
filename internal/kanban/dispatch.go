package kanban

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"talaria/internal/cron"
)

// Dispatcher turns Ready cards into background tasks, and keeps Running cards honest about their tasks.
//
// A task started for a card is the ordinary kind (see cron.StartTask): it runs on its own with a copy of the tools, is
// held to the chat's limit of three at a time, and has risky tools refused unless the card was started with
// AllowRisky. A card that finds no room stays Ready and is tried again on the scheduler's next look.
type Dispatcher struct {
	Cards *Store
	Jobs  *cron.Store
	// Known says whether a saved agent of that name exists now. It is asked at each start, so one removed in the app
	// stops its cards rather than quietly running them as something else.
	Known func(name string) bool
	Logf  func(format string, args ...any)

	mu sync.Mutex
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// taskPrompt is what the task is asked: the card as the user wrote it. A task has no memory of any conversation, so
// the card has to say it all.
func taskPrompt(c Card) string {
	var b strings.Builder
	b.WriteString("You have been given a card from the user's board to do.\n\nCard: " + c.Title)
	if c.Body != "" {
		b.WriteString("\n\n" + c.Body)
	}
	b.WriteString("\n\nDo the work, then reply with what you did and found, so the user can review it. If you could not finish, say what stopped you.")
	return b.String()
}

// Sweep starts what is waiting and settles what has stopped, for one gateway. ctl is that gateway's scheduler, which
// knows what is running. It is safe to call from anywhere, as often as wanted.
func (d *Dispatcher) Sweep(gateway string, ctl cron.Control) {
	if d == nil || d.Cards == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reconcile(gateway)
	cards, err := d.Cards.Dispatchable(gateway)
	if err != nil {
		d.logf("kanban: %v", err)
		return
	}
	full := map[int64]bool{} // chats that have no room for another task just now
	for _, c := range cards {
		if full[c.ChatID] {
			continue
		}
		if c.Agent != Main && (d.Known == nil || !d.Known(c.Agent)) {
			d.Cards.Settle(c.ID, StatusFailed, fmt.Sprintf("There is no saved agent called %q any more. Pick another one and move the card to Ready again.", c.Agent))
			continue
		}
		claimed, ok, err := d.Cards.Claim(c.ID)
		if err != nil || !ok {
			continue
		}
		job := cron.Job{Name: jobName(claimed.Title), Prompt: taskPrompt(claimed), Gateway: gateway, ChatID: claimed.ChatID,
			AllowRisky: claimed.AllowRisky, Card: claimed.ID}
		if claimed.Agent != Main {
			job.Agent = claimed.Agent
		}
		j, err := cron.StartTask(d.Jobs, ctl, job)
		switch {
		case err == nil:
			if err := d.Cards.Attach(claimed.ID, j.ID); err != nil {
				d.logf("kanban: %v", err)
			}
		case cron.IsBusy(err):
			d.Cards.Unclaim(claimed.ID)
			full[c.ChatID] = true
		default:
			d.Cards.Settle(claimed.ID, StatusFailed, "It could not be started: "+err.Error())
		}
	}
}

func jobName(title string) string {
	if r := []rune(title); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return title
}

// reconcile settles running cards whose task is gone without having reported: a task removed before it started, or
// cut off by a restart. A card whose task was never made (a crash between the two) goes back to Ready.
func (d *Dispatcher) reconcile(gateway string) {
	running, err := d.Cards.RunningCards(gateway)
	if err != nil || len(running) == 0 {
		return
	}
	jobs := map[string]bool{}
	if list, err := d.Jobs.List(); err == nil {
		for _, j := range list {
			jobs[j.ID] = true
		}
	} else {
		return
	}
	now := d.Cards.now()
	for _, c := range running {
		switch {
		case c.Job == "" && now.Sub(c.Started) > 2*time.Minute:
			d.Cards.Unclaim(c.ID)
		case c.Job != "" && !jobs[c.Job]:
			d.Cards.Settle(c.ID, StatusLost, "Its task ended without a result: it was cancelled before it started, or a restart cut it off. Move the card to Ready to try again.")
		}
	}
}

// Finished is the scheduler's OnFinish for a job: if the job worked on a card, the card follows it.
func (d *Dispatcher) Finished(j cron.Job, status, result string) {
	if d == nil || d.Cards == nil || j.Card == "" {
		return
	}
	st := ""
	switch status {
	case "ok":
		if strings.TrimSpace(result) == "" {
			result = "It finished without a report."
		}
	case "cancelled":
		st, result = StatusCancelled, "It was stopped before it finished."
	case "dropped":
		st, result = StatusLost, "It was dropped before it finished."
	default:
		st = StatusFailed
		if result == "" {
			result = "It failed."
		}
	}
	if _, ok := d.Cards.Finish(j.Card, j.ID, st, result); !ok {
		d.logf("kanban: card %s has no running record of job %s", j.Card, j.ID)
	}
}
