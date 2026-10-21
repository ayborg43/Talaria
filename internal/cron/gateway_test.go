package cron

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/tool"
)

// Jobs written before there was more than one gateway have none recorded, and
// belong to Telegram, which is what they were delivered through.
func TestJobsWithoutAGatewayBelongToTelegram(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	raw := `{"jobs":[{"id":"old1","name":"n","schedule":"@daily","prompt":"p","chat_id":5,` +
		`"allow_risky":false,"created_at":"2026-10-19T10:00:00Z","next_run":"2026-10-20T09:00:00Z"}]}`
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.List()
	if err != nil || len(jobs) != 1 || jobs[0].Gateway != "telegram" {
		t.Fatalf("an old job should be Telegram's: %+v %v", jobs, err)
	}
	due, _ := s.ClaimDue(utc("2026-10-20 10:00"), "telegram")
	if len(due) != 1 {
		t.Fatalf("Telegram's scheduler should run the old job, got %+v", due)
	}
}

// Two services may have a chat with the same number; they are different chats.
func TestChatsOfDifferentGatewaysAreKeptApart(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	tg, _ := s.Add(Job{Gateway: "telegram", Schedule: "@daily", Prompt: "tg", ChatID: 7})
	dc, _ := s.Add(Job{Gateway: "discord", Schedule: "@daily", Prompt: "dc", ChatID: 7})

	if err := s.Remove(tg.ID, "discord", 7); err == nil {
		t.Fatal("a Discord chat removed a Telegram job")
	}
	if err := s.Remove(dc.ID, "telegram", 7); err == nil {
		t.Fatal("a Telegram chat removed a Discord job")
	}

	reg := tool.NewRegistry()
	RegisterTools(reg, s, "discord", 7, Targets{})
	list, _ := reg.Get("cron_list")
	out, err := list.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, dc.ID) || strings.Contains(out, tg.ID) {
		t.Fatalf("a chat sees only its own gateway's jobs: %q %v", out, err)
	}
	rm, _ := reg.Get("cron_remove")
	if _, err := rm.Run(context.Background(), json.RawMessage(`{"id":"`+tg.ID+`"}`)); err == nil {
		t.Fatal("cron_remove reached another gateway's job")
	}

	add, _ := reg.Get("cron_add")
	if _, err := add.Run(context.Background(), json.RawMessage(`{"delay":"10m","prompt":"x"}`)); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	last := jobs[len(jobs)-1]
	if last.Gateway != "discord" || last.ChatID != 7 {
		t.Fatalf("a job made from a Discord chat is a Discord job: %+v", last)
	}
}

// A scheduler claims only its own gateway's jobs: another's stay due, untouched,
// for when that gateway runs, instead of being run through the wrong service or lost.
func TestSchedulerLeavesOtherGatewaysJobsAlone(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "done", nil })
	h.sched.Gateway = "discord"
	h.store.Add(Job{Gateway: "telegram", Name: "tg", Schedule: "@every 5m", Prompt: "tg", ChatID: 1})
	h.store.Add(Job{Gateway: "discord", Name: "dc", Schedule: "@every 5m", Prompt: "dc", ChatID: 2})

	h.fireAt("2026-10-20 10:06")
	if len(h.got) != 1 || h.got[0].chat != 2 {
		t.Fatalf("only the Discord job should run: %+v", h.got)
	}
	jobs, _ := h.store.List()
	for _, j := range jobs {
		if j.Gateway == "telegram" && (!j.LastRun.IsZero() || !j.NextRun.Equal(utc("2026-10-20 10:05"))) {
			t.Fatalf("the Telegram job must be left exactly as it was, still due: %+v", j)
		}
	}

	// When Telegram's scheduler does run, the job is still there to claim.
	h.sched.Gateway = "telegram"
	h.fireAt("2026-10-20 10:07")
	if len(h.got) != 2 || h.got[1].chat != 1 {
		t.Fatalf("the waiting Telegram job should run now: %+v", h.got)
	}
}
