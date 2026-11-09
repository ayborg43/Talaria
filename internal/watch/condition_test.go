package watch

import (
	"testing"
	"time"
)

func TestFindUSD(t *testing.T) {
	cases := []struct {
		text, near string
		want       float64
		ok         bool
	}{
		{"Now only $1,299.00 today", "", 1299, true},
		{"US$ 49 shipped", "", 49, true},
		{"costs 12.50 USD", "", 12.5, true},
		{"List: $80.00\nSale price: $59.99", "Sale price", 59.99, true},
		{"no money here, 42 items", "", 0, false},
		{"$5 here", "missing anchor", 0, false},
	}
	for _, c := range cases {
		got, ok := FindUSD(c.text, c.near)
		if ok != c.ok || got != c.want {
			t.Errorf("FindUSD(%q, %q) = %v, %v; want %v, %v", c.text, c.near, got, ok, c.want, c.ok)
		}
	}
}

func TestConditionValidate(t *testing.T) {
	for _, c := range []Condition{{Kind: "price_below"}, {Kind: "text_appears"}, {Kind: "nonsense", USD: 3}} {
		if err := c.validate(); err == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
	ok := Condition{Kind: PriceBelow, USD: 20, Text: "dropped"}
	if err := ok.validate(); err != nil || ok.Text != "" {
		t.Errorf("validate = %v, text %q", err, ok.Text)
	}
}

// A condition watch speaks once when the condition starts to hold, stays
// quiet while it holds, and speaks again only after it stopped holding.
func TestObserveConditionSpeaksOnceUntilItResets(t *testing.T) {
	w := Watch{MaxTriggers: 10, When: &Condition{Kind: PriceBelow, USD: 50}}
	now := time.Unix(1000, 0)
	step := func(page string) Outcome {
		now = now.Add(time.Minute)
		out, _ := w.Observe(now, page, nil)
		return out
	}
	if out := step("Price: $80.00"); out != Quiet {
		t.Fatalf("above the threshold = %v, want Quiet", out)
	}
	if out := step("Price: $45.00"); out != ConditionMet || w.LastFound != "$45" {
		t.Fatalf("crossing = %v (found %q), want ConditionMet", out, w.LastFound)
	}
	if out := step("Price: $44.00"); out != Quiet {
		t.Fatalf("still below = %v, want Quiet: one alert, not one per poll", out)
	}
	if out := step("Price: $70.00"); out != Quiet || w.Met {
		t.Fatalf("back above = %v met %v", out, w.Met)
	}
	if out := step("Price: $49.99"); out != ConditionMet {
		t.Fatalf("crossing again = %v, want ConditionMet", out)
	}
}

// A page that stops showing a price is a failing poll: it backs off into a
// pause rather than staying silent for ever.
func TestObserveConditionWithoutPricePauses(t *testing.T) {
	w := Watch{MaxTriggers: 10, Status: Active, When: &Condition{Kind: PriceAbove, USD: 5}}
	var out Outcome
	for i := 0; i < MaxErrors; i++ {
		out, _ = w.Observe(time.Unix(int64(i), 0), "sold out", nil)
	}
	if out != PausedByErrors || w.Status != Paused || w.LastError == "" {
		t.Fatalf("out %v status %s err %q", out, w.Status, w.LastError)
	}
}

func TestObserveTextCondition(t *testing.T) {
	w := Watch{MaxTriggers: 10, When: &Condition{Kind: TextGone, Text: "Sold Out"}}
	if out, _ := w.Observe(time.Unix(1, 0), "Tickets: SOLD OUT", nil); out != Quiet {
		t.Fatalf("still sold out = %v", out)
	}
	if out, _ := w.Observe(time.Unix(2, 0), "Tickets: available", nil); out != ConditionMet {
		t.Fatalf("available = %v, want ConditionMet", out)
	}
}
