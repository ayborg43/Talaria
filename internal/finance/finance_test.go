package finance

import (
	"strings"
	"testing"
)

func TestAnalyzeSpendingTotals(t *testing.T) {
	s, err := Analyze("date,description,amount,category\n2026-10-01,Coffee,3.50,Food\n2026-10-02,Paycheck,-2000.00,Income\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.Spending != 3.50 || s.Income != 2000 || s.Saved != 1996.50 || s.Count != 2 {
		t.Fatalf("%+v", s)
	}
	if len(s.Categories) != 1 || s.Categories[0].Name != "Food" {
		t.Fatalf("%+v", s.Categories)
	}
	if s.Period.From != "2026-10-01" || s.Period.To != "2026-10-02" {
		t.Fatalf("%+v", s.Period)
	}
}

func TestAnalyzeStrictness(t *testing.T) {
	for csv, want := range map[string]string{
		"date,description,amount\n2026-10-01,x,1.00":              "category",
		"date,description,amount,category,date\n2026-10-01,x,1,c": "duplicate",
		"date,description,amount,category\n2026-10-01,x,1.005,c":  "plain amount",
		"date,description,amount,category\n01/10/2026,x,1.00,c":   "ISO date",
		"date,description,amount,category\n2026-10-01,\"x,c":      "unclosed",
		"date,description,amount,category\n2026-10-01,x":          "number of columns",
	} {
		if _, err := Analyze(csv + "\n"); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want[:4])) {
			t.Fatalf("%q: got %v, want %q", csv, err, want)
		}
	}
	if _, err := Analyze("date,description,amount,category\n"); err == nil {
		t.Fatal("no rows must fail")
	}
}
