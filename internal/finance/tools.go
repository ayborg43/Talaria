package finance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// RegisterTools adds the finance import tool: paste a transaction CSV, get a
// spending summary with categories and a savings goal. It changes nothing.
func RegisterTools(r *tool.Registry) {
	r.Register(tool.Tool{
		Name: "finance_import",
		Description: "Import transaction CSV to create a spending summary with categories, transactions, and a savings goal. " +
			"The CSV needs date,description,amount,category columns; positive amounts are expenses and negative are income. " +
			"Values use the source currency; no currency conversion is inferred. At most 5,000 rows.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"csv":{"type":"string","description":"the transaction CSV text"}},
			"required":["csv"]}`),
		RunRich: func(_ context.Context, raw json.RawMessage) (tool.Result, error) {
			var a struct {
				CSV string `json:"csv"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return tool.Result{}, err
			}
			if strings.TrimSpace(a.CSV) == "" {
				return tool.Result{}, errors.New("paste the transaction CSV first")
			}
			s, err := Analyze(a.CSV)
			if err != nil {
				return tool.Result{}, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Imported %d transactions (%s to %s).\n", s.Count, s.Period.From, s.Period.To)
			fmt.Fprintf(&b, "Spending %.2f, income %.2f, saved %.2f.\n", s.Spending, s.Income, s.Saved)
			for i, c := range s.Categories {
				if i >= 8 {
					fmt.Fprintf(&b, "+%d more categories. ", len(s.Categories)-i)
					break
				}
				fmt.Fprintf(&b, "%s %.2f. ", c.Name, c.Amount)
			}
			cats := s.Categories
			if len(cats) > 12 { // the card shows the biggest; the text says how many there were
				cats = cats[:12]
			}
			return tool.Result{Text: strings.TrimSpace(b.String()), Artifact: tool.Artifact("spending", map[string]any{
				"count": s.Count, "from": s.Period.From, "to": s.Period.To, "spending": s.Spending, "income": s.Income, "saved": s.Saved, "categories": cats})}, nil
		},
	})
}
