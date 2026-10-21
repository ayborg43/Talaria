package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func TestMemoryAndSearchTools(t *testing.T) {
	dir := t.TempDir()
	notes := &Notes{Dir: dir}
	sessions, err := OpenSessions(dir + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	prev, _ := sessions.Start()
	sessions.Add(prev, "user", "remember the quokka migration plan")
	cur, _ := sessions.Start()

	reg := tool.NewRegistry()
	RegisterTools(reg, notes, sessions, cur)
	run := func(name, args string) (string, error) {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		return tl.Run(context.Background(), json.RawMessage(args))
	}

	out, err := run("memory", `{"action":"add","target":"user","content":"likes terse answers"}`)
	if err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("memory add: %q %v", out, err)
	}
	if _, err := run("memory", `{"action":"nope","target":"user"}`); err == nil {
		t.Fatal("unknown action should error")
	}
	if p := notes.Prompt(); !strings.Contains(p, "likes terse answers") {
		t.Fatalf("note not saved: %s", p)
	}

	out, err = run("session_search", `{"query":"quokka"}`)
	if err != nil || !strings.Contains(out, prev) {
		t.Fatalf("session_search: %q %v", out, err)
	}
	if out, _ := run("session_search", `{"query":"zebra"}`); !strings.Contains(out, "no matches") {
		t.Fatalf("expected no matches, got %q", out)
	}
}

func TestSessionSearchToolOmittedWithoutDB(t *testing.T) {
	reg := tool.NewRegistry()
	RegisterTools(reg, &Notes{Dir: t.TempDir()}, nil, "")
	if _, ok := reg.Get("session_search"); ok {
		t.Fatal("session_search should not register without a database")
	}
	if _, ok := reg.Get("memory"); !ok {
		t.Fatal("memory tool should still register")
	}
}
