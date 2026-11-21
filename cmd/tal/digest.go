package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"talaria/internal/cron"
	"talaria/internal/digest"
	"talaria/internal/goal"
	"talaria/internal/guard"
	"talaria/internal/kanban"
	"talaria/internal/runlog"
	"talaria/internal/usage"
	"talaria/internal/watch"
	"talaria/internal/web"
)

// digestName is how the scheduled digest's job is recognised among the others. It is a scheduled job of the web chat
// that asks no model: prepare writes the digest itself from what Talaria keeps and hands it back as the result.
const digestName = "While you were away"

// digestSources is where a digest is written from. Any field may be nil: that part is then left out.
type digestSources struct {
	home    string
	zone    *time.Location
	runs    *runlog.Log
	watches *watch.Store
	goals   *goal.Store
	cards   *kanban.Store
	ledger  func() *usage.Ledger
	prices  usage.Prices
	// waiting says how many questions wait for an answer, and how many finished tasks have not been looked at.
	waiting func() (approvals, toReview int)
}

// build writes the digest for the window.
func (d digestSources) build(since, until time.Time) digest.Digest {
	in := digest.Input{Since: since, Until: until, Zone: d.zone}
	if _, err := os.Stat(filepath.Join(d.home, "audit.log")); err == nil {
		(&guard.Log{Path: filepath.Join(d.home, "audit.log")}).Since(since, func(r guard.Record) {
			in.Calls = append(in.Calls, digest.Call{Tool: r.Tool, Allowed: r.Allowed, Failed: r.Status == "error", Origin: r.Origin})
		})
	}
	if d.runs != nil {
		for _, s := range d.runs.List("web", web.ChatID, 200) {
			if s.Status == runlog.Running || s.Ended.Before(since) || s.Ended.After(until) {
				continue
			}
			run, ok := d.runs.Get("web", web.ChatID, s.ID)
			if !ok {
				continue
			}
			name := s.Name
			if strings.TrimSpace(name) == "" {
				name = "(unnamed)"
			}
			in.Runs = append(in.Runs, digest.Run{Name: name, Kind: s.Kind, Status: s.Status, Result: run.Result, Ended: s.Ended})
		}
	}
	if d.watches != nil {
		if ws, err := d.watches.List("web", web.ChatID); err == nil {
			for _, w := range ws {
				if !w.LastChange.Before(since) && !w.LastChange.After(until) {
					in.Watches = append(in.Watches, digest.Item{Title: w.Title, Note: firstOf(w.LastFound, w.LastDiff), At: w.LastChange})
				}
			}
		}
	}
	if d.goals != nil {
		if gs, err := d.goals.List("web", web.ChatID); err == nil {
			for _, g := range gs {
				if g.LastCheckin.Before(since) || g.LastCheckin.After(until) {
					continue
				}
				note := ""
				if n := len(g.Notes); n > 0 {
					note = g.Notes[n-1].Text
				}
				in.Goals = append(in.Goals, digest.Item{Title: g.Title, Note: note, At: g.LastCheckin})
			}
		}
	}
	if d.cards != nil {
		if cs, err := d.cards.List("web", web.ChatID); err == nil {
			for _, c := range cs {
				if c.Finished.Before(since) || c.Finished.After(until) || c.Column != kanban.Review && c.Status == "" {
					continue
				}
				note := "now in Review"
				if c.Status != "" {
					note = "did not finish: " + c.Status
				}
				in.Board = append(in.Board, digest.Item{Title: c.Title, Note: note, At: c.Finished})
			}
		}
	}
	if d.ledger != nil && d.prices != nil {
		if l := d.ledger(); l != nil {
			days := int(until.Sub(since).Hours()/24) + 1
			rep := l.Report(days, d.prices)
			if rep.Total.Totals.Calls > 0 {
				in.Cost = rep.Summary(usagePeriod(days))
			}
		}
	}
	if d.waiting != nil {
		in.Approvals, in.ToReview = d.waiting()
	}
	return digest.Build(in)
}

func firstOf(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// digestWindow is what a scheduled digest covers: since it last ran, or the length of its cycle when it has not, and
// never more than a fortnight.
func digestWindow(j cron.Job, now time.Time) (since time.Time) {
	span := 24 * time.Hour
	if digestWeekly(j.Schedule) {
		span = 7 * 24 * time.Hour
	}
	since = now.Add(-span)
	if !j.LastRun.IsZero() && j.LastRun.After(now.Add(-14*24*time.Hour)) && j.LastRun.Before(now) {
		since = j.LastRun
	}
	return since
}

// digestWeekly says the schedule ("CRON_TZ=Zone 30 8 * * 1") runs once a week.
func digestWeekly(schedule string) bool {
	f := strings.Fields(schedule)
	return len(f) == 6 && f[5] != "*"
}

func digestJobIn(jobs []cron.Job) (cron.Job, bool) {
	for _, j := range jobs {
		if j.Gateway == "web" && j.ChatID == web.ChatID && j.Kind == "" && j.Name == digestName {
			return j, true
		}
	}
	return cron.Job{}, false
}

// digestInfo is the digest's schedule as the app reads it.
func webDigest(store *cron.Store, zone *time.Location) func() web.DigestInfo {
	return func() web.DigestInfo {
		jobs, err := store.List()
		if err != nil {
			return web.DigestInfo{}
		}
		j, ok := digestJobIn(jobs)
		if !ok {
			return web.DigestInfo{}
		}
		return web.DigestInfo{Enabled: true, At: briefTime(j.Schedule), Weekly: digestWeekly(j.Schedule), Next: j.NextRun.In(zone), Last: j.LastRun}
	}
}

// setDigest turns the scheduled digest on, daily or weekly (on Mondays) at a time of day in the person's zone, or off.
// Turning it on again replaces the job, so there is never more than one.
func setDigest(store *cron.Store, zone *time.Location) func(bool, string, bool) error {
	return func(enabled bool, at string, weekly bool) error {
		if jobs, err := store.List(); err == nil {
			if j, ok := digestJobIn(jobs); ok {
				if err := store.Remove(j.ID, j.Gateway, j.ChatID); err != nil {
					return err
				}
			}
		}
		if !enabled {
			return nil
		}
		var h, m int
		if _, err := fmt.Sscanf(at, "%d:%d", &h, &m); err != nil || !web.ValidClock(at) {
			return fmt.Errorf("give the time as hours and minutes, such as 08:00")
		}
		dow := "*"
		if weekly {
			dow = "1"
		}
		_, err := store.Add(cron.Job{
			Name: digestName, Gateway: "web", ChatID: web.ChatID,
			Prompt:   "(written by Talaria itself: no model is asked)",
			Schedule: fmt.Sprintf("CRON_TZ=%s %d %d * * %s", zone.String(), m, h, dow),
		})
		return err
	}
}

// runDigest prints what Talaria did on its own lately: tal digest [hours] (default 24).
func (c *cli) runDigestCmd(args []string) error {
	hours := 24
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 24*30 {
			return fmt.Errorf("usage: tal digest [hours]  (1 to 720; the default is 24)")
		}
		hours = n
	}
	zone, _ := calendarZone(c.cfg, c.getenv)
	now := time.Now()
	src := digestSources{home: c.home, zone: zone,
		runs:   &runlog.Log{Path: filepath.Join(c.home, "runs.json")},
		ledger: func() *usage.Ledger { return usage.Open(filepath.Join(c.home, "usage.json"), zone) }, prices: usagePrices(c.cfg)}
	fmt.Println(src.build(now.Add(-time.Duration(hours)*time.Hour), now).Text)
	return nil
}
