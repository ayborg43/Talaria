package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/provider"
	"talaria/internal/usage"
)

// Once a spending limit is reached, a task someone started says why it did not run (and is not retried in a minute),
// while a scheduled prompt or a goal is skipped quietly. The model is never asked.
func TestJobsHeldBackPastTheSpendingLimit(t *testing.T) {
	writer := &countingModel{p: &writerModel{path: filepath.Join(t.TempDir(), "out.txt")}}
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: writer, Model: "m", Env: listEnv{name: "local"}})
	defer rt.Close()
	ledger := usage.Open(filepath.Join(t.TempDir(), "usage.json"), nil)
	ledger.Add(provider.Spent{Profile: "paid", Usage: provider.Usage{In: 1_000_000}})
	spend := &usage.Cap{Ledger: ledger, Limits: func() usage.Limits { return usage.Limits{Day: 1} },
		Prices: func(string) (usage.Price, bool) { return usage.Price{In: 2}, true }} // $2 spent today
	r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, spend: spend}

	_, err := r.run(context.Background(), cron.Job{Kind: cron.KindTask, Prompt: "write", ChatID: 1})
	if !errors.Is(err, usage.ErrPaused) {
		t.Fatalf("task: %v", err)
	}
	for _, kind := range []string{"", cron.KindGoal} {
		out, err := r.run(context.Background(), cron.Job{Kind: kind, Prompt: "write", ChatID: 1, Goal: "g"})
		if err != nil || out != cron.Silent {
			t.Fatalf("kind %q: %q %v", kind, out, err)
		}
	}
	if writer.n.Load() != 0 {
		t.Fatalf("the model was asked %d times", writer.n.Load())
	}

	spend.Limits = func() usage.Limits { return usage.Limits{Day: 5} } // raised: runs again
	if out, err := r.run(context.Background(), cron.Job{Prompt: "write", ChatID: 1}); err != nil || out == cron.Silent {
		t.Fatalf("under the limit: %q %v", out, err)
	}
}

type countingModel struct {
	p provider.Provider
	n atomic.Int64
}

func (m *countingModel) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	m.n.Add(1)
	return m.p.Chat(ctx, req)
}

// The limit is also asked before each step of a run, so work that crosses it part way stops there; a chat does not.
func TestSpendGateStopsBackgroundRunsMidway(t *testing.T) {
	ledger := usage.Open(filepath.Join(t.TempDir(), "usage.json"), nil)
	ledger.Add(provider.Spent{Profile: "paid", Usage: provider.Usage{In: 1_000_000}})
	spend := &usage.Cap{Ledger: ledger, Limits: func() usage.Limits { return usage.Limits{Month: 1} },
		Prices: func(string) (usage.Price, bool) { return usage.Price{In: 2}, true }}
	model := &countingModel{p: &writerModel{path: filepath.Join(t.TempDir(), "out.txt")}}
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: model, Model: "m", Env: listEnv{name: "local"}, SpendGate: spend.Check})
	defer rt.Close()

	bg := provider.WithLabel(context.Background(), provider.Label{Source: "task", Background: true})
	if _, err := rt.NewAgent(app.AgentOptions{}).Run(bg, "write"); !errors.Is(err, usage.ErrPaused) || model.n.Load() != 0 {
		t.Fatalf("background run: %v, %d calls", err, model.n.Load())
	}
	chat := provider.WithLabel(context.Background(), provider.Label{Source: "chat"})
	if _, err := rt.NewAgent(app.AgentOptions{}).Run(chat, "write"); err != nil || model.n.Load() == 0 {
		t.Fatalf("chat run: %v, %d calls", err, model.n.Load())
	}
}
