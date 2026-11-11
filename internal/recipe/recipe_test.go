package recipe

import (
	"strings"
	"testing"
	"time"

	"talaria/internal/cron"
)

func TestEveryRecipeIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, g := range Groups {
		groups[g] = true
	}
	for _, r := range Catalog {
		if r.ID == "" || r.Title == "" || r.Summary == "" || seen[r.ID] {
			t.Fatalf("recipe %q is incomplete or repeated", r.ID)
		}
		seen[r.ID] = true
		if !groups[r.Group] {
			t.Errorf("%s: group %q is not listed", r.ID, r.Group)
		}
		for _, n := range r.Needs {
			if n != NeedsMail && n != NeedsCalendar && n != NeedsWeb {
				t.Errorf("%s: needs %q", r.ID, n)
			}
		}
		declared := map[string]bool{}
		for _, p := range r.Params {
			declared[p.ID] = true
		}
		var text string
		switch r.Kind {
		case Schedule:
			if r.Prompt == "" || r.When == "" || r.Spec != nil {
				t.Fatalf("%s: a schedule recipe has a prompt and a time", r.ID)
			}
			text = r.Prompt
			// it must be a schedule the scheduler accepts, and fit its prompt limit with every value at full length
			cr, err := r.CronFor("Africa/Lagos", r.When)
			if err != nil {
				t.Fatalf("%s: %v", r.ID, err)
			}
			if _, err := cron.ParseSchedule(cr); err != nil {
				t.Errorf("%s: %q is not a schedule: %v", r.ID, cr, err)
			}
			if len(r.Prompt)+len(r.Params)*maxValue > 3900 {
				t.Errorf("%s: its prompt may not fit the job limit once filled in", r.ID)
			}
		case Watch:
			if r.Spec == nil || r.Prompt != "" || r.Spec.ArgsTemplate == "" {
				t.Fatalf("%s: a watch recipe has a spec and no prompt", r.ID)
			}
			text = r.Spec.ArgsTemplate + r.Spec.Instructions
			if r.Spec.EveryMinutes < 5 || r.Spec.EveryMinutes > 1440 {
				t.Errorf("%s: polls every %d minutes", r.ID, r.Spec.EveryMinutes)
			}
			switch r.Spec.Condition {
			case "":
			case "price_below":
				if !declared[r.Spec.USDParam] {
					t.Errorf("%s: no price parameter", r.ID)
				}
			case "text_appears":
				if !declared[r.Spec.TextParam] {
					t.Errorf("%s: no words parameter", r.ID)
				}
			default:
				t.Errorf("%s: condition %q", r.ID, r.Spec.Condition)
			}
		default:
			t.Fatalf("%s: kind %q", r.ID, r.Kind)
		}
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			if !declared[m[1]] {
				t.Errorf("%s: {%s} is not a parameter", r.ID, m[1])
			}
		}
	}
}

// A recipe runs with nobody watching and no risky tools, so it may read, draft and make a page and nothing more. The
// names are checked against what each prompt mentions, so a recipe cannot be written that asks for an action.
func TestNoRecipeNamesAnActingTool(t *testing.T) {
	acting := []string{"email_send", "email_reply", "shell", "write_file", "x_post", "calendar_add", "calendar_update", "calendar_delete",
		"browser_click", "browser_type", "browser_fill", "files_delete", "cron_add", "task_start", "goal_create", "watch_create", "skill_save", "pdf_form_fill"}
	for _, r := range Catalog {
		text := r.Prompt
		if r.Spec != nil {
			text += r.Spec.Instructions
		}
		for _, name := range acting {
			if strings.Contains(text, name) {
				t.Errorf("%s names %s, which acts", r.ID, name)
			}
		}
	}
}

func TestEveryScheduleRecipeFiresAtItsTimeInTheUsersZone(t *testing.T) {
	lagos, _ := time.LoadLocation("Africa/Lagos")
	cases := map[string]string{"evening-wrapup": "Wed", "weekly-review": "Fri", "receipts-expenses": "Sun", "news-topic": ""}
	for id, day := range cases {
		r, _ := ByID(id)
		cr, _ := r.CronFor("Africa/Lagos", r.When)
		sch, err := cron.ParseSchedule(cr)
		if err != nil {
			t.Fatal(err)
		}
		next, ok := sch.Next(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)) // a Wednesday
		if !ok {
			t.Fatalf("%s never fires", id)
		}
		at := next.In(lagos)
		if want := r.When; at.Format("15:04") != want {
			t.Errorf("%s fires at %s, want %s", id, at.Format("15:04"), want)
		}
		if day != "" && at.Weekday().String()[:3] != day {
			t.Errorf("%s fires on %s, want %s", id, at.Weekday(), day)
		}
	}
	r, _ := ByID("meeting-prep")
	if _, err := r.CronFor("Africa/Lagos", "7:45"); err == nil {
		t.Error("a time without a leading zero was accepted")
	}
	if _, err := r.CronFor("Africa/Lagos", "25:00"); err == nil {
		t.Error("an hour past 23 was accepted")
	}
}

func TestValuesAreCleanedAndWrongOnesRefused(t *testing.T) {
	r, _ := ByID("price-watch")
	ok, err := r.Values(map[string]string{"url": "https://shop.example/item?id=1", "price": "$249.99", "near": "  Our   price \n"})
	if err != nil || ok["near"] != "Our price" || ok["price"] != "$249.99" {
		t.Fatalf("%v %v", ok, err)
	}
	for name, in := range map[string]map[string]string{
		"no url":         {"price": "5"},
		"file address":   {"url": "file:///etc/passwd", "price": "5"},
		"script address": {"url": "javascript:alert(1)", "price": "5"},
		"no price":       {"url": "https://x.example"},
		"negative":       {"url": "https://x.example", "price": "-3"},
		"not a number":   {"url": "https://x.example", "price": "cheap"},
		"extra":          {"url": "https://x.example", "price": "5", "tool": "shell"},
		"too long":       {"url": "https://x.example/" + strings.Repeat("a", 400), "price": "5"},
	} {
		if _, err := r.Values(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	news, _ := ByID("news-topic")
	vals, err := news.Values(map[string]string{"topic": "solar\npower {url}"})
	if err != nil {
		t.Fatal(err)
	}
	// a value is data: braces in it are not filled again, and it cannot add a line to the instruction
	if got := Fill(news.Prompt, vals); !strings.Contains(got, "solar power {url}.") || strings.Contains(got, "solar\n") {
		t.Fatalf("%s", got)
	}
}
