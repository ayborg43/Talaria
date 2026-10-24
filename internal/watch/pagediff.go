package watch

import (
	"fmt"
	"regexp"
	"strings"
)

// Page-diff helpers: when a watched page is polled, only a real change should
// cost a model run. A line whose only difference is relative-time wording
// ("posted 1 day ago" -> "posted 2 days ago") is not news; a line whose
// numbers changed (a price, a count) is reported as an update.

// PageLines splits page text into distinct lines for comparison, mirroring
// OpenMuse's page-diff: whitespace runs collapse, lines over 300 runes are
// cut, blanks are dropped, and repeats count once.
func PageLines(text string, limit ...int) []string {
	n := 2000
	if len(limit) > 0 && limit[0] > 0 {
		n = limit[0]
	}
	seen := map[string]bool{}
	out := make([]string, 0, 32)
	space := regexp.MustCompile(`\s+`)
	for _, raw := range strings.Split(text, "\n") {
		line := space.ReplaceAllString(strings.TrimSpace(raw), " ")
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 300 {
			line = string(r[:300])
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return out
}

// relativeTime matches English and German relative-time wording that ticks on
// its own ("58 minutes ago" -> "1 hour ago", "vor 2 Tagen"). A change in it
// alone is not news.
var relativeTime = regexp.MustCompile(`(?i)\b(?:\d+|an?|one)\s+(?:sec(?:ond)?|min(?:ute)?|hour|hr|day|week|month|year)s?\s+ago\b|\bvor\s+(?:\d+|einer?|einem)\s+(?:sekunde|minute|stunde|tag|woche|monat|jahr)(?:e|en|n)?\b`)

// WithoutRelativeTimes blanks relative-time wording so equal results mean
// only those times changed.
func WithoutRelativeTimes(text string) string {
	return relativeTime.ReplaceAllString(text, "<time>")
}

var digits = regexp.MustCompile(`\d+(?:[.,]\d+)*`)
var pluralLetter = regexp.MustCompile(`(\p{L})s\b`)

// numberlessKey maps a line to a key that ignores numbers and simple plurals,
// so "14 units" and "9 units" pair as the same line (an update, not news
// plus a removal). Relative times are blanked first so they never reach here.
func numberlessKey(line string) string {
	s := WithoutRelativeTimes(line)
	s = digits.ReplaceAllString(s, "#")
	s = pluralLetter.ReplaceAllString(s, "$1")
	return strings.ToLower(s)
}

// Diff is what changed between two checks of a watched page.
type Diff struct {
	Added   []string
	Updated []string
	Removed []string
}

// Empty reports whether the diff holds no news at all.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Updated) == 0 && len(d.Removed) == 0
}

// pair matches each current line with one unused previous line of the same
// key, so one remaining line can never account for another that was removed.
func pair(previous, current []string, key func(string) string) (paired, unpaired, left []string) {
	open := map[string][]string{}
	for _, line := range previous {
		k := key(line)
		open[k] = append(open[k], line)
	}
	used := map[string]bool{}
	for _, line := range current {
		q := open[key(line)]
		if len(q) == 0 {
			unpaired = append(unpaired, line)
			continue
		}
		open[key(line)] = q[1:]
		used[q[0]] = true
		paired = append(paired, line)
	}
	for _, line := range previous {
		if !used[line] {
			left = append(left, line)
		}
	}
	return paired, unpaired, left
}

func identity(s string) string { return s }

// DiffPage compares two checks of a watched page. Exact matches pair first,
// then lines differing only in relative time are dropped, then lines differing
// only in numbers or plurals are reported as updates; the rest is added and
// removed.
func DiffPage(previous, current []string) Diff {
	samePaired, sameUnpaired, sameLeft := pair(previous, current, identity)
	_, retimedUnpaired, retimedLeft := pair(sameLeft, sameUnpaired, WithoutRelativeTimes)
	renPaired, renUnpaired, renLeft := pair(retimedLeft, retimedUnpaired, numberlessKey)
	_ = samePaired
	return Diff{Added: renUnpaired, Updated: renPaired, Removed: renLeft}
}

// DescribePageDiff renders a short "New / Updated / Removed" summary, or ""
// when no line changed.
func DescribePageDiff(d Diff) string {
	section := func(title string, lines []string, limit int) []string {
		if len(lines) == 0 {
			return nil
		}
		out := []string{title + ":"}
		for _, line := range lines {
			if len(out)-1 >= limit {
				break
			}
			if r := []rune(line); len(r) > 160 {
				line = string(r[:160])
			}
			out = append(out, "• "+line)
		}
		if len(lines) > limit {
			out = append(out, fmt.Sprintf("+%d more", len(lines)-limit))
		}
		return out
	}
	parts := append(section("New", d.Added, 8), section("Updated", d.Updated, 4)...)
	parts = append(parts, section("Removed", d.Removed, 4)...)
	return strings.Join(parts, "\n")
}

// CountPageDiff renders a one-line count for task results, or "" when empty.
func CountPageDiff(d Diff) string {
	total := len(d.Added) + len(d.Updated) + len(d.Removed)
	if total == 0 {
		return ""
	}
	parts := []string{}
	if len(d.Added) > 0 {
		parts = append(parts, fmt.Sprintf("%d new", len(d.Added)))
	}
	if len(d.Updated) > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", len(d.Updated)))
	}
	if len(d.Removed) > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", len(d.Removed)))
	}
	word := "lines"
	if total == 1 {
		word = "line"
	}
	return fmt.Sprintf("%d %s changed (%s)", total, word, strings.Join(parts, ", "))
}
