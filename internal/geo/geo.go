// Package geo gives the agent places and directions from the free OpenStreetMap
// services: geocoding and reverse geocoding (Nominatim) and routing (OSRM). It
// is read-only: every tool returns text the model can reason about, and the
// results count as outside content, like a web page or a mail, because other
// people write the place names and road names.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default endpoints are the public, free services. Both ask for a polite User-Agent and a light
// touch; they are the owner's own lookups, not a bulk import.
const (
	DefaultNominatim = "https://nominatim.openstreetmap.org"
	DefaultOSRM      = "https://router.project-osrm.org"
)

// nominatimInterval spaces the requests to Nominatim, whose policy asks for at most one a second.
// A variable so tests need not wait.
var nominatimInterval = 1100 * time.Millisecond

// CheckURL reports whether raw is a usable service address: http or https, no username or password,
// and — unless allowPrivate — a host that is not this machine or a private network. The addresses are
// set by the owner in the config, never by the model, but they are still checked so a misconfigured
// address cannot point the fetches at a service that trusts the local network.
func CheckURL(raw string, allowPrivate bool) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("the address is not a web address")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("the address must start with https:// or http://")
	}
	if u.User != nil {
		return errors.New("the address must not hold a username or password")
	}
	if allowPrivate {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if privateIP(ip) {
			return errors.New("the address points at this machine or a private network, which is not allowed unless maps.allow_private is true")
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("the address points at this machine or a private network, which is not allowed unless maps.allow_private is true")
	}
	return nil
}

func privateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64.0.0/10
}

// Service holds the two endpoints and an HTTP client. The zero value points at the public services.
type Service struct {
	Nominatim string
	OSRM      string
	UserAgent string
	// Client is the HTTP client; nil is a plain client with a 20-second timeout. Tests replace it
	// with the one their test server builds.
	Client *http.Client

	mu   sync.Mutex
	last time.Time
}

func (s *Service) nominatim() string {
	if s.Nominatim == "" {
		return DefaultNominatim
	}
	return strings.TrimRight(s.Nominatim, "/")
}

func (s *Service) osrm() string {
	if s.OSRM == "" {
		return DefaultOSRM
	}
	return strings.TrimRight(s.OSRM, "/")
}

func (s *Service) agent() string {
	if s.UserAgent == "" {
		return "Talaria (personal agent)"
	}
	return s.UserAgent
}

// wait holds Nominatim to at most one request a second, as its usage policy asks.
func (s *Service) wait() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := time.Since(s.last); d < nominatimInterval {
		time.Sleep(nominatimInterval - d)
	}
	s.last = time.Now()
}

// Place is one result of a lookup: a place with a name, a kind, an address and a position.
type Place struct {
	Name      string
	Kind      string
	Address   string
	Lat, Lng  float64
	DistanceM float64 // set by Places: how far from the centre of the search
}

// Route is one routing answer: how far, how long, and the turn-by-turn steps.
type Route struct {
	DistanceM float64
	DurationS float64
	Steps     []Step
}

// Step is one instruction of a route.
type Step struct {
	Instruction string
	DistanceM   float64
	DurationS   float64
	Name        string
}

// ---- Nominatim ----

type nominatimPlace struct {
	Lat         string            `json:"lat"`
	Lon         string            `json:"lon"`
	DisplayName string            `json:"display_name"`
	Name        string            `json:"name"`
	Class       string            `json:"class"`
	Type        string            `json:"type"`
	Address     map[string]string `json:"address"`
}

// Lookup finds up to limit places matching an address or a name. limit is clamped to 1..10.
func (s *Service) Lookup(ctx context.Context, query string, limit int) ([]Place, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("lookup needs a place name or address to look for")
	}
	if limit < 1 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("addressdetails", "1")
	var out []nominatimPlace
	s.wait()
	if err := s.get(ctx, s.nominatim()+"/search", q, &out); err != nil {
		return nil, err
	}
	return places(out), nil
}

// At finds the nearest place to a position (reverse geocoding).
func (s *Service) At(ctx context.Context, lat, lng float64) (Place, error) {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	q.Set("lon", strconv.FormatFloat(lng, 'f', 6, 64))
	q.Set("format", "jsonv2")
	q.Set("addressdetails", "1")
	var out nominatimPlace
	s.wait()
	if err := s.get(ctx, s.nominatim()+"/reverse", q, &out); err != nil {
		return Place{}, err
	}
	p := toPlace(out)
	if p.Name == "" && p.Address == "" {
		return Place{}, errors.New("nothing was found at that position")
	}
	return p, nil
}

// Places lists up to limit places of one kind near a point or a named place. radius is in metres,
// clamped to 50..50000 (default 1500). Results are ordered by distance from the centre.
func (s *Service) Places(ctx context.Context, query, near string, radius, limit int) ([]Place, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("say what kind of place to look for")
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	if radius < 50 {
		radius = 1500
	}
	if radius > 50000 {
		radius = 50000
	}
	lat, lng, err := s.resolve(ctx, near)
	if err != nil {
		return nil, err
	}
	// Nominatim searches inside a viewbox rather than a radius; a square of the right size is close enough.
	dlat := float64(radius) / 111320.0
	dlng := float64(radius) / (111320.0 * math.Cos(lat*math.Pi/180))
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("addressdetails", "1")
	q.Set("viewbox", fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", lng-dlng, lat-dlat, lng+dlng, lat+dlat))
	q.Set("bounded", "1")
	var out []nominatimPlace
	s.wait()
	if err := s.get(ctx, s.nominatim()+"/search", q, &out); err != nil {
		return nil, err
	}
	ps := places(out)
	for i := range ps {
		ps[i].DistanceM = haversine(lat, lng, ps[i].Lat, ps[i].Lng)
	}
	// Nominatim does not sort by distance; do it here.
	sortByDistance(ps)
	if len(ps) > limit {
		ps = ps[:limit]
	}
	return ps, nil
}

func places(in []nominatimPlace) []Place {
	out := make([]Place, 0, len(in))
	for _, n := range in {
		out = append(out, toPlace(n))
	}
	return out
}

func toPlace(n nominatimPlace) Place {
	lat, _ := strconv.ParseFloat(n.Lat, 64)
	lng, _ := strconv.ParseFloat(n.Lon, 64)
	name := n.Name
	if name == "" {
		name = n.DisplayName
	}
	kind := n.Type
	if n.Class != "" && n.Class != "place" {
		kind = n.Class + ":" + n.Type
	}
	return Place{
		Name:    clean(name, 120),
		Kind:    clean(kind, 60),
		Address: clean(n.DisplayName, 220),
		Lat:     lat,
		Lng:     lng,
	}
}

// ---- OSRM ----

type osrmResponse struct {
	Code   string `json:"code"`
	Routes []struct {
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
		Legs     []struct {
			Steps []osrmStep `json:"steps"`
		} `json:"legs"`
	} `json:"routes"`
}

type osrmStep struct {
	Distance float64 `json:"distance"`
	Duration float64 `json:"duration"`
	Name     string  `json:"name"`
	Maneuver struct {
		Type     string `json:"type"`
		Modifier string `json:"modifier"`
		Exit     int    `json:"exit"`
	} `json:"maneuver"`
}

// Route plans a journey between two places, each a "lat,lng" pair or a name to look up. profile is
// driving, walking or cycling (default driving).
func (s *Service) Route(ctx context.Context, profile, from, to string) (Route, error) {
	switch profile = strings.ToLower(strings.TrimSpace(profile)); profile {
	case "", "driving":
		profile = "driving"
	case "walking", "cycling":
	default:
		return Route{}, errors.New("profile must be driving, walking or cycling")
	}
	flat, flng, err := s.resolve(ctx, from)
	if err != nil {
		return Route{}, fmt.Errorf("from %q: %w", from, err)
	}
	tlat, tlng, err := s.resolve(ctx, to)
	if err != nil {
		return Route{}, fmt.Errorf("to %q: %w", to, err)
	}
	path := fmt.Sprintf("/route/v1/%s/%.6f,%.6f;%.6f,%.6f", profile, flng, flat, tlng, tlat)
	q := url.Values{}
	q.Set("steps", "true")
	q.Set("overview", "false")
	var out osrmResponse
	if err := s.get(ctx, s.osrm()+path, q, &out); err != nil {
		return Route{}, err
	}
	if out.Code != "Ok" {
		return Route{}, fmt.Errorf("the route service could not find a route (%s)", out.Code)
	}
	if len(out.Routes) == 0 {
		return Route{}, errors.New("the route service returned no route")
	}
	r := out.Routes[0]
	route := Route{DistanceM: r.Distance, DurationS: r.Duration}
	for _, leg := range r.Legs {
		for _, st := range leg.Steps {
			route.Steps = append(route.Steps, Step{
				Instruction: maneuver(st),
				DistanceM:   st.Distance,
				DurationS:   st.Duration,
				Name:        clean(st.Name, 120),
			})
		}
	}
	return route, nil
}

// maneuver turns OSRM's type/modifier into a short instruction.
func maneuver(st osrmStep) string {
	t, m := st.Maneuver.Type, st.Maneuver.Modifier
	name := strings.TrimSpace(st.Name)
	dir := strings.ReplaceAll(m, "_", " ")
	onto := ""
	if name != "" {
		onto = " onto " + name
	}
	switch t {
	case "depart":
		if dir != "" {
			return "Head " + dir + onto
		}
		return "Start"
	case "arrive":
		return "Arrive at your destination"
	case "turn":
		return "Turn " + dir + onto
	case "new name":
		return "Continue" + onto
	case "merge":
		return "Merge " + dir + onto
	case "on ramp", "off ramp":
		return "Take the ramp " + dir
	case "fork":
		return "Keep " + dir + onto
	case "end of road":
		return "At the end of the road, turn " + dir + onto
	case "roundabout", "rotary", "roundabout turn":
		if st.Maneuver.Exit <= 0 {
			return "At the roundabout, continue" + onto
		}
		return fmt.Sprintf("At the roundabout, take the %s exit", ord(st.Maneuver.Exit)) + onto
	case "exit roundabout", "exit rotary":
		return "Leave the roundabout" + onto
	default:
		if dir != "" && dir != "straight" {
			return "Continue " + dir + onto
		}
		return "Continue" + onto
	}
}

func ord(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return fmt.Sprintf("%dth", n)
	}
}

// ---- shared ----

// resolve turns "lat,lng" (or "lat lng") into a position, and anything else into the first match
// of a lookup.
func (s *Service) resolve(ctx context.Context, raw string) (lat, lng float64, err error) {
	if lat, lng, ok := parsePoint(raw); ok {
		if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return 0, 0, fmt.Errorf("%s is not a latitude and longitude", raw)
		}
		return lat, lng, nil
	}
	ps, err := s.Lookup(ctx, raw, 1)
	if err != nil {
		return 0, 0, err
	}
	if len(ps) == 0 {
		return 0, 0, fmt.Errorf("no place was found for %q", raw)
	}
	return ps[0].Lat, ps[0].Lng, nil
}

func parsePoint(raw string) (lat, lng float64, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, 0, false
	}
	// "6.45 3.40" and "6.45, 3.40" both work; a name with a space does not parse and falls through.
	s = strings.ReplaceAll(s, " ", ",")
	parts := strings.Split(s, ",")
	i := 0
	var nums []float64
	for _, p := range parts {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, 0, false
		}
		nums = append(nums, f)
		i++
	}
	if len(nums) < 2 {
		return 0, 0, false
	}
	return nums[0], nums[1], true
}

func (s *Service) get(ctx context.Context, u string, q url.Values, v any) error {
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("the service address is not usable")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.agent())
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the place service could not be reached: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return errors.New("the place service's answer could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return errors.New("the place service asked to slow down (HTTP 429): try again in a moment")
		case http.StatusForbidden:
			return errors.New("the place service refused the request (HTTP 403)")
		default:
			return fmt.Errorf("the place service answered HTTP %d", resp.StatusCode)
		}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("the place service answered in a shape that cannot be read")
	}
	return nil
}

// clean makes text from outside safe to show and to hand the model: control and direction-changing
// characters removed, whitespace collapsed, and cut to n characters.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const earth = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earth * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func sortByDistance(ps []Place) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].DistanceM < ps[j-1].DistanceM; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// ---- formatting ----

func formatDistance(m float64) string {
	if m < 1000 {
		return fmt.Sprintf("%.0f m", m)
	}
	return fmt.Sprintf("%.1f km", m/1000)
}

func formatDuration(sec float64) string {
	s := int(math.Round(sec))
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	h := m / 60
	mm := m % 60
	if mm == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, mm)
}

// FormatPlaces renders lookup and search results as one line each.
func FormatPlaces(ps []Place, heading string) string {
	var b strings.Builder
	b.WriteString(heading + "\n")
	if len(ps) == 0 {
		b.WriteString("Nothing was found.\n")
	}
	for i, p := range ps {
		line := fmt.Sprintf("%d. %s", i+1, p.Name)
		if p.Kind != "" {
			line += "  (" + p.Kind + ")"
		}
		if p.DistanceM > 0 {
			line += "  ~" + formatDistance(p.DistanceM)
		}
		if p.Address != "" && p.Address != p.Name {
			line += "\n   " + p.Address
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("(Results from OpenStreetMap's Nominatim, © OpenStreetMap contributors.)\n")
	return b.String()
}

// FormatRoute renders a route as a summary line and numbered steps.
func FormatRoute(r Route) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Route: %s, about %s.\n", formatDistance(r.DistanceM), formatDuration(r.DurationS))
	for i, st := range r.Steps {
		line := st.Instruction
		if st.Name != "" && !strings.Contains(st.Instruction, st.Name) {
			line += " (" + st.Name + ")"
		}
		fmt.Fprintf(&b, "%d. %s  [%s]\n", i+1, line, formatDistance(st.DistanceM))
	}
	b.WriteString("(Route from OSRM; map data © OpenStreetMap contributors.)\n")
	return b.String()
}
