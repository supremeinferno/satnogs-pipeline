package orbit

import (
	"math"
	"testing"
	"time"
)

func declinationDeg(sun [3]float64) float64 {
	return math.Asin(sun[2]/math.Sqrt(dot(sun, sun))) * 180 / math.Pi
}

func TestSunDeclination(t *testing.T) {
	// The Sun's declination is the Earth's axial tilt at the solstices and
	// zero at the equinoxes.
	tests := []struct {
		name string
		at   time.Time
		want float64
	}{
		{"June solstice 2026", time.Date(2026, 6, 21, 8, 24, 0, 0, time.UTC), 23.44},
		{"December solstice 2026", time.Date(2026, 12, 21, 20, 50, 0, 0, time.UTC), -23.44},
		{"March equinox 2026", time.Date(2026, 3, 20, 14, 46, 0, 0, time.UTC), 0},
		{"September equinox 2026", time.Date(2026, 9, 23, 0, 5, 0, 0, time.UTC), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := declinationDeg(SunPosition(tt.at)); math.Abs(got-tt.want) > 0.1 {
				t.Fatalf("declination %.3f deg, want %.2f", got, tt.want)
			}
		})
	}
}

func TestSunDistance(t *testing.T) {
	// 0.983 AU in early January (perihelion), 1.017 AU in early July (aphelion).
	norm := func(v [3]float64) float64 { return math.Sqrt(dot(v, v)) / astronomicalUnitKm }
	jan := norm(SunPosition(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)))
	jul := norm(SunPosition(time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)))
	if math.Abs(jan-0.9833) > 0.001 || math.Abs(jul-1.0167) > 0.001 {
		t.Fatalf("distance %.4f AU in January, %.4f AU in July", jan, jul)
	}
}

func TestInSunlight(t *testing.T) {
	sun := [3]float64{astronomicalUnitKm, 0, 0} // Sun along +x
	tests := []struct {
		name string
		r    [3]float64
		want bool
	}{
		{"sun side", [3]float64{7000, 0, 0}, true},
		{"night side, directly behind the Earth", [3]float64{-7000, 0, 0}, false},
		{"night side, just inside the shadow", [3]float64{-7000, 6300, 0}, false},
		{"night side, just outside the shadow", [3]float64{-7000, 6500, 0}, true},
		{"over the terminator, clear of the shadow", [3]float64{-1, 7000, 0}, true},
		{"far behind the Earth, off the axis", [3]float64{-40000, 0, 7000}, true},
		{"far behind the Earth, on the axis", [3]float64{-40000, 0, 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inSunlight(tt.r, sun); got != tt.want {
				t.Fatalf("inSunlight(%v) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}
