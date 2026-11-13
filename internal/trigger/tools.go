package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"talaria/internal/phone"
	"talaria/internal/tool"
)

// Specs turns the stored triggers into what the phone is told to watch for.
func Specs(s *Store) []phone.Spec {
	var out []phone.Spec
	for _, t := range s.List() {
		out = append(out, phone.Spec{ID: t.ID, Kind: t.Kind, Match: t.Match, Level: t.level2()})
	}
	return out
}

func (t Trigger) level2() int {
	if t.Kind == BatteryLow {
		return t.level()
	}
	return 0
}

// RegisterTools gives the agent three tools for "when this happens on my phone, do that". Setting one up asks first, since
// it acts later with nobody watching, and the phone must have allowed events too.
func RegisterTools(r *tool.Registry, s *Store, hub *phone.Hub) {
	r.Register(tool.Tool{
		Name: "phone_trigger_add",
		Description: "Set something up to happen when an event occurs on the user's phone: its battery drops to a level (on=battery_low, level 3-50), the charger is plugged in or " +
			"unplugged (charging_start, charging_stop), it joins a Wi-Fi network (wifi, name=the network, left out for any) or connects to a Bluetooth device (bluetooth, name=the device). " +
			"'then' is what you will be asked to do each time it happens, written as an instruction to yourself that stands alone, e.g. 'Tell me my first meeting tomorrow and whether I need to charge'. " +
			"Use once=true for a single time. The user turns events on separately on their phone, and a job started this way runs with nobody to approve anything, so it can read and tell but not send, post or delete.",
		Schema: json.RawMessage(`{"type":"object","properties":{"on":{"type":"string","enum":["battery_low","charging_start","charging_stop","wifi","bluetooth"]},` +
			`"level":{"type":"integer","minimum":3,"maximum":50},"name":{"type":"string"},"then":{"type":"string"},"once":{"type":"boolean"}},"required":["on","then"]}`),
		NeedsApproval: true,
		Egress:        tool.Always, // it leaves something behind that acts later
		Describe: func(args json.RawMessage) string {
			t, err := parseAdd(args)
			if err != nil {
				return "Set up something to happen when an event occurs on your phone"
			}
			return "When " + t.Describe() + ": " + t.Prompt
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			t, err := parseAdd(args)
			if err != nil {
				return "", err
			}
			st := hub.Status()
			if st.Connected && !slices.Contains(st.Caps, phone.OpTriggers) {
				return "", fmt.Errorf("the user has not allowed events on their phone. They can turn it on in Talaria's Settings, under Your phone")
			}
			added, err := s.Add(t)
			if err != nil {
				return "", err
			}
			hub.PushTriggers()
			note := ""
			if !st.Connected {
				note = " The phone is not connected right now: it will start watching when it connects."
			}
			return fmt.Sprintf("Set up (%s): when %s, you will be asked: %s.%s", added.ID, added.Describe(), added.Prompt, note), nil
		},
	})
	r.Register(tool.Tool{
		Name:        "phone_trigger_list",
		Description: "List what is set to happen when something occurs on the user's phone.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Parallel:    true,
		Run: func(context.Context, json.RawMessage) (string, error) {
			list := s.List()
			if len(list) == 0 {
				return "Nothing is set up.", nil
			}
			var b strings.Builder
			for _, t := range list {
				fmt.Fprintf(&b, "- %s: when %s, %s%s\n", t.ID, t.Describe(), t.Prompt, map[bool]string{true: " (once)", false: ""}[t.Once])
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
	r.Register(tool.Tool{
		Name:        "phone_trigger_remove",
		Description: "Stop something that was set up to happen when an event occurs on the user's phone, by its id from phone_trigger_list.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ ID string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if !s.Remove(strings.TrimSpace(a.ID)) {
				return "", fmt.Errorf("there is no trigger %q", a.ID)
			}
			hub.PushTriggers()
			return "Removed.", nil
		},
	})
}

func parseAdd(args json.RawMessage) (Trigger, error) {
	var a struct {
		On    string `json:"on"`
		Level int    `json:"level"`
		Name  string `json:"name"`
		Then  string `json:"then"`
		Once  bool   `json:"once"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Trigger{}, fmt.Errorf("the arguments were not understood")
	}
	return Check(Trigger{Kind: a.On, Level: a.Level, Match: a.Name, Prompt: a.Then, Once: a.Once})
}
