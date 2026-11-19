package web

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/kanban"
	"talaria/internal/persona"
)

type boardView struct {
	Available bool          `json:"available"`
	Columns   []string      `json:"columns"`
	Cards     []kanban.Card `json:"cards"`
	Agents    []string      `json:"agents"`
	Card      string        `json:"card"`
	Error     string        `json:"error"`
}

func boardRig(t *testing.T) (*rig, *int, *[]string) {
	t.Helper()
	r := newRig(t)
	r.login()
	dir := t.TempDir()
	r.srv.Cards = &kanban.Store{Path: filepath.Join(dir, "sessions.db")}
	r.srv.Agents = &persona.Store{Path: filepath.Join(dir, "agents.json")}
	r.srv.Agents.Save(persona.Agent{Name: "Researcher", Instructions: "look things up"})
	kicks, stops := new(int), new([]string)
	r.srv.Sources.Board = &BoardHooks{
		Kick: func() { *kicks++ },
		Stop: func(job string) bool { *stops = append(*stops, job); return true },
	}
	return r, kicks, stops
}

func (r *rig) board(method string, in any) (int, boardView) {
	r.t.Helper()
	resp, body := r.do(method, "/api/board", in)
	var v boardView
	if err := json.Unmarshal(body, &v); err != nil {
		r.t.Fatalf("%d %s", resp.StatusCode, body)
	}
	return resp.StatusCode, v
}

func TestBoardNeedsASignInAndIsOffWithoutAStore(t *testing.T) {
	r := newRig(t)
	if resp, _ := r.do("GET", "/api/board", nil); resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/board", map[string]any{"action": "add", "title": "x"}); resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.login()
	code, v := r.board("GET", nil)
	if code != 200 || v.Available || len(v.Cards) != 0 {
		t.Fatalf("%d %+v", code, v)
	}
	if code, _ := r.board("POST", map[string]any{"action": "add", "title": "x"}); code != 501 {
		t.Fatalf("add without a store: %d", code)
	}
}

func TestBoardAddMoveEditRemove(t *testing.T) {
	r, kicks, _ := boardRig(t)

	code, v := r.board("POST", map[string]any{"action": "add", "title": "Plan the trip", "body": "three days"})
	if code != 200 || len(v.Cards) != 1 || v.Cards[0].Column != kanban.Backlog || v.Card == "" {
		t.Fatalf("add: %d %+v", code, v)
	}
	if len(v.Agents) != 1 || v.Agents[0] != "Researcher" || len(v.Columns) != 5 {
		t.Fatalf("the page needs the agents and columns: %+v", v)
	}
	if v.Cards[0].Gateway != "" || v.Cards[0].ChatID != 0 {
		t.Fatalf("internal fields leaked: %+v", v.Cards[0])
	}
	id := v.Card
	if *kicks != 0 {
		t.Fatal("a backlog card kicked the dispatcher")
	}

	code, v = r.board("POST", map[string]any{"action": "edit", "id": id, "title": "Plan the Accra trip", "agent": "researcher", "allow_risky": true})
	c := v.Cards[0]
	if code != 200 || c.Title != "Plan the Accra trip" || c.Agent != "Researcher" || !c.AllowRisky || c.Body != "three days" {
		t.Fatalf("edit: %d %+v", code, c)
	}

	code, v = r.board("POST", map[string]any{"action": "move", "id": id, "column": "ready"})
	if code != 200 || v.Cards[0].Column != kanban.Ready || *kicks != 1 {
		t.Fatalf("move: %d %+v kicks=%d", code, v.Cards, *kicks)
	}
	// The page can give an agent in the same move.
	code, v = r.board("POST", map[string]any{"action": "move", "id": id, "column": "review"})
	code, v = r.board("POST", map[string]any{"action": "move", "id": id, "column": "ready", "agent": "main", "allow_risky": false})
	if code != 200 || v.Cards[0].Agent != kanban.Main || v.Cards[0].AllowRisky || *kicks != 2 {
		t.Fatalf("move with agent: %d %+v", code, v.Cards)
	}

	code, v = r.board("POST", map[string]any{"action": "move", "id": id, "column": "done"})
	if code != 200 || v.Cards[0].Column != kanban.Done {
		t.Fatalf("move to done: %d %+v", code, v.Cards)
	}
	code, v = r.board("POST", map[string]any{"action": "remove", "id": id})
	if code != 200 || len(v.Cards) != 0 {
		t.Fatalf("remove: %d %+v", code, v)
	}
}

func TestBoardAddStraightToReadyKicks(t *testing.T) {
	r, kicks, _ := boardRig(t)
	code, v := r.board("POST", map[string]any{"action": "add", "title": "x", "column": "ready", "agent": "Researcher"})
	if code != 200 || v.Cards[0].Column != kanban.Ready || *kicks != 1 {
		t.Fatalf("%d %+v kicks=%d", code, v.Cards, *kicks)
	}
}

func TestBoardRefusals(t *testing.T) {
	r, _, _ := boardRig(t)
	_, v := r.board("POST", map[string]any{"action": "add", "title": "x"})
	id := v.Card
	for name, in := range map[string]map[string]any{
		"no title":        {"action": "add", "title": " "},
		"to running":      {"action": "add", "title": "x", "column": "running"},
		"to done":         {"action": "add", "title": "x", "column": "done"},
		"unknown agent":   {"action": "add", "title": "x", "agent": "Nobody"},
		"unknown action":  {"action": "explode"},
		"edit unknown":    {"action": "edit", "id": "nope", "title": "x"},
		"move to running": {"action": "move", "id": id, "column": "running"},
		"move to nowhere": {"action": "move", "id": id, "column": "later"},
		"move unknown":    {"action": "move", "id": "nope", "column": "done"},
		"edit bad agent":  {"action": "edit", "id": id, "agent": "Nobody"},
		"stop idle":       {"action": "stop", "id": id},
		"remove unknown":  {"action": "remove", "id": "nope"},
		"title too long":  {"action": "edit", "id": id, "title": strings.Repeat("x", 500)},
	} {
		code, got := r.board("POST", in)
		if code < 400 || got.Error == "" {
			t.Errorf("%s: %d %+v", name, code, got)
		}
	}
	if code, _ := r.board("POST", map[string]any{"action": "move", "id": "nope", "column": "done"}); code != 404 {
		t.Errorf("an unknown card should be 404, got %d", code)
	}
	if _, v := r.board("GET", nil); len(v.Cards) != 1 || v.Cards[0].Column != kanban.Backlog {
		t.Fatalf("refused calls changed the board: %+v", v.Cards)
	}
}

func TestBoardStopAndTheRunningLock(t *testing.T) {
	r, _, stops := boardRig(t)
	_, v := r.board("POST", map[string]any{"action": "add", "title": "x", "column": "ready", "agent": "main"})
	id := v.Card
	// What the dispatcher does: claim it and attach its task.
	if _, ok, err := r.srv.Cards.Claim(id); !ok || err != nil {
		t.Fatal("could not claim", err)
	}
	r.srv.Cards.Attach(id, "job-9")

	for name, in := range map[string]map[string]any{
		"move":   {"action": "move", "id": id, "column": "done"},
		"edit":   {"action": "edit", "id": id, "title": "y"},
		"remove": {"action": "remove", "id": id},
	} {
		if code, _ := r.board("POST", in); code != 400 {
			t.Errorf("a running card could be changed by %s: %d", name, code)
		}
	}
	code, _ := r.board("POST", map[string]any{"action": "stop", "id": id})
	if code != 200 || len(*stops) != 1 || (*stops)[0] != "job-9" {
		t.Fatalf("stop: %d %v", code, *stops)
	}
}

func TestBoardScrubsWhatACardReported(t *testing.T) {
	r, _, _ := boardRig(t)
	r.srv.Scrub = func(s string) string { return strings.ReplaceAll(s, "hunter2", "[hidden]") }
	_, v := r.board("POST", map[string]any{"action": "add", "title": "x", "column": "ready", "agent": "main"})
	id := v.Card
	r.srv.Cards.Claim(id)
	r.srv.Cards.Attach(id, "j")
	r.srv.Cards.Finish(id, "j", "", "logged in with hunter2")
	_, v = r.board("GET", nil)
	if strings.Contains(v.Cards[0].Result, "hunter2") || !strings.Contains(v.Cards[0].Result, "[hidden]") {
		t.Fatalf("result = %q", v.Cards[0].Result)
	}
}

func TestBoardIsPerChat(t *testing.T) {
	r, _, _ := boardRig(t)
	r.srv.Cards.Add("telegram", 5, kanban.Card{Title: "someone else's"})
	r.srv.Cards.Add("web", ChatID+1, kanban.Card{Title: "another chat"})
	if _, v := r.board("GET", nil); len(v.Cards) != 0 {
		t.Fatalf("the board showed another chat's cards: %+v", v.Cards)
	}
}
