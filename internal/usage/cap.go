package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"talaria/internal/provider"
)

// ErrPaused is what background work gets once a spending limit is reached. Errors from Cap.Check wrap it.
var ErrPaused = errors.New("spending limit reached")

// warnShare is the part of a limit at which the owner is told it is getting close.
const warnShare = 0.8

// Limits are the most the models may cost, in dollars; 0 is no limit.
type Limits struct {
	Day   float64
	Month float64
}

// Cap pauses background work once what the models cost today, or this calendar month, reaches a limit. The owner's
// own chats are never stopped by it: they are what the money is for, and the person is there to see what it costs.
//
// It reads the ledger, so it only knows the cost of profiles the owner gave a price; a profile with no price counts as
// free. It keeps no state of its own but which notices it has sent, so a restart may repeat one.
type Cap struct {
	Ledger *Ledger
	Prices Prices
	// Limits is read at every check, so a limit changed in Settings applies at once.
	Limits func() Limits
	// Notify tells the owner something; nil only logs nothing.
	Notify func(title, body string)

	mu   sync.Mutex
	sent map[string]bool // "day:2026-11-25:80" and the like: each notice once a period
}

// Status is where spending stands against the limits.
type Status struct {
	Day        float64 `json:"day"`
	DayLimit   float64 `json:"day_limit"`
	Month      float64 `json:"month"`
	MonthLimit float64 `json:"month_limit"`
	// Paused is true when background work is held back. Reason says which limit, in a sentence; Until is when it lifts.
	Paused bool      `json:"paused"`
	Reason string    `json:"reason,omitempty"`
	Until  time.Time `json:"until,omitzero"`
	// Unpriced are profiles used this month that have no price, so what they cost is not counted.
	Unpriced []string `json:"unpriced,omitempty"`
}

// Status works out what has been spent today and this month, and whether background work is paused.
func (c *Cap) Status() Status {
	var st Status
	if c == nil || c.Ledger == nil {
		return st
	}
	if c.Limits != nil {
		l := c.Limits()
		st.DayLimit, st.MonthLimit = l.Day, l.Month
	}
	now := c.Ledger.now().In(c.Ledger.zone)
	st.Day = c.Ledger.Report(1, c.Prices).Total.Cost
	month := c.Ledger.Report(now.Day(), c.Prices)
	st.Month, st.Unpriced = month.Total.Cost, month.Unpriced
	switch {
	case st.MonthLimit > 0 && st.Month >= st.MonthLimit:
		st.Paused = true
		st.Until = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
		st.Reason = fmt.Sprintf("this month's spending limit of %s is reached (%s so far)", Money(st.MonthLimit), Money(st.Month))
	case st.DayLimit > 0 && st.Day >= st.DayLimit:
		st.Paused = true
		st.Until = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		st.Reason = fmt.Sprintf("today's spending limit of %s is reached (%s so far)", Money(st.DayLimit), Money(st.Day))
	}
	return st
}

// Paused returns an error wrapping ErrPaused when background work may not run now, and nil when it may.
func (c *Cap) Paused() error {
	st := c.Status()
	if !st.Paused {
		return nil
	}
	return fmt.Errorf("%w: %s, so background work is paused until %s. Your own chats still work. Raise or remove the limit under Settings > AI models", ErrPaused, st.Reason, st.Until.Format("Jan 2 15:04 MST"))
}

// Check is asked before each request to a model. It refuses background work while a limit is reached, and lets
// everything else through.
func (c *Cap) Check(ctx context.Context) error {
	if c == nil || !provider.LabelOf(ctx).Background {
		return nil
	}
	return c.Paused()
}

// Observe is told after each answered request. It sends the 80% and the limit-reached notices, once each a period.
func (c *Cap) Observe() {
	if c == nil || c.Notify == nil || c.Limits == nil {
		return
	}
	l := c.Limits()
	if l.Day <= 0 && l.Month <= 0 {
		return
	}
	st := c.Status()
	now := c.Ledger.now().In(c.Ledger.zone)
	check := func(period, key string, spent, limit float64, until time.Time) {
		if limit <= 0 {
			return
		}
		switch {
		case spent >= limit:
			if c.once(key + ":100") {
				c.Notify("Spending limit reached", fmt.Sprintf("%s the models have cost %s, the limit you set. Scheduled jobs, tasks, goals and watches are paused until %s; your own chats still work.", period, Money(spent), until.Format("Jan 2 15:04 MST")))
			}
		case spent >= limit*warnShare:
			if c.once(key + ":80") {
				c.Notify("Spending is close to the limit", fmt.Sprintf("%s the models have cost %s of the %s you set. Background work pauses at the limit.", period, Money(spent), Money(limit)))
			}
		}
	}
	check("This month", "month:"+now.Format("2006-01"), st.Month, l.Month, time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location()))
	if l.Month > 0 && st.Month >= l.Month {
		return // the month is what holds work back: a notice about today would only repeat it
	}
	check("Today", "day:"+now.Format("2006-01-02"), st.Day, l.Day, time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location()))
}

func (c *Cap) once(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent[key] {
		return false
	}
	if c.sent == nil {
		c.sent = map[string]bool{}
	}
	c.sent[key] = true
	return true
}

// Line is the status in a sentence or two, for the command line and the agent; "" when no limit is set.
func (s Status) Line() string {
	if s.DayLimit <= 0 && s.MonthLimit <= 0 {
		return ""
	}
	var parts []string
	if s.DayLimit > 0 {
		parts = append(parts, fmt.Sprintf("today %s of %s", Money(s.Day), Money(s.DayLimit)))
	}
	if s.MonthLimit > 0 {
		parts = append(parts, fmt.Sprintf("this month %s of %s", Money(s.Month), Money(s.MonthLimit)))
	}
	line := "Spending limit: " + strings.Join(parts, ", ") + "."
	if s.Paused {
		line += " Background work is paused: " + s.Reason + ", until " + s.Until.Format("Jan 2 15:04 MST") + "."
	}
	return line
}
