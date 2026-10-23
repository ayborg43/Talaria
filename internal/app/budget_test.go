package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/execenv"
	"talaria/internal/provider"
)

func TestTheHourlyBudgetRefusesAfterTheLimitAndRecovers(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	b := &budget{max: 3, now: func() time.Time { return clock }}
	for i := 0; i < 3; i++ {
		if err := b.take(); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		clock = clock.Add(time.Minute)
	}
	err := b.take()
	if err == nil || !strings.Contains(err.Error(), "limit of 3") || !strings.Contains(err.Error(), "model_calls_per_hour") {
		t.Fatalf("%v", err)
	}
	clock = clock.Add(57 * time.Minute) // the first request is now an hour old, the second is not
	if err := b.take(); err != nil {
		t.Fatalf("it lifts as the hour passes: %v", err)
	}
	if err := b.take(); err == nil {
		t.Fatal("but only for as many as have aged out")
	}
	var none *budget
	if none.take() != nil || (&budget{}).take() != nil {
		t.Fatal("no budget, no limit")
	}
}

func TestTheBudgetIsSharedByEveryAgentAndStopsARunBeforeItReachesTheModel(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	model := chatFunc(func(context.Context, provider.Request) (provider.Message, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	})
	home := t.TempDir()
	rt := Open(Config{Home: home, Provider: model, Model: "m", Env: execenv.Local{}, ModelCallsPerHour: 2})
	defer rt.Close()
	a, b := rt.NewAgent(AgentOptions{}), rt.NewAgent(AgentOptions{})
	if _, err := a.Run(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	_, err := a.Run(context.Background(), "three")
	if err == nil || !strings.Contains(err.Error(), "limit of 2") {
		t.Fatalf("a third request, by either agent, is over the limit: %v", err)
	}
	if calls != 2 {
		t.Fatalf("the model was asked %d times: a refused request must never reach it", calls)
	}
}
