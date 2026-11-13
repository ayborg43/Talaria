package evals

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/provider"
)

type says func(prompt string) (string, error)

func (f says) Chat(_ context.Context, r provider.Request) (provider.Message, error) {
	s, err := f(r.Messages[len(r.Messages)-1].Content)
	return provider.Message{Content: s}, err
}

func TestAnAnswerIsCheckedAgainstWhatTheCaseSays(t *testing.T) {
	c := Case{Expect: []string{"Lagos"}, Any: []string{"1914", "nineteen fourteen"}, Avoid: []string{"I don't know"}}
	for reply, pass := range map[string]bool{
		"The capital was Lagos until 1914.": true, "lagos, in nineteen fourteen": true,
		"It was Lagos": false, "Abuja since 1914": false, "Lagos 1914 but I don't know": false,
	} {
		if got, missed := Check(c, reply); got != pass {
			t.Errorf("%q: %v %v", reply, got, missed)
		}
	}
	if pass, missed := Check(c, "Abuja"); pass || len(missed) != 2 {
		t.Errorf("what was missed should be named: %v", missed)
	}
}

func TestACaseNeedsAQuestionAndTheSetHasALimit(t *testing.T) {
	s := &Store{}
	if _, err := s.Add(Case{Prompt: "  "}); err == nil {
		t.Error("an empty question was kept")
	}
	if _, err := s.Add(Case{Prompt: strings.Repeat("x", MaxPrompt+1)}); err == nil {
		t.Error("a huge question was kept")
	}
	c, err := s.Add(Case{Prompt: " What is 2+2? ", Expect: []string{" 4 ", "4", ""}})
	if err != nil || c.ID == "" || c.Prompt != "What is 2+2?" || len(c.Expect) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	for range MaxCases - 1 {
		s.Add(Case{Prompt: "q"})
	}
	if _, err := s.Add(Case{Prompt: "one too many"}); err == nil {
		t.Error("the limit was not applied")
	}
	if !s.Remove(c.ID) || s.Remove(c.ID) {
		t.Error("remove")
	}
}

func TestARunScoresEachCaseAndSaysWhatIsNotCheckable(t *testing.T) {
	cases := []Case{
		{ID: "a", Prompt: "capital of France", Expect: []string{"Paris"}},
		{ID: "b", Prompt: "2+2", Expect: []string{"4"}},
		{ID: "c", Prompt: "tell me a joke"}, // nothing to check
		{ID: "d", Prompt: "broken", Expect: []string{"x"}},
	}
	model := says(func(p string) (string, error) {
		switch p {
		case "capital of France":
			return "Paris", nil
		case "2+2":
			return "five", nil
		case "broken":
			return "", &provider.StatusError{Status: 503, Err: errors.New("overloaded")}
		}
		return "knock knock", nil
	})
	rs := Execute(t.Context(), cases, model, "m", nil, "")
	run := rs.Summary("lite", "m", "")
	if run.Passed != 1 || run.Counted != 3 || run.Total != 4 {
		t.Fatalf("%+v", run)
	}
	if !rs[2].Unchecked || rs[1].Pass || !strings.Contains(rs[3].Error, "down or overloaded") {
		t.Errorf("%+v", rs)
	}
	if !strings.Contains(run.Line(), "1 of 3") {
		t.Errorf("the summary line: %q", run.Line())
	}
}

func TestARubricIsJudgedBySecondModelWhenThereIsOne(t *testing.T) {
	cases := []Case{{ID: "a", Prompt: "how many days in a year", Rubric: "says 365 and mentions leap years"}}
	answer := says(func(string) (string, error) { return "365, 366 in a leap year", nil })
	yes := says(func(string) (string, error) { return "YES: it does", nil })
	no := says(func(string) (string, error) { return "NO: no mention of leap years", nil })
	if run := Execute(t.Context(), cases, answer, "m", yes, "j").Summary("p", "m", "j"); run.Passed != 1 {
		t.Errorf("judge says yes: %+v", run)
	}
	rs := Execute(t.Context(), cases, answer, "m", no, "j")
	if rs.Summary("p", "m", "j").Passed != 0 || !strings.Contains(strings.Join(rs[0].Missed, " "), "judged not to meet") {
		t.Errorf("judge says no: %+v", rs)
	}
	// with no judge, a rubric-only case is shown but not counted
	if rs := Execute(t.Context(), cases, answer, "m", nil, ""); !rs[0].Unchecked {
		t.Errorf("no judge: %+v", rs[0])
	}
}

func TestRunsSurviveARestartAndOnlyTheNewestPerProfileAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evals.json")
	s := &Store{Path: path}
	s.Add(Case{Prompt: "q", Expect: []string{"a"}})
	for range keepRuns + 3 {
		s.Keep(Run2{{Pass: true}}.Summary("lite", "m", ""))
	}
	s.Keep(Run2{{Pass: true}}.Summary("main", "m", ""))
	again := &Store{Path: path}
	if len(again.Cases()) != 1 {
		t.Error("the cases did not survive")
	}
	n := 0
	for _, r := range again.Runs() {
		if r.Profile == "lite" {
			n++
		}
	}
	if n != keepRuns {
		t.Errorf("kept %d runs for lite", n)
	}
	if r, ok := again.Latest("main"); !ok || r.Profile != "main" {
		t.Error("latest")
	}
	if _, ok := again.Latest("ghost"); ok {
		t.Error("a run for a profile that was never run")
	}
}
