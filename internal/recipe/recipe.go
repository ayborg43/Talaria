// Package recipe holds the ready-made jobs the app offers with one tap: a stored instruction, when it should run,
// and what the user fills in (a topic, a page address). A recipe is data: it creates an ordinary scheduled job or
// watch, so everything that governs those (the permission rules, quiet hours, Pause all, the activity log) governs
// it too, and nothing here can do more than a job the user typed themselves.
package recipe

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Kinds of recipe.
const (
	Schedule = "schedule" // a scheduled prompt, run by the model
	Watch    = "watch"    // a page polled without the model, which speaks only when something changes
)

// Needs a recipe may name: what has to be connected for it to be worth adding.
const (
	NeedsMail     = "mail"
	NeedsCalendar = "calendar"
	NeedsWeb      = "web"
)

// Param is one thing the user fills in before a recipe is added.
type Param struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Kind        string `json:"kind"` // text, url or number
	Required    bool   `json:"required"`
}

// WatchSpec is what a watch recipe polls and waits for.
type WatchSpec struct {
	// ArgsTemplate is the JSON arguments of web_fetch, with {id} placeholders.
	ArgsTemplate string
	// Condition is "" (tell me about any change that matters), "price_below" or "text_appears".
	Condition string
	// USDParam, TextParam and NearParam name the parameters holding a condition's price, words, and the text a
	// price follows.
	USDParam, TextParam, NearParam string
	Instructions                   string
	EveryMinutes                   int
}

// Recipe is one ready-made job.
type Recipe struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Group   string   `json:"group"`
	Needs   []string `json:"needs,omitempty"`
	Kind    string   `json:"kind"`
	// When is the default time of day for a schedule, and Days which days it runs: "daily", "weekdays" or
	// "weekly:fri" (a day name).
	When   string  `json:"when,omitempty"`
	Days   string  `json:"days,omitempty"`
	Params []Param `json:"params,omitempty"`
	// Prompt is what the model is asked each time, with {id} placeholders. It is shown to the user in full before the
	// recipe is added: what a recipe does is never hidden.
	Prompt string     `json:"prompt,omitempty"`
	Spec   *WatchSpec `json:"-"`
}

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// ByID finds a recipe in the catalog.
func ByID(id string) (Recipe, bool) {
	for _, r := range Catalog {
		if r.ID == id {
			return r, true
		}
	}
	return Recipe{}, false
}

const maxValue = 300

// CleanValue checks one filled-in value: one line, a sensible length, and for a url an http or https address (so a
// recipe can never be pointed at a file or a script).
func CleanValue(p Param, v string) (string, error) {
	v = strings.Join(strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	switch {
	case v == "" && p.Required:
		return "", fmt.Errorf("%s is needed", strings.ToLower(p.Label))
	case v == "":
		return "", nil
	case len([]rune(v)) > maxValue:
		return "", fmt.Errorf("%s is too long (%d characters at most)", strings.ToLower(p.Label), maxValue)
	}
	switch p.Kind {
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("%s must be a web address starting with http:// or https://", strings.ToLower(p.Label))
		}
	case "number":
		var f float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(v, "$"), "%g", &f); err != nil || f <= 0 || f > 1e9 {
			return "", fmt.Errorf("%s must be a number above zero", strings.ToLower(p.Label))
		}
	}
	return v, nil
}

// Values checks and cleans every parameter of r from what the user sent. Parameters r does not have are refused.
func (r Recipe) Values(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	known := map[string]bool{}
	for _, p := range r.Params {
		known[p.ID] = true
		v, err := CleanValue(p, in[p.ID])
		if err != nil {
			return nil, err
		}
		out[p.ID] = v
	}
	for k := range in {
		if !known[k] {
			return nil, fmt.Errorf("%q is not something this recipe asks for", k)
		}
	}
	return out, nil
}

// Fill puts the cleaned values into text. A value is data, never markup, so it is put in as it is; a placeholder
// with no value stays out rather than leaving the braces in the instruction.
func Fill(text string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string { return values[m[1:len(m)-1]] })
}

var weekdays = map[string]string{"sun": "0", "mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6"}

// CronFor writes the schedule of a recipe at a time of day ("07:30") in a time zone, as the scheduler reads it.
func (r Recipe) CronFor(zone, at string) (string, error) {
	if len(at) != 5 || at[2] != ':' {
		return "", fmt.Errorf("give the time as hours and minutes, such as 07:30")
	}
	var h, m int
	if _, err := fmt.Sscanf(at, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", fmt.Errorf("give the time as hours and minutes, such as 07:30")
	}
	dow := "*"
	switch {
	case r.Days == "daily" || r.Days == "":
	case r.Days == "weekdays":
		dow = "1-5"
	case strings.HasPrefix(r.Days, "weekly:"):
		d, ok := weekdays[strings.TrimPrefix(r.Days, "weekly:")]
		if !ok {
			return "", fmt.Errorf("recipe %s has a bad day", r.ID)
		}
		dow = d
	default:
		return "", fmt.Errorf("recipe %s has a bad schedule", r.ID)
	}
	return fmt.Sprintf("CRON_TZ=%s %d %d * * %s", zone, m, h, dow), nil
}

// DaysText says in words when a schedule recipe runs, for the app and the read-back.
func (r Recipe) DaysText() string {
	switch {
	case r.Days == "weekdays":
		return "every weekday"
	case strings.HasPrefix(r.Days, "weekly:"):
		names := map[string]string{"sun": "Sunday", "mon": "Monday", "tue": "Tuesday", "wed": "Wednesday", "thu": "Thursday", "fri": "Friday", "sat": "Saturday"}
		return "every " + names[strings.TrimPrefix(r.Days, "weekly:")]
	}
	return "every day"
}
