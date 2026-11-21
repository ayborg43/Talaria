package web

import (
	"net/http"
	"sort"
	"strconv"
	"talaria/internal/config"

	"talaria/internal/usage"
)

type usagePrice struct {
	Profile  string   `json:"profile"`
	Model    string   `json:"model,omitempty"`
	PriceIn  *float64 `json:"price_in"`
	PriceOut *float64 `json:"price_out"`
}

// priceRows lists every profile a price can be set for: those in the config, or, with no profiles, the one that is in
// use, which is called "default".
func (s *Server) priceRows() []usagePrice {
	rows := []usagePrice{}
	if s.Cfg == nil {
		return rows
	}
	add := func(name, model string) {
		row := usagePrice{Profile: name, Model: model}
		if in, out, ok := s.Cfg.Price(name); ok {
			row.PriceIn, row.PriceOut = &in, &out
		}
		rows = append(rows, row)
	}
	names := s.Cfg.ProfileNames()
	for _, n := range names {
		add(n, s.Cfg.Profiles[n].Model)
	}
	if len(names) == 0 {
		add("default", s.Model)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Profile < rows[j].Profile })
	return rows
}

// usageGet is what the models were used for lately and what it cost: ?days=1, 7 or 30 (any 1 to 90), and the prices
// the owner has set, so the page can offer to set the ones that are missing.
func (s *Server) usageGet(w http.ResponseWriter, r *http.Request) {
	if s.Usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days < 1 || days > 90 {
		days = 7
	}
	rep := s.Usage.Report(days, s.usagePrices())
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "report": rep, "summary": rep.Summary(periodName(days)), "prices": s.priceRows()})
}

func periodName(days int) string {
	switch days {
	case 1:
		return "today"
	case 7:
		return "this week"
	case 30:
		return "this month"
	}
	return "in the last " + strconv.Itoa(days) + " days"
}

func (s *Server) usagePrices() usage.Prices {
	return func(profile string) (usage.Price, bool) {
		if s.Cfg == nil {
			return usage.Price{}, false
		}
		in, out, ok := s.Cfg.Price(profile)
		return usage.Price{In: in, Out: out}, ok
	}
}

// usagePost sets or clears what one profile's model costs. Prices are the owner's own figures: nothing here asks a
// service, and a profile left without one is counted in tokens only.
func (s *Server) usagePost(w http.ResponseWriter, r *http.Request) {
	var in usagePrice
	if !readJSON(w, r, &in) {
		return
	}
	if s.Cfg == nil {
		fail(w, http.StatusNotImplemented, "settings are not available here")
		return
	}
	known := false
	for _, row := range s.priceRows() {
		known = known || row.Profile == in.Profile
	}
	if !known {
		fail(w, http.StatusBadRequest, "that is not one of your profiles")
		return
	}
	for _, v := range []*float64{in.PriceIn, in.PriceOut} {
		if v != nil && (*v < 0 || *v > 100000) {
			fail(w, http.StatusBadRequest, "a price is dollars for a million tokens, between 0 and 100000")
			return
		}
	}
	var price config.ModelPrice
	if in.PriceIn != nil {
		price.In = *in.PriceIn
	}
	if in.PriceOut != nil {
		price.Out = *in.PriceOut
	}
	s.Cfg.SetPrice(in.Profile, price, in.PriceIn != nil || in.PriceOut != nil) // both blank forgets it
	if !s.writeConfig(w) {
		return
	}
	s.usageGet(w, r)
}
