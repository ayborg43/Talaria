package trigger

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"talaria/internal/phone"
	"talaria/internal/tool"
)

type conn struct {
	mu   sync.Mutex
	sent []string
}

func (c *conn) Send(b []byte) error {
	c.mu.Lock()
	c.sent = append(c.sent, string(b))
	c.mu.Unlock()
	return nil
}
func (c *conn) Close() {}
func (c *conn) pushed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.sent {
		if strings.Contains(s, `"t":"triggers"`) {
			n++
		}
	}
	return n
}

func rig(t *testing.T, caps ...string) (*tool.Registry, *Store, *phone.Hub, *conn) {
	t.Helper()
	s := &Store{}
	h := phone.NewHub()
	h.Triggers = func() []phone.Spec { return Specs(s) }
	c := &conn{}
	if caps != nil {
		l := h.Attach(c)
		raw, _ := json.Marshal(map[string]any{"t": "hello", "name": "P", "caps": caps})
		l.Receive(raw)
	}
	r := tool.NewRegistry()
	RegisterTools(r, s, h)
	return r, s, h, c
}

func TestTheAgentCanSetUpAListAndRemoveATriggerAndTheApprovalSaysWhatItDoes(t *testing.T) {
	r, s, _, c := rig(t, "triggers")
	add, _ := r.Get("phone_trigger_add")
	if !add.NeedsApproval || add.Egress == nil {
		t.Error("a trigger acts later with nobody watching: it must ask first and count as leaving something behind")
	}
	args := json.RawMessage(`{"on":"wifi","name":"Home","then":"Turn on the heating and tell me the weather","once":false}`)
	if d := add.Describe(args); !strings.Contains(d, `joins the Wi-Fi network "Home"`) || !strings.Contains(d, "Turn on the heating") {
		t.Errorf("the approval prompt: %q", d)
	}
	out, err := add.Run(t.Context(), args)
	if err != nil || !strings.Contains(out, "Set up (") {
		t.Fatalf("%q %v", out, err)
	}
	if len(s.List()) != 1 || c.pushed() < 2 { // the hello pushed an empty list, the add pushed the new one
		t.Errorf("stored %d, pushed %d", len(s.List()), c.pushed())
	}
	list, _ := r.Get("phone_trigger_list")
	if got, _ := list.Run(t.Context(), nil); !strings.Contains(got, "Home") {
		t.Errorf("list: %q", got)
	}
	id := s.List()[0].ID
	rm, _ := r.Get("phone_trigger_remove")
	if _, err := rm.Run(t.Context(), json.RawMessage(`{"id":"`+id+`"}`)); err != nil || len(s.List()) != 0 {
		t.Errorf("remove: %v", err)
	}
	if _, err := rm.Run(t.Context(), json.RawMessage(`{"id":"nope"}`)); err == nil {
		t.Error("removing a missing trigger")
	}
}

func TestATriggerIsRefusedWhenThePhoneHasNotAllowedEventsAndAllowedWhenItIsAway(t *testing.T) {
	r, s, _, _ := rig(t, "status") // connected, events not allowed
	add, _ := r.Get("phone_trigger_add")
	if _, err := add.Run(t.Context(), json.RawMessage(`{"on":"battery_low","then":"warn me"}`)); err == nil || !strings.Contains(err.Error(), "not allowed events") {
		t.Errorf("%v", err)
	}
	if len(s.List()) != 0 {
		t.Error("it was stored anyway")
	}
	r2, s2, _, _ := rig(t) // no phone linked at all: it is kept and starts when the phone connects
	add2, _ := r2.Get("phone_trigger_add")
	out, err := add2.Run(t.Context(), json.RawMessage(`{"on":"charging_start","then":"log it"}`))
	if err != nil || !strings.Contains(out, "not connected") || len(s2.List()) != 1 {
		t.Errorf("%q %v", out, err)
	}
	for _, bad := range []string{`{"on":"shake","then":"x"}`, `{"on":"battery_low","then":""}`, `{"on":"battery_low","level":99,"then":"x"}`} {
		if _, err := add2.Run(t.Context(), json.RawMessage(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
