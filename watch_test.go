package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// at builds a reading `ago` before t0 with the given current and voltage.
func at(ago time.Duration, sunlit bool, mA, mV int) Point {
	return Point{At: t0.Add(-ago), Sunlit: sunlit, CurrentMA: &mA, VoltageMV: mV}
}

// atV is a reading without a current, as most satellites send.
func atV(ago time.Duration, sunlit bool, mV int) Point {
	return Point{At: t0.Add(-ago), Sunlit: sunlit, VoltageMV: mV}
}

func TestDischargingInSunlight(t *testing.T) {
	h := time.Hour
	m := time.Minute
	tests := []struct {
		name string
		pts  []Point // newest first
		want bool
	}{
		{"three discharging sunlit readings over hours",
			[]Point{at(0, true, -120, 4000), at(2*h, true, -90, 4010), at(4*h, true, -80, 4020)}, true},
		{"charging as it should", []Point{at(0, true, 150, 4100), at(2*h, true, 140, 4100), at(4*h, true, 130, 4100)}, false},
		{"one discharging reading is normal (seen 8 times in 77)",
			[]Point{at(0, true, -112, 4016), at(2*h, true, 100, 4100), at(4*h, true, 90, 4100)}, false},
		{"older discharge, newest reading charging",
			[]Point{at(0, true, 80, 4100), at(2*h, true, -90, 4010), at(4*h, true, -80, 4020)}, false},
		{"discharging in the shadow is normal",
			[]Point{at(0, false, -300, 3800), at(2*h, false, -250, 3850), at(4*h, false, -280, 3820)}, false},
		{"shadow readings in between are skipped",
			[]Point{at(0, true, -120, 4000), at(1*h, false, -300, 3800), at(2*h, true, -90, 4010), at(3*h, false, -300, 3800), at(4*h, true, -80, 4020)}, true},
		{"three beacons of one pass are one event",
			[]Point{at(0, true, -120, 4000), at(1*m, true, -110, 4000), at(2*m, true, -100, 4000)}, false},
		{"only two sunlit readings", []Point{at(0, true, -120, 4000), at(2*h, true, -90, 4010)}, false},
		{"threshold is exclusive", []Point{at(0, true, dischargeMA, 4000), at(2*h, true, dischargeMA, 4000), at(4*h, true, dischargeMA, 4000)}, false},
		{"a battery with no current has nothing to say", []Point{atV(0, true, 4000), atV(2*h, true, 4000), atV(4*h, true, 4000)}, false},
		{"no readings", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dischargingInSunlight(tt.pts); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLowVoltage(t *testing.T) {
	const limit = 3500
	if lowVoltage(nil, limit) {
		t.Error("no readings must not be an alert")
	}
	if !lowVoltage([]Point{atV(0, false, limit-1)}, limit) {
		t.Error("below the limit should alert, in the shadow too")
	}
	if lowVoltage([]Point{atV(0, true, limit)}, limit) {
		t.Error("at the limit is not below it")
	}
	if lowVoltage([]Point{atV(0, true, 100)}, 0) {
		t.Error("no limit, no alert")
	}
	// Only the newest reading counts: an old low value has recovered.
	if lowVoltage([]Point{atV(0, true, 4000), atV(time.Hour, false, 3400)}, limit) {
		t.Error("old low reading should not alert")
	}
}

// history builds sunlit readings every two hours: `recent` of them at the
// newest end with voltage recentMV, the rest at baseMV.
func history(recent, older int, recentMV, baseMV int) []Point {
	var pts []Point
	for i := 0; i < recent; i++ {
		pts = append(pts, atV(time.Duration(i)*2*time.Hour, true, recentMV))
	}
	for i := 0; i < older; i++ {
		pts = append(pts, atV(time.Duration(recent+i)*2*time.Hour, true, baseMV))
	}
	return pts
}

func TestVoltageBelowBaseline(t *testing.T) {
	tests := []struct {
		name string
		pts  []Point
		want bool
	}{
		{"as usual", history(3, 20, 16300, 16300), false},
		{"a little lower is normal", history(3, 20, 16000, 16300), false}, // -1.8%
		{"clearly lower", history(3, 20, 15400, 16300), true},             // -5.5%
		{"higher is fine", history(3, 20, 16800, 16300), false},
		{"too little history to know the baseline", history(3, baselineMin-1, 12000, 16300), false},
		{"one low reading among the newest three is not enough", history(1, 22, 12000, 16300), false},
		{"two low readings among the newest three are", history(2, 22, 12000, 16300), true},
		{"fewer than three sunlit readings at all", history(2, 0, 12000, 16300), false},
		{"the shadow does not count",
			append([]Point{atV(0, false, 10000), atV(time.Hour, false, 10000), atV(2*time.Hour, false, 10000)}, history(3, 20, 16300, 16300)...), false},
		{"beacons of one pass are one event",
			append([]Point{atV(0, true, 12000), atV(time.Minute, true, 12000), atV(2*time.Minute, true, 12000)}, history(0, 20, 0, 16300)...), false},
		{"works in any unit", history(3, 20, 7600, 8300), true}, // -8.4%, a two-cell pack
		{"no readings", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := voltageBelowBaseline(tt.pts); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTLE(t *testing.T) {
	b, err := os.ReadFile("testdata/uwe4.tle")
	if err != nil {
		t.Fatal(err)
	}
	el, err := parseTLE(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if el.Name != "UWE-4" || el.Epoch.Year() != 2026 {
		t.Fatalf("parsed %q epoch %v", el.Name, el.Epoch)
	}
	if _, err := parseTLE("No GP data found\n"); err == nil {
		t.Fatal("CelesTrak's 'no data' answer must be an error")
	}
	if _, err := parseTLE(""); err == nil {
		t.Fatal("empty answer must be an error")
	}
}

func TestFetchTLE(t *testing.T) {
	b, _ := os.ReadFile("testdata/uwe4.tle")
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write(b)
	}))
	defer srv.Close()
	old := celestrakURL
	celestrakURL = srv.URL
	defer func() { celestrakURL = old }()

	el, err := fetchTLE(context.Background(), 43880)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "CATNR=43880") || el.Name != "UWE-4" {
		t.Fatalf("query %q, name %q", query, el.Name)
	}
}

// With the TLE fetched on 21 Oct, the model must agree with the batteries it
// was checked against: shadow readings never charging.
func TestOrbitModelMatchesRealBeacons(t *testing.T) {
	b, _ := os.ReadFile("testdata/uwe4.tle")
	el, err := parseTLE(string(b))
	if err != nil {
		t.Fatal(err)
	}
	m, err := newOrbitModel(el)
	if err != nil {
		t.Fatal(err)
	}
	// Real readings (UTC) and their battery A current, from the database.
	cases := []struct {
		at     time.Time
		mA     int
		sunlit bool
	}{
		{time.Date(2026, 10, 19, 1, 29, 0, 0, time.UTC), -351, false},
		{time.Date(2026, 10, 19, 4, 8, 0, 0, time.UTC), 136, true},
		{time.Date(2026, 10, 19, 4, 55, 0, 0, time.UTC), -142, false},
	}
	for _, c := range cases {
		got, err := m.sunlit(c.at)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.sunlit {
			t.Errorf("%s: sunlit = %v, want %v (battery current was %d mA)", c.at.Format("01-02 15:04"), got, c.sunlit, c.mA)
		}
	}
}
