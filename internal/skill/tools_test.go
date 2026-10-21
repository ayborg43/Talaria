package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func TestSkillTools(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	reg := tool.NewRegistry()
	RegisterTools(reg, s)
	run := func(name, args string) (string, error) {
		tl, _ := reg.Get(name)
		return tl.Run(context.Background(), json.RawMessage(args))
	}

	for _, name := range []string{"skill_save", "skill_delete"} {
		if tl, _ := reg.Get(name); !tl.NeedsApproval {
			t.Errorf("%s must require approval", name)
		}
	}

	if out, _ := run("skill_list", `{}`); !strings.Contains(out, "no skills") {
		t.Fatalf("empty list: %q", out)
	}
	if out, err := run("skill_save", `{"name":"a-skill","description":"d","body":"step one"}`); err != nil || !strings.Contains(out, "created") {
		t.Fatalf("save: %q %v", out, err)
	}
	if out, _ := run("skill_save", `{"name":"a-skill","description":"d","body":"step two"}`); !strings.Contains(out, "updated") {
		t.Fatalf("second save should report update: %q", out)
	}
	if out, err := run("skill_view", `{"name":"a-skill"}`); err != nil || !strings.Contains(out, "step two") {
		t.Fatalf("view: %q %v", out, err)
	}
	if _, err := run("skill_view", `{"name":"../x"}`); err == nil {
		t.Fatal("view accepted a bad name")
	}
	if _, err := run("skill_delete", `{"name":"a-skill"}`); err != nil {
		t.Fatal(err)
	}
}
