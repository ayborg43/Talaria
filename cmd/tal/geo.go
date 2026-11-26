package main

import (
	"fmt"

	"talaria/internal/config"
	"talaria/internal/geo"
)

// buildGeo makes the place and route service from the config's maps section, using the free
// OpenStreetMap services when it names none. An address that cannot be used is reported and the
// tools are left out, rather than stopping Talaria: everything else still works.
func buildGeo(cfg *config.Config) (*geo.Service, []string) {
	nominatim := cfg.Maps.NominatimURL
	if nominatim == "" {
		nominatim = geo.DefaultNominatim
	}
	osrm := cfg.Maps.OSRMURL
	if osrm == "" {
		osrm = geo.DefaultOSRM
	}
	var problems []string
	if err := geo.CheckURL(nominatim, cfg.Maps.AllowPrivate); err != nil {
		problems = append(problems, fmt.Sprintf("maps.nominatim_url: %v", err))
	}
	if err := geo.CheckURL(osrm, cfg.Maps.AllowPrivate); err != nil {
		problems = append(problems, fmt.Sprintf("maps.osrm_url: %v", err))
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return &geo.Service{Nominatim: nominatim, OSRM: osrm}, nil
}
