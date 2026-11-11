package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"talaria/internal/recipe"
)

// RecipeAdded says a scheduled recipe is set up, and when it next runs.
type RecipeAdded struct {
	At   string    `json:"at"`             // time of day, "18:00", in the person's zone
	Next time.Time `json:"next,omitzero"`  // when it runs next
	Last string    `json:"last,omitempty"` // how its last run went
	// Values is what was filled in when it was added, to show again when it is changed.
	Values map[string]string `json:"values,omitempty"`
}

// RecipeHooks connects the recipes screen to what makes jobs and watches. Add returns a sentence saying exactly
// what was set up, which the page shows.
type RecipeHooks struct {
	State  func() map[string]RecipeAdded
	Caps   func() map[string]bool // mail, calendar and web: which are connected
	Add    func(id string, values map[string]string, at string) (string, error)
	Now    func(id string, values map[string]string) (string, error)
	Remove func(id string) error
}

type recipeView struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Summary string         `json:"summary"`
	Group   string         `json:"group"`
	Kind    string         `json:"kind"`
	When    string         `json:"when,omitempty"`
	Days    string         `json:"days,omitempty"` // in words: "every weekday"
	Params  []recipe.Param `json:"params,omitempty"`
	Prompt  string         `json:"prompt"` // what it asks, in full: nothing a recipe does is hidden
	Ready   bool           `json:"ready"`
	Missing []string       `json:"missing,omitempty"` // what to connect first
	Added   *RecipeAdded   `json:"added,omitempty"`
}

var needLabel = map[string]string{recipe.NeedsMail: "your email", recipe.NeedsCalendar: "your calendar", recipe.NeedsWeb: "web access"}

func (s *Server) recipesView() map[string]any {
	h := s.Sources.Recipes
	caps := map[string]bool{recipe.NeedsWeb: true}
	var added map[string]RecipeAdded
	if h != nil {
		if h.Caps != nil {
			for k, v := range h.Caps() {
				caps[k] = v
			}
		}
		if h.State != nil {
			added = h.State()
		}
	}
	var out []recipeView
	for _, r := range recipe.Catalog {
		v := recipeView{ID: r.ID, Title: r.Title, Summary: r.Summary, Group: r.Group, Kind: r.Kind, Params: r.Params, Ready: true}
		if r.Kind == recipe.Schedule {
			v.When, v.Days, v.Prompt = r.When, r.DaysText(), r.Prompt
		} else {
			v.Prompt = "Polls the page with web_fetch every " + minutesText(r.Spec.EveryMinutes) + ", without the model. " + r.Spec.Instructions
		}
		for _, n := range r.Needs {
			if !caps[n] {
				v.Ready = false
				v.Missing = append(v.Missing, needLabel[n])
			}
		}
		if a, ok := added[r.ID]; ok {
			a := a
			v.Added = &a
		}
		out = append(out, v)
	}
	return map[string]any{"recipes": out, "groups": recipe.Groups, "available": h != nil}
}

func minutesText(m int) string {
	switch {
	case m == 60:
		return "hour"
	case m%60 == 0:
		return strconv.Itoa(m/60) + " hours"
	}
	return strconv.Itoa(m) + " minutes"
}

func (s *Server) recipesGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.recipesView())
}

// recipesPost adds a recipe, runs one once now, or takes one away. What is added is an ordinary job or watch, so the
// permission rules and quiet hours govern it; the answer says in a sentence exactly what was set up.
func (s *Server) recipesPost(w http.ResponseWriter, r *http.Request) {
	h := s.Sources.Recipes
	if h == nil {
		fail(w, http.StatusNotImplemented, "recipes are not set up here")
		return
	}
	var in struct {
		Action string            `json:"action"` // add, now or remove
		ID     string            `json:"id"`
		Values map[string]string `json:"values"`
		At     string            `json:"at"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.ID, in.At = strings.TrimSpace(in.ID), strings.TrimSpace(in.At)
	rc, ok := recipe.ByID(in.ID)
	if !ok {
		fail(w, http.StatusNotFound, "there is no such recipe")
		return
	}
	if !slices.Contains([]string{"add", "now", "remove"}, in.Action) {
		fail(w, http.StatusBadRequest, "say add, now or remove")
		return
	}
	if in.At != "" && !ValidClock(in.At) {
		fail(w, http.StatusBadRequest, "give the time as hours and minutes, such as 07:30")
		return
	}
	out := map[string]any{"ok": true}
	var err error
	switch in.Action {
	case "add":
		out["message"], err = h.Add(rc.ID, in.Values, in.At)
	case "now":
		if rc.Kind != recipe.Schedule {
			fail(w, http.StatusBadRequest, "a watch has nothing to run now: it speaks when the page changes")
			return
		}
		out["message"], err = h.Now(rc.ID, in.Values)
	case "remove":
		err = h.Remove(rc.ID)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	for k, v := range s.recipesView() {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}
