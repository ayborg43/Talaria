package kanban

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"talaria/internal/tool"
)

// A tool whose schema is not valid JSON breaks every request to the model, so each one must parse, with and without
// saved agents (whose names are written into the description).
func TestEveryBoardToolHasAValidSchema(t *testing.T) {
	for _, names := range [][]string{nil, {"Researcher", `He said "hi"`}} {
		reg := tool.NewRegistry()
		names := names
		RegisterTools(reg, Binding{Store: &Store{Path: filepath.Join(t.TempDir(), "s.db")}, Gateway: "web", ChatID: 1, Create: true,
			Agents: func() []string { return names }})
		specs := reg.Specs()
		if len(specs) < 4 {
			t.Fatalf("only %d board tools", len(specs))
		}
		for _, s := range specs {
			var v map[string]any
			if err := json.Unmarshal(s.Parameters, &v); err != nil {
				t.Errorf("%s (agents %v): %v: %s", s.Name, names, err, s.Parameters)
			}
		}
	}
}
