// Package orbit answers one question about a satellite's position: is it in
// sunlight or in the Earth's shadow?
package orbit

import (
	"math"
	"time"
)

const (
	earthRadiusKm      = 6378.137
	astronomicalUnitKm = 149597870.7
)

// SunPosition returns the Sun's position in km in the TEME frame (the frame
// SGP4 uses) at time t. It is the low-precision series from Vallado,
// "Fundamentals of Astrodynamics and Applications", Algorithm 29: about
// 0.01 degrees, which is far finer than anything here needs. Treating the
// mean equator of date as TEME changes the answer by even less.
func SunPosition(t time.Time) [3]float64 {
	jd := float64(t.UnixNano())/1e9/86400 + 2440587.5
	T := (jd - 2451545.0) / 36525.0

	rad := math.Pi / 180
	meanLon := 280.460 + 36000.771*T // mean longitude, degrees
	meanAnomaly := (357.5277233 + 35999.05034*T) * rad
	eclipticLon := (meanLon + 1.914666471*math.Sin(meanAnomaly) + 0.019994643*math.Sin(2*meanAnomaly)) * rad
	distanceAU := 1.000140612 - 0.016708617*math.Cos(meanAnomaly) - 0.000139589*math.Cos(2*meanAnomaly)
	obliquity := (23.439291 - 0.0130042*T) * rad

	return [3]float64{
		distanceAU * math.Cos(eclipticLon) * astronomicalUnitKm,
		distanceAU * math.Cos(obliquity) * math.Sin(eclipticLon) * astronomicalUnitKm,
		distanceAU * math.Sin(obliquity) * math.Sin(eclipticLon) * astronomicalUnitKm,
	}
}

// InSunlight reports whether a satellite at r (km, TEME) is lit by the Sun at
// time t.
//
// The shadow is modelled as a cylinder along the Sun-Earth line with the
// Earth's radius. The real shadow is a cone (and has a penumbra), which only
// matters for the few seconds around entering and leaving the shadow of a
// low orbit.
func InSunlight(r [3]float64, t time.Time) bool {
	return inSunlight(r, SunPosition(t))
}

func inSunlight(r, sun [3]float64) bool {
	norm := math.Sqrt(dot(sun, sun))
	s := [3]float64{sun[0] / norm, sun[1] / norm, sun[2] / norm} // unit vector to the Sun

	along := dot(r, s)
	if along >= 0 {
		return true // on the Sun's side of the Earth
	}
	// Behind the Earth: lit only if clear of the shadow cylinder.
	perp := [3]float64{r[0] - along*s[0], r[1] - along*s[1], r[2] - along*s[2]}
	return math.Sqrt(dot(perp, perp)) > earthRadiusKm
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
