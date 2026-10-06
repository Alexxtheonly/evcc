package tariff

import (
	"errors"
	"math"
	"strings"

	"github.com/spf13/cast"
)

// Location is a solar forecast's site position in decimal degrees.
type Location struct {
	Lat, Lon float64
}

// LocationFromConfig reads the lat/lon parameters a solar forecast template
// was configured with (forecast-solar, open-meteo, ...). ok is false when the
// configuration carries no valid coordinates - they are never guessed.
func LocationFromConfig(other map[string]any) (Location, bool) {
	get := func(key string) (float64, bool) {
		for k, v := range other {
			if strings.EqualFold(k, key) {
				f, err := cast.ToFloat64E(v)
				return f, err == nil
			}
		}
		return 0, false
	}
	lat, okLat := get("lat")
	lon, okLon := get("lon")
	if !okLat || !okLon || lat < -90 || lat > 90 || lon < -180 || lon > 180 || lat == 0 && lon == 0 {
		return Location{}, false
	}
	return Location{Lat: lat, Lon: lon}, true
}

// SolarLocation returns the one site position all configured solar forecasts
// agree on. It fails when there is no solar forecast, when any forecast has no
// coordinates or when forecasts disagree by more than about 10 km.
func (t *Tariffs) SolarLocation() (Location, error) {
	if t == nil || len(t.SolarLocations) == 0 {
		return Location{}, errors.New("no solar forecast configured")
	}
	first := t.SolarLocations[0]
	for _, l := range t.SolarLocations {
		if l == nil {
			return Location{}, errors.New("solar forecast has no coordinates")
		}
		if math.Abs(l.Lat-first.Lat) > 0.1 || math.Abs(l.Lon-first.Lon) > 0.1 {
			return Location{}, errors.New("solar forecasts disagree on coordinates")
		}
	}
	return *first, nil
}
