package tool

import (
	"encoding/json"
	"testing"
)

// A schema with "required": null makes OpenAI refuse the whole request, so none may reach a provider.
func TestSpecsNeverSendNullRequired(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "a", Schema: json.RawMessage(`{"type":"object","properties":{},"required":null}`)})
	r.Register(Tool{Name: "b", Schema: json.RawMessage(`{"type":"object","properties":null,"required":["x"]}`)})
	r.Register(Tool{Name: "c", Schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","default":null}},"required":["x"]}`)})
	for _, sp := range r.Specs() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sp.Parameters, &m); err != nil {
			t.Fatalf("%s: %v", sp.Name, err)
		}
		for _, k := range []string{"required", "properties"} {
			if string(m[k]) == "null" {
				t.Errorf("%s: %q is null in %s", sp.Name, k, sp.Parameters)
			}
		}
	}
	got := r.Specs()[2].Parameters
	if string(got) != `{"type":"object","properties":{"x":{"type":"string","default":null}},"required":["x"]}` {
		t.Errorf("a schema with nothing to fix was rewritten: %s", got)
	}
}
