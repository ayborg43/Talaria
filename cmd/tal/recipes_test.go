package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/guard"
	"talaria/internal/recipe"
	"talaria/internal/web"
)

func recipeKit(t *testing.T, rules map[string]string) (*web.RecipeHooks, *cron.Store) {
	t.Helper()
	home := t.TempDir()
	cfg := app.Config{Home: home, Model: "m"}
	if rules != nil {
		pol, err := guard.ParsePolicy(rules, nil)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Guard = &guard.Guard{Policy: pol, Grants: &guard.Grants{Path: filepath.Join(home, "g.json")}}
	}
	rt := app.Open(cfg)
	t.Cleanup(rt.Close)
	store := &cron.Store{Path: filepath.Join(home, "jobs.json")}
	lagos, _ := time.LoadLocation("Africa/Lagos")
	return webRecipes(rt, store, lagos, func() cron.Control { return nil }), store
}

func TestAScheduledRecipeIsOneOrdinaryJobThatCanBeMovedAndRemoved(t *testing.T) {
	h, store := recipeKit(t, nil)
	msg, err := h.Add("evening-wrapup", nil, "")
	if err != nil || !strings.Contains(msg, "every weekday at 18:00") || !strings.Contains(msg, "cannot send, post or change anything") {
		t.Fatalf("the read-back: %q %v", msg, err)
	}
	jobs, _ := store.List()
	if len(jobs) != 1 {
		t.Fatalf("%d jobs", len(jobs))
	}
	j := jobs[0]
	if j.Recipe != "evening-wrapup" || j.Gateway != "web" || j.ChatID != web.ChatID || j.AllowRisky || j.Kind != "" || j.Schedule != "CRON_TZ=Africa/Lagos 0 18 * * 1-5" {
		t.Fatalf("the job is wrong (it must not be allowed risky tools): %+v", j)
	}
	if st := h.State()["evening-wrapup"]; st.At != "18:00" || st.Next.IsZero() {
		t.Fatalf("state %+v", st)
	}
	if _, err := h.Add("evening-wrapup", nil, "19:30"); err != nil { // moving it replaces it
		t.Fatal(err)
	}
	if jobs, _ = store.List(); len(jobs) != 1 || h.State()["evening-wrapup"].At != "19:30" {
		t.Fatalf("moving it left %d jobs", len(jobs))
	}
	if _, err := h.Add("evening-wrapup", nil, "7:3"); err == nil {
		t.Fatal("a bad time was accepted")
	}
	if err := h.Remove("evening-wrapup"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = store.List(); len(jobs) != 0 {
		t.Fatalf("remove left %d jobs", len(jobs))
	}
	if err := h.Remove("evening-wrapup"); err == nil {
		t.Fatal("removing what is not there said nothing")
	}
}

func TestARecipeThatNeedsAnAnswerFillsItInAndRefusesWhatIsWrong(t *testing.T) {
	h, store := recipeKit(t, nil)
	if _, err := h.Add("news-topic", nil, ""); err == nil {
		t.Fatal("a topic recipe with no topic was added")
	}
	if _, err := h.Add("news-topic", map[string]string{"topic": "solar prices", "shell": "rm -rf"}, ""); err == nil {
		t.Fatal("an extra value was accepted")
	}
	if _, err := h.Add("news-topic", map[string]string{"topic": "solar prices"}, ""); err != nil {
		t.Fatal(err)
	}
	jobs, _ := store.List()
	if len(jobs) != 1 || !strings.Contains(jobs[0].Prompt, "news update on: solar prices.") || strings.Contains(jobs[0].Prompt, "{") {
		t.Fatalf("the topic was not filled in: %+v", jobs)
	}
	if v := h.State()["news-topic"].Values; v["topic"] != "solar prices" {
		t.Fatalf("what was filled in should come back for editing: %v", v)
	}
	msg, err := h.Now("news-topic", map[string]string{"topic": "solar prices"})
	if err != nil || !strings.Contains(msg, "once now") {
		t.Fatalf("%q %v", msg, err)
	}
	all, _ := store.List()
	var task *cron.Job
	for i := range all {
		if all[i].Kind == cron.KindTask {
			task = &all[i]
		}
	}
	if task == nil || task.AllowRisky || !strings.Contains(task.Prompt, "solar prices") {
		t.Fatalf("running it once should start an ordinary background task with risky tools off: %+v", all)
	}
	if _, err := h.Now("price-watch", nil); err == nil {
		t.Fatal("a watch was run as a task")
	}
}

// Recipes are held to the user's rules like anything else that schedules or starts work.
func TestRulesAgainstSchedulingStopRecipesToo(t *testing.T) {
	for rule, want := range map[string]string{"deny": "may never", "handoff": "yourself"} {
		h, store := recipeKit(t, map[string]string{"cron_add": rule, "task_start": rule})
		if _, err := h.Add("evening-wrapup", nil, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("rule %s let a recipe be added: %v", rule, err)
		}
		if _, err := h.Now("evening-wrapup", nil); err == nil {
			t.Errorf("rule %s let a recipe start work", rule)
		}
		if jobs, _ := store.List(); len(jobs) != 0 {
			t.Errorf("rule %s left %d jobs", rule, len(jobs))
		}
	}
	h, _ := recipeKit(t, map[string]string{"cron_add": "ask"}) // asking is what the tap is
	if _, err := h.Add("evening-wrapup", nil, ""); err != nil {
		t.Fatalf("a rule that asks should let the user's own tap through: %v", err)
	}
}

func TestAWatchRecipeBuildsTheSameCallTheAgentWould(t *testing.T) {
	r, _ := recipe.ByID("price-watch")
	vals, err := r.Values(map[string]string{"url": `https://shop.example/item?q="x"&id=1`, "price": "$249.99", "near": "Our price"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := watchArgs(r, vals)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Title        string
		Tool         string
		Every        int `json:"every_minutes"`
		Arguments    struct{ URL string }
		Instructions string
		When         struct {
			Kind string
			USD  float64
			Near string
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not valid JSON (an address with a quote must not break it): %v\n%s", err, raw)
	}
	if got.Tool != "web_fetch" || got.Every != 60 || got.Arguments.URL != vals["url"] || got.When.Kind != "price_below" || got.When.USD != 249.99 || got.When.Near != "Our price" || got.Title != "Price watch: shop.example" {
		t.Fatalf("%+v", got)
	}
	pw, _ := recipe.ByID("page-watch")
	v2, _ := pw.Values(map[string]string{"url": "https://jobs.example/", "matter": "an engineer role"})
	raw, _ = watchArgs(pw, v2)
	if !strings.Contains(string(raw), "about this: an engineer role.") || strings.Contains(string(raw), `"when"`) {
		t.Fatalf("a page watch has instructions and no condition: %s", raw)
	}
}
