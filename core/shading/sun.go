// Package shading learns a multiplicative solar forecast correction per sun
// position from archived forecasts and measured PV energy (ADR-016).
package shading

import (
	"math"
	"time"
)

// Location is a site position in decimal degrees, north and east positive.
type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Valid reports whether the location is a usable coordinate pair.
func (l Location) Valid() bool {
	return !math.IsNaN(l.Lat) && !math.IsNaN(l.Lon) &&
		l.Lat >= -90 && l.Lat <= 90 && l.Lon >= -180 && l.Lon <= 180
}

const deg = math.Pi / 180

// SunPosition returns the sun's azimuth (degrees clockwise from north, 0..360)
// and geometric elevation (degrees above the horizon, no refraction) at t,
// using the NOAA solar calculator equations. Accurate to well below 0.1° for
// dates between 1901 and 2099.
func SunPosition(t time.Time, loc Location) (azimuth, elevation float64) {
	jd := float64(t.UnixNano())/float64(24*time.Hour) + 2440587.5
	T := (jd - 2451545) / 36525

	l0 := math.Mod(280.46646+T*(36000.76983+T*0.0003032), 360)
	m := 357.52911 + T*(35999.05029-0.0001537*T)
	e := 0.016708634 - T*(0.000042037+0.0000001267*T)
	c := math.Sin(m*deg)*(1.914602-T*(0.004817+0.000014*T)) +
		math.Sin(2*m*deg)*(0.019993-0.000101*T) +
		math.Sin(3*m*deg)*0.000289
	omega := 125.04 - 1934.136*T
	lambda := l0 + c - 0.00569 - 0.00478*math.Sin(omega*deg)
	eps0 := 23 + (26+(21.448-T*(46.815+T*(0.00059-T*0.001813)))/60)/60
	eps := eps0 + 0.00256*math.Cos(omega*deg)
	decl := math.Asin(math.Sin(eps*deg) * math.Sin(lambda*deg))

	y := math.Pow(math.Tan(eps*deg/2), 2)
	eqTime := 4 / deg * (y*math.Sin(2*l0*deg) - 2*e*math.Sin(m*deg) +
		4*e*y*math.Sin(m*deg)*math.Cos(2*l0*deg) -
		0.5*y*y*math.Sin(4*l0*deg) - 1.25*e*e*math.Sin(2*m*deg))

	u := t.UTC()
	minutes := float64(u.Hour()*60+u.Minute()) + float64(u.Second())/60 + float64(u.Nanosecond())/6e10
	ha := (minutes+eqTime+4*loc.Lon)/4 - 180

	lat := loc.Lat * deg
	h := ha * deg
	cosZen := math.Sin(lat)*math.Sin(decl) + math.Cos(lat)*math.Cos(decl)*math.Cos(h)
	zen := math.Acos(max(-1, min(1, cosZen)))

	az := math.Atan2(math.Sin(h), math.Cos(h)*math.Sin(lat)-math.Tan(decl)*math.Cos(lat))/deg + 180
	return math.Mod(az+360, 360), 90 - zen/deg
}
