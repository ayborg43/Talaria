package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"talaria/internal/tool"
)

func run(t *testing.T, reg *tool.Registry, args string) (string, error) {
	t.Helper()
	tl, ok := reg.Get("telegram_notify")
	if !ok {
		t.Fatal("not registered")
	}
	return tl.Run(context.Background(), []byte(args))
}

func TestTheToolSendsTextToTheUserAndNeedsNoPermission(t *testing.T) {
	var got []string
	reg := tool.NewRegistry()
	RegisterTool(reg, func(_ context.Context, s string) error { got = append(got, s); return nil }, &Limit{})
	if tl, _ := reg.Get("telegram_notify"); tl.NeedsApproval || tl.RiskOf(nil) != 0 {
		t.Fatal("a message to the user's own chat must not ask")
	}
	if _, err := run(t, reg, `{"text":"  the report is ready  "}`); err != nil || len(got) != 1 || got[0] != "the report is ready" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := run(t, reg, `{"text":" "}`); err == nil {
		t.Fatal("an empty message was sent")
	}
	if _, err := run(t, reg, `{"text":"`+strings.Repeat("é", maxRunes+50)+`"}`); err != nil || len([]rune(got[1])) > maxRunes+30 {
		t.Fatalf("a long message was not cut: %d %v", len([]rune(got[1])), err)
	}
}

func TestAFailedSendIsReportedAndTheLimitHolds(t *testing.T) {
	now := time.Now()
	lim := &Limit{Now: func() time.Time { return now }}
	reg := tool.NewRegistry()
	RegisterTool(reg, func(context.Context, string) error { return errors.New("telegram is down") }, lim)
	if _, err := run(t, reg, `{"text":"hi"}`); err == nil || !strings.Contains(err.Error(), "telegram is down") {
		t.Fatalf("%v", err)
	}
	reg = tool.NewRegistry()
	sent := 0
	RegisterTool(reg, func(context.Context, string) error { sent++; return nil }, lim)
	for i := 0; i < perHour+5; i++ {
		run(t, reg, `{"text":"hi"}`)
	}
	if sent != perHour-1 { // one slot went to the failed attempt above
		t.Fatalf("sent %d, want %d", sent, perHour-1)
	}
	now = now.Add(61 * time.Minute)
	if _, err := run(t, reg, `{"text":"hi"}`); err != nil {
		t.Fatalf("the limit never lifts: %v", err)
	}
}
