package geo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() { nominatimInterval = 0 } // the tests need not sit out the rate limit

// fake returns a service pointed at a server that answers these paths.
func fake(t *testing.T, h http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Service{Nominatim: srv.URL, OSRM: srv.URL, Client: srv.Client(), UserAgent: "test"}
}

func TestLookupParsesPlaces(t *testing.T) {
	var gotUserAgent string
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("lookup asked for %q", r.URL.Path)
		}
		gotUserAgent = r.Header.Get("User-Agent")
		if r.URL.Query().Get("q") != "Lekki" || r.URL.Query().Get("addressdetails") != "1" {
			t.Errorf("lookup sent %v", r.URL.Query())
		}
		json.NewEncoder(w).Encode([]nominatimPlace{{
			Lat: "6.4541", Lon: "3.3947", DisplayName: "Lekki, Lagos, Nigeria",
			Name: "Lekki", Class: "place", Type: "suburb",
		}})
	})
	ps, err := s.Lookup(context.Background(), "Lekki", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "Lekki" || ps[0].Address != "Lekki, Lagos, Nigeria" {
		t.Fatalf("%+v", ps)
	}
	if ps[0].Lat != 6.4541 || ps[0].Lng != 3.3947 {
		t.Fatalf("position %+v", ps[0])
	}
	if gotUserAgent != "test" {
		t.Fatalf("the service must be told who is asking: %q", gotUserAgent)
	}
}

func TestLookupClampsTheLimitAndRefusesAnEmptyQuery(t *testing.T) {
	var sent string
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.URL.Query().Get("limit")
		w.Write([]byte(`[]`))
	})
	if _, err := s.Lookup(context.Background(), "x", 99); err != nil {
		t.Fatal(err)
	}
	if sent != "10" {
		t.Fatalf("limit sent %q, want 10", sent)
	}
	if _, err := s.Lookup(context.Background(), "  ", 3); err == nil {
		t.Fatal("an empty query must be refused")
	}
}

func TestReverseReadsTheNearestPlace(t *testing.T) {
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reverse" {
			t.Errorf("reverse asked for %q", r.URL.Path)
		}
		if q := r.URL.Query(); q.Get("lat") != "6.454100" || q.Get("lon") != "3.394700" {
			t.Errorf("reverse sent %v", q)
		}
		json.NewEncoder(w).Encode(nominatimPlace{
			Lat: "6.4541", Lon: "3.3947", DisplayName: "12 Admiralty Way, Lekki, Lagos",
			Name: "12 Admiralty Way", Class: "building", Type: "yes",
		})
	})
	p, err := s.At(context.Background(), 6.4541, 3.3947)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "12 Admiralty Way" || p.Kind != "building:yes" {
		t.Fatalf("%+v", p)
	}
}

func TestReverseSaysWhenNothingIsThere(t *testing.T) {
	s := fake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	if _, err := s.At(context.Background(), 0, 0); err == nil {
		t.Fatal("an empty answer must be an error")
	}
}

func TestPlacesResolvesTheCentreThenSortsNearestFirst(t *testing.T) {
	var searches int
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		searches++
		q := r.URL.Query()
		switch q.Get("q") {
		case "Ikeja":
			json.NewEncoder(w).Encode([]nominatimPlace{{Lat: "6.5", Lon: "3.4", DisplayName: "Ikeja", Name: "Ikeja"}})
		case "cafe":
			if q.Get("bounded") != "1" || !strings.Contains(q.Get("viewbox"), ",") {
				t.Errorf("a search near a place must be bounded by a viewbox: %v", q)
			}
			json.NewEncoder(w).Encode([]nominatimPlace{
				{Lat: "6.51", Lon: "3.4", DisplayName: "Cafe Far", Name: "Cafe Far", Class: "amenity", Type: "cafe"},
				{Lat: "6.501", Lon: "3.4", DisplayName: "Cafe Near", Name: "Cafe Near", Class: "amenity", Type: "cafe"},
				{Lat: "6.52", Lon: "3.4", DisplayName: "Cafe Farthest", Name: "Cafe Farthest", Class: "amenity", Type: "cafe"},
			})
		default:
			t.Errorf("unexpected query %q", q.Get("q"))
		}
	})
	ps, err := s.Places(context.Background(), "cafe", "Ikeja", 1500, 10)
	if err != nil {
		t.Fatal(err)
	}
	if searches != 2 {
		t.Fatalf("wanted a lookup for the centre and one search, got %d requests", searches)
	}
	if len(ps) != 3 {
		t.Fatalf("%d places", len(ps))
	}
	if ps[0].Name != "Cafe Near" || ps[1].Name != "Cafe Far" || ps[2].Name != "Cafe Farthest" {
		t.Fatalf("not nearest first: %v", []string{ps[0].Name, ps[1].Name, ps[2].Name})
	}
	if !(ps[0].DistanceM < ps[1].DistanceM && ps[1].DistanceM < ps[2].DistanceM) {
		t.Fatalf("distances %v", []float64{ps[0].DistanceM, ps[1].DistanceM, ps[2].DistanceM})
	}
}

func TestRouteBuildsStepsAndInstructions(t *testing.T) {
	var path, profile string
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		path, profile = r.URL.Path, r.URL.Query().Get("steps")
		if !strings.HasPrefix(path, "/route/v1/walking/") {
			t.Errorf("path %q", path)
		}
		w.Write([]byte(`{"code":"Ok","routes":[{"distance":5200,"duration":720,"legs":[{"steps":[
			{"distance":100,"duration":20,"name":"Main Street","maneuver":{"type":"depart","modifier":"","exit":0}},
			{"distance":5000,"duration":600,"name":"Broadway","maneuver":{"type":"turn","modifier":"right","exit":0}},
			{"distance":100,"duration":100,"name":"","maneuver":{"type":"arrive","modifier":"","exit":0}}
		]}]}]}`))
	})
	r, err := s.Route(context.Background(), "walking", "6.45,3.40", "6.46,3.41")
	if err != nil {
		t.Fatal(err)
	}
	if profile != "true" {
		t.Fatalf("steps not asked for: %q", profile)
	}
	if r.DistanceM != 5200 || r.DurationS != 720 || len(r.Steps) != 3 {
		t.Fatalf("%+v", r)
	}
	if r.Steps[0].Instruction != "Start" {
		t.Fatalf("depart: %q", r.Steps[0].Instruction)
	}
	if r.Steps[1].Instruction != "Turn right onto Broadway" {
		t.Fatalf("turn: %q", r.Steps[1].Instruction)
	}
	if r.Steps[2].Instruction != "Arrive at your destination" {
		t.Fatalf("arrive: %q", r.Steps[2].Instruction)
	}
	out := FormatRoute(r)
	for _, want := range []string{"5.2 km", "12 min", "Turn right onto Broadway", "OpenStreetMap"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the route text is missing %q:\n%s", want, out)
		}
	}
}

func TestRouteResolvesNamedEndsAndRejectsABadProfile(t *testing.T) {
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			json.NewEncoder(w).Encode([]nominatimPlace{{Lat: "1", Lon: "2", DisplayName: "Somewhere", Name: "Somewhere"}})
		case strings.HasPrefix(r.URL.Path, "/route/"):
			t.Errorf("a bad profile must be refused before any routing request: %s", r.URL.Path)
		}
	})
	if _, err := s.Route(context.Background(), "flying", "A", "B"); err == nil {
		t.Fatal("flying is not a profile")
	}
}

func TestRouteReportsAProblemFromTheService(t *testing.T) {
	s := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			json.NewEncoder(w).Encode([]nominatimPlace{{Lat: "1", Lon: "2", DisplayName: "x", Name: "x"}})
			return
		}
		w.Write([]byte(`{"code":"NoRoute"}`))
	})
	if _, err := s.Route(context.Background(), "driving", "1,2", "3,4"); err == nil || !strings.Contains(err.Error(), "NoRoute") {
		t.Fatalf("%v", err)
	}
}

func TestCheckURL(t *testing.T) {
	cases := []struct {
		raw          string
		allowPrivate bool
		ok           bool
	}{
		{"https://nominatim.openstreetmap.org", false, true},
		{"https://nominatim.openstreetmap.org/", false, true},
		{"http://127.0.0.1:5000", false, false},
		{"http://127.0.0.1:5000", true, true},
		{"http://localhost:5000", false, false},
		{"http://localhost:5000", true, true},
		{"http://10.0.0.5:5000", false, false},
		{"https://user:pass@example.com", false, false},
		{"ftp://example.com", false, false},
		{"not an address", false, false},
	}
	for _, c := range cases {
		err := CheckURL(c.raw, c.allowPrivate)
		if (err == nil) != c.ok {
			t.Errorf("CheckURL(%q, %v) = %v, want ok=%v", c.raw, c.allowPrivate, err, c.ok)
		}
	}
}

func TestParsePoint(t *testing.T) {
	cases := []struct {
		in  string
		ok  bool
		lat float64
		lng float64
	}{
		{"6.45,3.40", true, 6.45, 3.40},
		{"6.45, 3.40", true, 6.45, 3.40},
		{"6.45 3.40", true, 6.45, 3.40},
		{"New York", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, c := range cases {
		lat, lng, ok := parsePoint(c.in)
		if ok != c.ok || (ok && (lat != c.lat || lng != c.lng)) {
			t.Errorf("parsePoint(%q) = %v,%v,%v", c.in, lat, lng, ok)
		}
	}
}

func TestManeuverWording(t *testing.T) {
	cases := []struct {
		typ, modifier, name string
		exit                int
		want                string
	}{
		{"turn", "left", "Main Street", 0, "Turn left onto Main Street"},
		{"merge", "slight right", "", 0, "Merge slight right"},
		{"roundabout", "right", "Ring Road", 2, "At the roundabout, take the 2nd exit onto Ring Road"},
		{"roundabout", "right", "Ring Road", 0, "At the roundabout, continue onto Ring Road"},
		{"arrive", "", "", 0, "Arrive at your destination"},
	}
	for _, c := range cases {
		var st osrmStep
		st.Name = c.name
		st.Maneuver.Type, st.Maneuver.Modifier, st.Maneuver.Exit = c.typ, c.modifier, c.exit
		if got := maneuver(st); got != c.want {
			t.Errorf("%s/%s/%q = %q, want %q", c.typ, c.modifier, c.name, got, c.want)
		}
	}
}

func TestFormatPlacesSaysWhenNothingWasFound(t *testing.T) {
	out := FormatPlaces(nil, "Nothing here:")
	if !strings.Contains(out, "Nothing was found.") || !strings.Contains(out, "OpenStreetMap") {
		t.Fatalf("%q", out)
	}
}
