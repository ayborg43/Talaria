package goal

import (
	"context"
	"encoding/json"
	"testing"
)

// The goal tools hand a front end the goal as a record, so a checklist is drawn from the plan, not read out of text.
func TestGoalToolsReturnThePlanAsARecord(t *testing.T) {
	e := newEnv(t, true)
	create, _ := e.reg.Get("goal_create")
	res, err := create.Execute(context.Background(), json.RawMessage(`{"title":"Plan the trip","objective":"all booked","plan":["Read the email","Fill the slip"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kind, ID, Title, Status, Next string
		Steps                         []struct {
			Text string
			Done bool
		}
	}
	if err := json.Unmarshal(res.Artifact, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Artifact)
	}
	if got.Kind != "plan" || got.ID == "" || got.Title != "Plan the trip" || got.Status != Open || len(got.Steps) != 2 || got.Steps[0].Done || got.Next != "Read the email" {
		t.Fatalf("%+v", got)
	}
	if res.Text == "" {
		t.Fatal("the model's text is gone")
	}
	get, _ := e.reg.Get("goal_get")
	res, err = get.Execute(context.Background(), json.RawMessage(`{"id":"`+got.ID+`"}`))
	if err != nil || len(res.Artifact) == 0 {
		t.Fatalf("goal_get: %v %s", err, res.Artifact)
	}
}
