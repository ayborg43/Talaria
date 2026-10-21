package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// RegisterTools adds skill_list, skill_view, skill_save and skill_delete.
// Writes need approval because a saved skill steers every future session.
func RegisterTools(r *tool.Registry, s *Store) {
	r.Register(tool.Tool{
		Name:        "skill_list",
		Description: "List all saved skills with their descriptions.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			idx := s.Index()
			if idx == "" {
				return "no skills saved yet", nil
			}
			return idx, nil
		},
	})

	r.Register(tool.Tool{
		Name:        "skill_view",
		Description: "Load the full instructions of a saved skill by name.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			sk, err := s.Load(a.Name)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("# %s\n%s\n\n%s", sk.Name, sk.Description, sk.Body), nil
		},
	})

	r.Register(tool.Tool{
		Name: "skill_save",
		Description: "Create a skill, or overwrite an existing one to improve it. Use after solving a non-trivial task " +
			"or when a skill you followed was wrong or incomplete. Body is markdown: when to use it, steps, pitfalls. " +
			"To edit, skill_view first, then save the full revised body.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"lowercase-hyphenated, e.g. deploy-static-site"},
			"description":{"type":"string","description":"one line: what it does and when to use it"},
			"body":{"type":"string"}},
			"required":["name","description","body"]}`),
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Name, Description, Body string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			_, loadErr := s.Load(a.Name)
			if err := s.Save(a.Name, a.Description, a.Body); err != nil {
				return "", err
			}
			if loadErr == nil {
				return "updated skill " + a.Name, nil
			}
			return "created skill " + a.Name, nil
		},
	})

	r.Register(tool.Tool{
		Name:          "skill_delete",
		Description:   "Delete a saved skill by name.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if err := s.Delete(strings.TrimSpace(a.Name)); err != nil {
				return "", err
			}
			return "deleted skill " + a.Name, nil
		},
	})
}
