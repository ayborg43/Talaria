package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"talaria/internal/config"
	"talaria/internal/provider"
	"talaria/internal/usage"
)

// openLedger starts counting what the models are asked and what it costs. It reads the ledger already on disk and
// reports every answered request to it from now on.
func (c *cli) openLedger() {
	if c.ledger != nil {
		return
	}
	zone, _ := calendarZone(c.cfg, c.getenv)
	c.ledger = usage.Open(filepath.Join(c.home, "usage.json"), zone)
	provider.SetSink(func(sp provider.Spent) {
		c.ledger.Add(sp)
		if t := c.tap.Load(); t != nil {
			(*t)(sp)
		}
	})
}

// usagePrices is how a profile's cost is looked up: the owner's own figures in the config, read live so a price
// saved under Settings counts at once.
func usagePrices(cfg *config.Config) usage.Prices {
	return func(profile string) (usage.Price, bool) {
		in, out, ok := cfg.Price(profile)
		return usage.Price{In: in, Out: out}, ok
	}
}

func usagePeriod(days int) string {
	switch days {
	case 1:
		return "today"
	case 7:
		return "this week"
	case 30:
		return "this month"
	}
	return fmt.Sprintf("in the last %d days", days)
}

// runUsage prints what the models were used for and what that cost.
func (c *cli) runUsageCmd(args []string) error {
	return runUsage(c.cfg, c.home, args, c.getenv, os.Stdout)
}

func runUsage(cfg *config.Config, home string, args []string, getenv func(string) string, w io.Writer) error {
	days := 7
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 400 {
			return fmt.Errorf("usage: tal usage [days]  (1 to 400; the default is 7)")
		}
		days = n
	}
	zone, _ := calendarZone(cfg, getenv)
	rep := usage.Open(filepath.Join(home, "usage.json"), zone).Report(days, usagePrices(cfg))
	fmt.Fprintln(w, rep.Summary(usagePeriod(days)))
	if rep.Total.Totals.Calls == 0 {
		return nil
	}
	table := func(title string, ls []usage.Line) {
		if len(ls) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, l := range ls {
			cost := "(no price)"
			if l.Priced {
				cost = usage.Money(l.Cost)
			}
			fmt.Fprintf(w, "  %-34s %6d calls  %8s in  %8s out  %s\n", l.Name, l.Totals.Calls, usage.Tokens(l.Totals.In), usage.Tokens(l.Totals.Out), cost)
		}
	}
	table("By model", rep.ByModel)
	table("By agent", rep.ByAgent)
	table("By kind of work", rep.ByKind)
	table("By who asked", rep.BySource)
	fmt.Fprintln(w, "\nA price is set per profile (\"prices\" in the config, or under Settings: dollars for a million tokens in and out).")
	return nil
}
