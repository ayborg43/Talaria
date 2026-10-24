// Package finance imports a transaction CSV and summarizes spending, mirroring
// OpenMuse's finance engine: strict columns, plain amounts, integer cents, and
// no currency conversion. Positive amounts are expenses; negative are income.
package finance

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limits mirror the reference implementation.
const (
	MaxBytes = 500000
	MaxRows  = 5000
)

// Transaction is one imported row.
type Transaction struct {
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
}

// CategoryTotal is spending per category, largest first.
type CategoryTotal struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
}

// Summary is the result of Analyze.
type Summary struct {
	Income       float64         `json:"income"`
	Spending     float64         `json:"spending"`
	Saved        float64         `json:"saved"`
	Count        int             `json:"count"`
	Categories   []CategoryTotal `json:"categories"`
	Transactions []Transaction   `json:"transactions"`
	Period       struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"period"`
	AmountConvention string `json:"amount_convention"`
}

var amountRe = regexp.MustCompile(`^[+-]?\d+(?:\.\d{1,2})?$`)

// Analyze parses csv and returns its spending summary.
func Analyze(csv string) (Summary, error) {
	var s Summary
	if len(csv) > MaxBytes {
		return s, errors.New("import at most 500 KB of transaction CSV")
	}
	csv = strings.TrimPrefix(csv, "\ufeff")
	rows, err := splitRows(csv)
	if err != nil {
		return s, err
	}
	if len(rows) == 0 {
		return s, errors.New("CSV needs date,description,amount,category columns")
	}
	header := rows[0]
	for i, v := range header {
		header[i] = strings.ToLower(strings.TrimSpace(v))
	}
	need := []string{"date", "description", "amount", "category"}
	for _, name := range need {
		if idx(header, name) < 0 {
			return s, errors.New("CSV needs date,description,amount,category columns")
		}
	}
	for _, name := range need {
		if idx(header, name) != lastIdx(header, name) {
			return s, errors.New("CSV has a duplicate required column")
		}
	}
	body := rows[1:]
	// Drop trailing blank lines the splitter may keep as empty rows.
	kept := body[:0]
	for _, r := range body {
		blank := true
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				blank = false
				break
			}
		}
		if !blank {
			kept = append(kept, r)
		}
	}
	body = kept
	if len(body) == 0 || len(body) > MaxRows {
		return s, errors.New("import between 1 and 5,000 transactions")
	}
	type parsed struct {
		t     Transaction
		cents int64
	}
	out := make([]parsed, 0, len(body))
	get := func(r []string, name string) string {
		i := idx(header, name)
		if i < 0 || i >= len(r) {
			return ""
		}
		return strings.TrimSpace(r[i])
	}
	for i, r := range body {
		if len(r) != len(header) {
			return s, fmt.Errorf("row %d has the wrong number of columns", i+2)
		}
		date, amount := get(r, "date"), get(r, "amount")
		if !validDate(date) || !amountRe.MatchString(amount) {
			return s, fmt.Errorf("row %d needs an ISO date and a plain amount with at most two decimal places", i+2)
		}
		f, err := strconv.ParseFloat(amount, 64)
		if err != nil {
			return s, fmt.Errorf("row %d needs an ISO date and a plain amount with at most two decimal places", i+2)
		}
		cents := int64(0)
		if f >= 0 {
			cents = int64(f*100 + 0.5)
		} else {
			cents = -int64(-f*100 + 0.5)
		}
		if abs(cents) > 1e12 {
			return s, fmt.Errorf("row %d amount is out of range", i+2)
		}
		cat := get(r, "category")
		if cat == "" {
			cat = "Uncategorized"
		}
		out = append(out, parsed{
			t:     Transaction{ID: fmt.Sprintf("row-%d", i+2), Date: date, Description: get(r, "description"), Amount: float64(cents) / 100, Category: cat},
			cents: cents,
		})
	}
	var expenses, income int64
	grouped := map[string]int64{}
	for _, p := range out {
		if p.cents > 0 {
			expenses += p.cents
			grouped[p.t.Category] += p.cents
		} else {
			income += -p.cents
		}
	}
	cats := make([]CategoryTotal, 0, len(grouped))
	for name, cents := range grouped {
		cats = append(cats, CategoryTotal{Name: name, Amount: float64(cents) / 100})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Amount > cats[j].Amount })
	tx := make([]Transaction, len(out))
	dates := make([]string, len(out))
	for i, p := range out {
		tx[i], dates[i] = p.t, p.t.Date
	}
	sort.Strings(dates)
	s.Income = float64(income) / 100
	s.Spending = float64(expenses) / 100
	s.Saved = float64(income-expenses) / 100
	s.Count = len(out)
	s.Categories = cats
	s.Transactions = tx
	s.Period.From, s.Period.To = dates[0], dates[len(dates)-1]
	s.AmountConvention = "Positive expenses; negative income. Values use the source currency; no currency conversion."
	return s, nil
}

// splitRows parses CSV the way the reference does: quoted fields, embedded
// commas and newlines inside quotes, doubled quotes as escapes, \r tolerated.
func splitRows(csv string) ([][]string, error) {
	var rows [][]string
	var row []string
	var cell strings.Builder
	quoted := false
	flush := func() {
		s := cell.String()
		cell.Reset()
		row = append(row, strings.TrimSuffix(s, "\r"))
	}
	runes := []rune(csv)
	for i := 0; i <= len(runes); i++ {
		var c rune
		end := i >= len(runes)
		if !end {
			c = runes[i]
		}
		switch {
		case !end && c == '"':
			if quoted && i+1 < len(runes) && runes[i+1] == '"' {
				cell.WriteRune('"')
				i++
			} else if !quoted && cell.Len() > 0 {
				return nil, errors.New("invalid quoted CSV field")
			} else {
				quoted = !quoted
				if !quoted && i+1 < len(runes) {
					next := runes[i+1]
					if next != ',' && next != '\n' && next != '\r' && i+1 < len(runes) {
						// A closing quote must end the field; \r\n is handled
						// by the comma/newline branch via the \r trim.
						if next != 0 && !(next == '\r') {
							return nil, errors.New("invalid quoted CSV field")
						}
					}
				}
			}
		case end || (!quoted && (c == ',' || c == '\n')):
			flush()
			if end || c == '\n' {
				if end {
					// A trailing newline leaves an empty row the caller drops.
					if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
						row = nil
						break
					}
				}
				rows = append(rows, row)
				row = nil
			}
		case !end:
			cell.WriteRune(c)
		}
	}
	if quoted {
		return nil, errors.New("CSV has an unclosed quoted field")
	}
	return rows, nil
}

func validDate(d string) bool {
	if _, err := time.Parse("2006-01-02", d); err == nil {
		return true
	}
	// Accept full ISO timestamps too, but the period math uses the date part.
	if len(d) >= 10 {
		if _, err := time.Parse("2006-01-02", d[:10]); err == nil {
			if _, err := time.Parse(time.RFC3339, strings.Replace(d, " ", "T", 1)); err == nil {
				return true
			}
		}
	}
	return false
}

func idx(h []string, name string) int {
	for i, v := range h {
		if v == name {
			return i
		}
	}
	return -1
}

func lastIdx(h []string, name string) int {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] == name {
			return i
		}
	}
	return -1
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
