package finance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/tool"
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

func TestImportReturnsTheSummaryAsARecord(t *testing.T) {
	r := tool.NewRegistry()
	RegisterTools(r)
	tl, _ := r.Get("finance_import")
	args, _ := json.Marshal(map[string]string{"csv": "date,description,amount,category\n2026-10-01,Rent,900,Housing\n2026-10-02,Pay,-3200,Income\n2026-10-03,Food,410.25,Groceries\n"})
	res, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Kind             string
		Count            int
		Spending, Income float64
		Categories       []struct {
			Name   string
			Amount float64
		}
	}
	if err := json.Unmarshal(res.Artifact, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Artifact)
	}
	if got.Kind != "spending" || got.Count != 3 || got.Spending != 1310.25 || got.Income != 3200 || len(got.Categories) == 0 {
		t.Fatalf("%+v", got)
	}
	if !strings.HasPrefix(res.Text, "Imported 3 transactions") {
		t.Fatalf("text %q", res.Text)
	}
}
