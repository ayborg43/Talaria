package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/persona"
)

func TestACardsTaskWorksAsItsSavedAgent(t *testing.T) {
	home := t.TempDir()
	model := &spy{reply: "three options"}
	rt := app.Open(app.Config{Home: home, Provider: model, Model: "m", Env: execenv.Local{}})
	t.Cleanup(rt.Close)
	agents := &persona.Store{Path: filepath.Join(home, "agents.json")}
	if _, err := agents.Save(persona.Agent{Name: "Researcher", Instructions: "Always give exactly three options.", ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, agents: agents}

	reply, err := r.run(context.Background(), cron.Job{ID: "j1", Name: "Find flights", Kind: cron.KindTask, Prompt: "Card: find flights",
		Gateway: "web", ChatID: 1, Agent: "researcher", AllowRisky: true})
	if err != nil || reply != "three options" {
		t.Fatalf("reply %q, err %v", reply, err)
	}
	if len(model.system) != 1 || !strings.Contains(model.system[0], "Always give exactly three options.") || !strings.Contains(model.system[0], "Researcher") {
		t.Fatalf("the saved agent's instructions did not reach the model: %q", model.system)
	}
	if !strings.Contains(model.system[0], "look only") {
		t.Fatalf("a look-only agent was not told so: %q", model.system[0])
	}
}

func TestACardsTaskWithoutASavedAgentIsTheMainAgent(t *testing.T) {
	home := t.TempDir()
	model := &spy{reply: "done"}
	rt := app.Open(app.Config{Home: home, Provider: model, Model: "m", Env: execenv.Local{}})
	t.Cleanup(rt.Close)
	r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, agents: &persona.Store{Path: filepath.Join(home, "agents.json")}}
	if _, err := r.run(context.Background(), cron.Job{ID: "j", Kind: cron.KindTask, Prompt: "p", Gateway: "web", ChatID: 1}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.system[0], "named agents") {
		t.Fatalf("an ordinary task was given a saved agent's role: %q", model.system[0])
	}
}

func TestACardsTaskWhoseAgentWasRemovedFailsInsteadOfRunningAsSomethingElse(t *testing.T) {
	home := t.TempDir()
	model := &spy{reply: "x"}
	rt := app.Open(app.Config{Home: home, Provider: model, Model: "m", Env: execenv.Local{}})
	t.Cleanup(rt.Close)
	r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, agents: &persona.Store{Path: filepath.Join(home, "agents.json")}}
	_, err := r.run(context.Background(), cron.Job{ID: "j", Kind: cron.KindTask, Prompt: "p", Gateway: "web", ChatID: 1, Agent: "Gone"})
	if err == nil || !strings.Contains(err.Error(), "Gone") {
		t.Fatalf("err = %v", err)
	}
	if model.calls() != 0 {
		t.Fatal("the model was called for a card whose agent no longer exists")
	}
}
