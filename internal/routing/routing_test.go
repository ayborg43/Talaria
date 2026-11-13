package routing

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKindOfIsTheRuleOfThumb(t *testing.T) {
	for text, want := range map[string]Kind{
		"fix this ```go\nfunc main(){}```":                Code,
		"why does my code give a traceback":               Code,
		"prove that the square root of two is irrational": Hard,
		"hi":                            Simple,
		"what is the capital of France": Simple,
		strings.Repeat("a long and plain request ", 8): General,
	} {
		if got := KindOf(text); got != want {
			t.Errorf("%q: %s, want %s", text[:min(len(text), 30)], got, want)
		}
	}
}

func TestParseFindsTheFirstKindInAnAnswer(t *testing.T) {
	for in, want := range map[string]Kind{"code": Code, "Code.": Code, "I think this is simple, not hard": Simple, "HARD": Hard, "writing": Writing} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("%q: %s %v", in, got, ok)
		}
	}
	if _, ok := Parse("no idea"); ok {
		t.Error("an answer with no kind was taken for one")
	}
}

func TestCorrectionsAreRecognisedAtTheStartOfAMessage(t *testing.T) {
	for _, s := range []string{"No, that is not right", "wrong.", "that's wrong", "Not what I asked", "try again please", "nope"} {
		if !IsCorrection(s) {
			t.Errorf("%q was not taken for a correction", s)
		}
	}
	for _, s := range []string{"Now explain the second part", "I know that", "nothing else", "Notes from the meeting"} {
		if IsCorrection(s) {
			t.Errorf("%q was taken for a correction", s)
		}
	}
}

func TestWithNoRecordTheUsualChoiceStands(t *testing.T) {
	s := &Store{}
	if p, why := s.Prefer(Code, []string{"a", "b"}, "a"); p != "a" || why != "" {
		t.Errorf("%s %q", p, why)
	}
	for range 3 { // a little evidence is not enough
		s.Record(Code, "b", Up)
	}
	if p, _ := s.Prefer(Code, []string{"a", "b"}, "a"); p != "a" {
		t.Error("three thumbs moved the choice")
	}
}

func TestAProfileThatClearlyDoesBetterIsPreferredOnThatKindOnly(t *testing.T) {
	s := &Store{}
	for range 7 {
		s.Record(Code, "strong", Up)
		s.Record(Code, "usual", Down)
	}
	p, why := s.Prefer(Code, []string{"usual", "strong"}, "usual")
	if p != "strong" || !strings.Contains(why, "learned") {
		t.Fatalf("%s %q", p, why)
	}
	if p, _ := s.Prefer(Simple, []string{"usual", "strong"}, "usual"); p != "usual" {
		t.Error("what was learned about code changed simple messages")
	}
}

func TestRetriesAndCorrectionsCountAgainstAProfile(t *testing.T) {
	s := &Store{}
	for range 5 {
		s.Record(General, "cheap", Retry)
		s.Record(General, "cheap", Correction)
		s.Record(General, "good", Continue)
		s.Record(General, "good", Continue)
		s.Record(General, "good", Continue)
	}
	rows := s.Rows()
	if rows[0].Profile != "good" || rows[0].Score <= rows[1].Score || rows[1].Retry != 5 {
		t.Errorf("%+v", rows)
	}
	if p, _ := s.Prefer(General, []string{"cheap", "good"}, "cheap"); p != "good" {
		t.Errorf("the usual choice was kept despite retries: %s", p)
	}
}

func TestWhatIsLearnedSurvivesARestartAndCanBeForgotten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.json")
	s := &Store{Path: path}
	s.Record(Code, "x", Up)
	again := &Store{Path: path}
	if rows := again.Rows(); len(rows) != 1 || rows[0].Up != 1 {
		t.Fatalf("%+v", rows)
	}
	if err := again.Reset(); err != nil || len(again.Rows()) != 0 {
		t.Fatalf("reset: %v %v", err, again.Rows())
	}
	if len((&Store{Path: path}).Rows()) != 0 {
		t.Error("the file came back")
	}
	(*Store)(nil).Record(Code, "x", Up) // a missing store is a no-op, not a crash
}

func TestExploreOnlyTriesProfilesWithLittleEvidence(t *testing.T) {
	s := &Store{}
	if got := s.Explore(Simple, []string{"a", "b"}, "a", 1); got != "b" {
		t.Errorf("%q", got)
	}
	for range 6 {
		s.Record(Simple, "b", Up)
	}
	if got := s.Explore(Simple, []string{"a", "b"}, "a", 1); got != "" {
		t.Errorf("explored a profile that has plenty of evidence: %q", got)
	}
	if got := s.Explore(Simple, []string{"a", "b"}, "a", 0); got != "" {
		t.Error("explored with no chance")
	}
}
