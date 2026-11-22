// Package usage keeps count of what the models were asked and what that cost: tokens by day, by profile and model,
// by who asked (a chat, a job, a task, a helper), by saved agent and by the router's kind of message.
//
// It is built to stay small: one in-memory table keyed by day and those five fields (a few hundred rows a month at
// the most), written to one file now and then. Prices are the person's own, per profile, so a cost is only shown
// where they said what a model costs; tokens are always shown.
package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"talaria/internal/provider"
)

// keepDays is how long a day's counts are kept.
const keepDays = 400

// flushAfter is how soon after a change the file is written: a burst of requests is one write.
const flushAfter = 20 * time.Second

// Totals is a sum of requests.
type Totals struct {
	Calls     int64 `json:"calls"`
	In        int64 `json:"in"`
	Out       int64 `json:"out"`
	Estimated int64 `json:"estimated,omitempty"` // calls whose counts were guessed from the text
}

func (t *Totals) add(o Totals) {
	t.Calls += o.Calls
	t.In += o.In
	t.Out += o.Out
	t.Estimated += o.Estimated
}

// Price is what a model costs, in US dollars for a million tokens.
type Price struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

func (p Price) cost(t Totals) float64 {
	return float64(t.In)*p.In/1e6 + float64(t.Out)*p.Out/1e6
}

type row struct {
	Profile string `json:"p"`
	Model   string `json:"m,omitempty"`
	Source  string `json:"s,omitempty"`
	Agent   string `json:"a,omitempty"`
	Kind    string `json:"k,omitempty"`
	Totals
}

func (r row) key() string {
	return strings.Join([]string{r.Profile, r.Model, r.Source, r.Agent, r.Kind}, "\x1f")
}

// Ledger is the table.
type Ledger struct {
	path string
	now  func() time.Time
	zone *time.Location

	mu    sync.Mutex
	days  map[string]map[string]*row // "2006-01-02" -> key -> row
	timer *time.Timer
	dirty bool
}

// Open reads the ledger at path (empty when there is none yet). zone decides where one day ends; nil is UTC.
func Open(path string, zone *time.Location) *Ledger {
	if zone == nil {
		zone = time.UTC
	}
	l := &Ledger{path: path, now: time.Now, zone: zone, days: map[string]map[string]*row{}}
	var file struct {
		Days map[string][]row `json:"days"`
	}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &file) == nil {
		for d, rows := range file.Days {
			m := map[string]*row{}
			for i := range rows {
				r := rows[i]
				m[r.key()] = &r
			}
			l.days[d] = m
		}
	}
	return l
}

// Add counts one answered request.
func (l *Ledger) Add(s provider.Spent) {
	if l == nil {
		return
	}
	r := row{Profile: s.Profile, Model: s.Model, Source: orDefault(s.Label.Source, "chat"), Agent: s.Label.Agent, Kind: s.Label.Kind}
	r.Calls, r.In, r.Out = 1, int64(s.Usage.In), int64(s.Usage.Out)
	if s.Usage.Estimated {
		r.Estimated = 1
	}
	day := l.now().In(l.zone).Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.days[day]
	if m == nil {
		m = map[string]*row{}
		l.days[day] = m
	}
	if cur := m[r.key()]; cur != nil {
		cur.add(r.Totals)
	} else {
		m[r.key()] = &r
	}
	l.dirty = true
	if l.timer == nil {
		l.timer = time.AfterFunc(flushAfter, func() { l.Flush() })
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Flush writes the ledger now. It is called on its own shortly after a change, and once more when Talaria stops.
func (l *Ledger) Flush() error {
	if l == nil || l.path == "" {
		return nil
	}
	l.mu.Lock()
	l.timer = nil
	if !l.dirty {
		l.mu.Unlock()
		return nil
	}
	l.dirty = false
	cutoff := l.now().In(l.zone).AddDate(0, 0, -keepDays).Format("2006-01-02")
	out := struct {
		Days map[string][]row `json:"days"`
	}{Days: map[string][]row{}}
	for d, m := range l.days {
		if d < cutoff {
			delete(l.days, d)
			continue
		}
		for _, r := range m {
			out.Days[d] = append(out.Days[d], *r)
		}
	}
	l.mu.Unlock()
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// Line is one row of a report: a name, what it used and, where a price is known, what that cost.
type Line struct {
	Name   string  `json:"name"`
	Totals Totals  `json:"totals"`
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"` // every call in it was on a model with a price
}

// Day is one day's total, for a small chart.
type Day struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// Report is the ledger over a stretch of time.
type Report struct {
	Since    string   `json:"since"`
	Days     int      `json:"days"`
	Total    Line     `json:"total"`
	ByModel  []Line   `json:"by_model"`
	ByAgent  []Line   `json:"by_agent"`
	BySource []Line   `json:"by_source"`
	ByKind   []Line   `json:"by_kind"`
	PerDay   []Day    `json:"per_day"`
	Unpriced []string `json:"unpriced,omitempty"` // profiles that were used but have no price, so their cost is not in the total
}

// Prices says what a profile's model costs; ok is false when the person has not said.
type Prices func(profile string) (Price, bool)

type acc struct {
	t      Totals
	cost   float64
	priced bool
	any    bool
}

func (a *acc) add(t Totals, p Price, ok bool) {
	first := !a.any
	a.any = true
	a.t.add(t)
	if ok {
		a.cost += p.cost(t)
	}
	if first {
		a.priced = ok
	} else {
		a.priced = a.priced && ok
	}
}

func lines(m map[string]*acc) []Line {
	out := make([]Line, 0, len(m))
	for k, a := range m {
		out = append(out, Line{Name: k, Totals: a.t, Cost: round(a.cost), Priced: a.priced})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		ti, tj := out[i].Totals.In+out[i].Totals.Out, out[j].Totals.In+out[j].Totals.Out
		if ti != tj {
			return ti > tj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func round(f float64) float64 { return math.Round(f*10000) / 10000 }

// Report sums the last days days (1 is today only), pricing each row by its profile.
func (l *Ledger) Report(days int, prices Prices) Report {
	if days < 1 {
		days = 1
	}
	if days > keepDays {
		days = keepDays
	}
	now := l.now().In(l.zone)
	from := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rep := Report{Since: from, Days: days, PerDay: []Day{}}
	byModel, byAgent, bySource, byKind := map[string]*acc{}, map[string]*acc{}, map[string]*acc{}, map[string]*acc{}
	var total acc
	perDay := map[string]*acc{}
	unpriced := map[string]bool{}
	get := func(m map[string]*acc, k string) *acc {
		if m[k] == nil {
			m[k] = &acc{}
		}
		return m[k]
	}
	l.mu.Lock()
	for d, m := range l.days {
		if d < from {
			continue
		}
		for _, r := range m {
			var p Price
			ok := false
			if prices != nil {
				p, ok = prices(r.Profile)
			}
			if !ok {
				unpriced[r.Profile] = true
			}
			model := r.Profile
			if r.Model != "" {
				model = r.Profile + " (" + r.Model + ")"
			}
			agent := r.Agent
			if agent == "" {
				agent = "main agent"
			}
			kind := r.Kind
			if kind == "" {
				kind = "other"
			}
			get(byModel, model).add(r.Totals, p, ok)
			get(byAgent, agent).add(r.Totals, p, ok)
			get(bySource, r.Source).add(r.Totals, p, ok)
			get(byKind, kind).add(r.Totals, p, ok)
			get(perDay, d).add(r.Totals, p, ok)
			total.add(r.Totals, p, ok)
		}
	}
	l.mu.Unlock()
	rep.Total = Line{Name: "total", Totals: total.t, Cost: round(total.cost), Priced: total.priced || !total.any}
	rep.ByModel, rep.ByAgent, rep.BySource, rep.ByKind = lines(byModel), lines(byAgent), lines(bySource), lines(byKind)
	for d := now.AddDate(0, 0, -(days - 1)); !d.After(now); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		day := Day{Date: k}
		if a := perDay[k]; a != nil {
			day.Tokens, day.Cost = a.t.In+a.t.Out, round(a.cost)
		}
		rep.PerDay = append(rep.PerDay, day)
	}
	for p := range unpriced {
		rep.Unpriced = append(rep.Unpriced, p)
	}
	sort.Strings(rep.Unpriced)
	return rep
}

// Money writes an amount of dollars the way a person reads it.
func Money(f float64) string {
	switch {
	case f == 0:
		return "$0"
	case f < 0.01:
		return "under $0.01"
	case f < 10:
		return fmt.Sprintf("$%.2f", f)
	}
	return fmt.Sprintf("$%.0f", f)
}

// Tokens writes a token count briefly: 1,200,000 is "1.2M".
func Tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// Summary is the report in a few plain sentences: what was used, what it cost, and where most of it went.
func (r Report) Summary(period string) string {
	if r.Total.Totals.Calls == 0 {
		return "Nothing was asked of a model " + period + "."
	}
	var b strings.Builder
	t := r.Total.Totals
	fmt.Fprintf(&b, "%s: %s, %s tokens in and %s out", capitalize(period), requests(t.Calls), Tokens(t.In), Tokens(t.Out))
	if r.Total.Cost > 0 || r.Total.Priced {
		if m := Money(r.Total.Cost); strings.HasPrefix(m, "under") {
			fmt.Fprintf(&b, ", %s", m)
		} else {
			fmt.Fprintf(&b, ", about %s", m)
		}
		if !r.Total.Priced {
			b.WriteString(" for the models with a price set")
		}
	}
	b.WriteString(".")
	top := func(ls []Line, what string) {
		if len(ls) == 0 || len(ls) == 1 && ls[0].Name == "other" {
			return
		}
		l := ls[0]
		var share float64
		if r.Total.Cost > 0 && l.Cost > 0 {
			share = l.Cost / r.Total.Cost
		} else if tot := t.In + t.Out; tot > 0 {
			share = float64(l.Totals.In+l.Totals.Out) / float64(tot)
		}
		if share >= 0.5 && l.Name != "other" && l.Name != "main agent" {
			fmt.Fprintf(&b, " Mostly %s %s (%d%%).", what, l.Name, int(share*100+0.5))
		}
	}
	top(r.ByModel, "the model")
	top(r.ByKind, "on")
	top(r.ByAgent, "the agent")
	if t.Estimated > 0 {
		fmt.Fprintf(&b, " %d of them had no count from the service, so those are estimates.", t.Estimated)
	}
	if len(r.Unpriced) > 0 {
		fmt.Fprintf(&b, " No price is set for %s, so what it cost is not counted.", strings.Join(r.Unpriced, ", "))
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func requests(n int64) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}
