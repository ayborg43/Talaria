package chat

import (
	"testing"
)

func TestChoiceDataRoundTrip(t *testing.T) {
	d := ChoiceData("abc123", 2)
	panel, idx, ok := ParseChoice(d)
	if !ok || panel != "abc123" || idx != 2 {
		t.Fatalf("%q -> %q %d %v", d, panel, idx, ok)
	}
	for _, bad := range []string{"", "ch:", "ch::1", "ap:x:y", "ch:abc:notanumber", "ch:abc:1:2"} {
		if _, _, ok := ParseChoice(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestParseChoicePanel(t *testing.T) {
	id := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	text := "question " + id + ":\nWhen? (pick one, 2 options \u2014 reply with the number or name):\n1. Morning \u2605\n   - opens at 9\n   - aquarium: https://example.org/visits\n2. Afternoon\n   - opens at 1"
	gotID, labels, ok := ParseChoicePanel(text)
	if !ok || gotID != id || len(labels) != 2 || labels[0] != "Morning" || labels[1] != "Afternoon" {
		t.Fatalf("%q %q %v", gotID, labels, ok)
	}
	if _, _, ok := ParseChoicePanel("just chatting"); ok {
		t.Fatal("plain text parsed as a panel")
	}
	over := "question " + id + ":\nPick? (pick one, 13 options \u2014 reply with the number or name):\n"
	for i := 1; i <= 13; i++ {
		over += "1. opt\n"
	}
	// More options than a panel may hold is not a panel.
	if _, _, ok := ParseChoicePanel(over); ok {
		t.Fatal("13 options parsed")
	}
}
