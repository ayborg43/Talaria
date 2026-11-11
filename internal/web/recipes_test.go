package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
)

func TestTheRecipeListSaysWhatIsReadyWhatIsAddedAndWhatEachOneAsks(t *testing.T) {
	r := newRig(t)
	r.login()
	if _, body := r.do("GET", "/api/recipes", nil); !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("with no hooks recipes should say so: %s", body)
	}
	next := time.Now().Add(time.Hour)
	r.srv.Sources.Recipes = &RecipeHooks{
		Caps: func() map[string]bool { return map[string]bool{"mail": false, "calendar": true, "web": true} },
		State: func() map[string]RecipeAdded {
			return map[string]RecipeAdded{"evening-wrapup": {At: "18:00", Next: next}}
		},
	}
	_, body := r.do("GET", "/api/recipes", nil)
	var got struct {
		Available bool         `json:"available"`
		Recipes   []recipeView `json:"recipes"`
		Groups    []string     `json:"groups"`
	}
	if err := json.Unmarshal(body, &got); err != nil || !got.Available || len(got.Recipes) < 10 || len(got.Groups) != 3 {
		t.Fatalf("%v: %s", err, body)
	}
	by := map[string]recipeView{}
	for _, v := range got.Recipes {
		by[v.ID] = v
	}
	if v := by["newsletter-digest"]; v.Ready || len(v.Missing) != 1 || v.Missing[0] != "your email" {
		t.Fatalf("a recipe that needs mail should say so when there is none: %+v", v)
	}
	if v := by["meeting-prep"]; !v.Ready || v.Days != "every weekday" || v.When != "07:45" {
		t.Fatalf("%+v", v)
	}
	if v := by["evening-wrapup"]; v.Added == nil || v.Added.At != "18:00" {
		t.Fatalf("an added recipe should say so: %+v", v)
	}
	if v := by["news-topic"]; len(v.Params) != 1 || !strings.Contains(v.Prompt, "{topic}") {
		t.Fatalf("what a recipe asks is shown in full, with its blank: %+v", v)
	}
	if v := by["price-watch"]; v.Kind != "watch" || !strings.Contains(v.Prompt, "without the model") || !strings.Contains(v.Prompt, "every hour") {
		t.Fatalf("%+v", v)
	}
}

func TestAddingRunningAndRemovingARecipeOverHTTP(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("POST", "/api/recipes", map[string]any{"action": "add", "id": "evening-wrapup"}); resp.StatusCode != 501 {
		t.Fatalf("with no hooks: %d", resp.StatusCode)
	}
	var calls []string
	r.srv.Sources.Recipes = &RecipeHooks{
		Add: func(id string, v map[string]string, at string) (string, error) {
			if id == "news-topic" && v["topic"] == "" {
				return "", errors.New("topic is needed")
			}
			calls = append(calls, "add:"+id+":"+at+":"+v["topic"])
			return "set it up", nil
		},
		Now: func(id string, v map[string]string) (string, error) {
			calls = append(calls, "now:"+id)
			return "started", nil
		},
		Remove: func(id string) error { calls = append(calls, "remove:"+id); return nil },
	}
	resp, body := r.do("POST", "/api/recipes", map[string]any{"action": "add", "id": "news-topic", "values": map[string]string{"topic": "solar"}, "at": "06:30"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"message":"set it up"`) || !strings.Contains(string(body), `"recipes"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/recipes", map[string]any{"action": "now", "id": "news-topic", "values": map[string]string{"topic": "solar"}}); resp.StatusCode != 200 {
		t.Fatalf("now: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/recipes", map[string]any{"action": "remove", "id": "news-topic"}); resp.StatusCode != 200 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if strings.Join(calls, ",") != "add:news-topic:06:30:solar,now:news-topic,remove:news-topic" {
		t.Fatalf("%v", calls)
	}
	for name, c := range map[string]struct {
		in   map[string]any
		code int
	}{
		"unknown recipe":   {map[string]any{"action": "add", "id": "everything"}, 404},
		"unknown action":   {map[string]any{"action": "delete", "id": "news-topic"}, 400},
		"bad time":         {map[string]any{"action": "add", "id": "news-topic", "at": "7pm"}, 400},
		"a watch run now":  {map[string]any{"action": "now", "id": "price-watch"}, 400},
		"the hook refuses": {map[string]any{"action": "add", "id": "news-topic"}, 400},
	} {
		if resp, body := r.do("POST", "/api/recipes", c.in); resp.StatusCode != c.code {
			t.Errorf("%s answered %d (%s), want %d", name, resp.StatusCode, body, c.code)
		}
	}
}

func TestTheRoutineModelIsChosenInSettingsAndOnlyAmongYourProfiles(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{"strong": {Model: "big"}, "zippy": {Model: "small", BestFor: []string{"cheap"}}}}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Getenv = func(string) string { return "" }
	r.login()
	view := func() (choice, using string) {
		_, body := r.do("GET", "/api/settings", nil)
		var v struct {
			Choice string `json:"routine_profile"`
			Using  string `json:"routine_using"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		return v.Choice, v.Using
	}
	if c, u := view(); c != "" || u != "zippy" {
		t.Fatalf("automatic should pick the profile marked cheap: %q %q", c, u)
	}
	for name, want := range map[string]string{"strong": "strong", "default": "", "": "zippy"} {
		if resp, body := r.do("POST", "/api/settings", map[string]any{"routine_profile": name}); resp.StatusCode != 200 || strings.Contains(string(body), "restart_needed") {
			t.Fatalf("%q: %d %s", name, resp.StatusCode, body)
		}
		if _, u := view(); u != want {
			t.Errorf("choice %q: routine jobs use %q, want %q", name, u, want)
		}
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"routine_profile": "nope"}); resp.StatusCode != 400 {
		t.Fatalf("an unknown profile answered %d", resp.StatusCode)
	}
	raw, _ := os.ReadFile(r.srv.ConfigPath)
	if strings.Contains(string(raw), "nope") {
		t.Fatalf("a refused choice was saved: %s", raw)
	}
}
