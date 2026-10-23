package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSource serves the real UWE-4 frames of testdata, the real TLE of the same
// day, and a clock.
func fakeSource(t *testing.T, frames []TelemetryFrame, ferr error, now time.Time) snapSource {
	t.Helper()
	text, err := os.ReadFile("testdata/uwe4.tle")
	if err != nil {
		t.Fatal(err)
	}
	return snapSource{
		now: func() time.Time { return now },
		frames: func(ctx context.Context, norad, pages int) ([]TelemetryFrame, error) {
			return frames, ferr
		},
		tle: func(ctx context.Context, norad int) (string, error) { return string(text), nil },
	}
}

func uwe4Frames(t *testing.T) []TelemetryFrame {
	var out []TelemetryFrame
	for ts, raw := range realFrames(t) {
		at, _ := time.Parse(time.RFC3339, ts)
		out = append(out, TelemetryFrame{NoradID: uwe4NORAD, Frame: hex.EncodeToString(raw), Timestamp: at})
	}
	return out
}

var snapNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func uwe4(t *testing.T) satellite {
	s, _ := lookupSatellite(uwe4NORAD)
	return s
}

func TestBuildSnapshot(t *testing.T) {
	snap := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow), nil, []satellite{uwe4(t)})
	if len(snap.Satellites) != 1 {
		t.Fatalf("%d satellites", len(snap.Satellites))
	}
	s := snap.Satellites[0]
	if s.Name != "UWE-4" || len(s.Batteries) != 2 || s.Error != "" {
		t.Fatalf("%+v", s)
	}
	a := s.Batteries[0]
	if a.Name != "a" || len(a.Readings) != 3 || a.Readings[0].T.After(a.Readings[2].T) {
		t.Fatalf("battery a: %+v", a.Readings)
	}
	for _, r := range a.Readings {
		if r.Sunlit == nil || r.MA == nil {
			t.Fatalf("a reading lacks lighting or current: %+v", r)
		}
	}
	if s.SunlitNow == nil || s.TLEEpoch == nil || len(s.Shadows) == 0 {
		t.Fatalf("lighting not worked out: %+v", s)
	}
	for _, name := range []string{"discharging_in_sunlight", "voltage_below_baseline", "low_voltage"} {
		if _, ok := a.Anomalies[name]; !ok {
			t.Errorf("no verdict for %s", name)
		}
	}
	if s.LastBeacon == nil || s.Stale {
		t.Fatalf("last beacon %v stale %v", s.LastBeacon, s.Stale)
	}
}

// A second run adds the new beacons to the old ones and does not repeat a beacon.
func TestSnapshotMergesWithThePreviousOne(t *testing.T) {
	all := uwe4Frames(t)
	first := buildSnapshot(context.Background(), fakeSource(t, all[:2], nil, snapNow), nil, []satellite{uwe4(t)})
	// The next run sees one old frame again and the third, new one.
	second := buildSnapshot(context.Background(), fakeSource(t, all[1:], nil, snapNow.Add(time.Hour)), first, []satellite{uwe4(t)})
	if n := len(second.Satellites[0].Batteries[0].Readings); n != 3 {
		t.Fatalf("battery a has %d readings after merging, want 3", n)
	}
	// And through the file the Action carries from one run to the next.
	dir := t.TempDir()
	if err := writeSnapshot(dir, second); err != nil {
		t.Fatal(err)
	}
	back := readPrevious(filepath.Join(dir, "status.json"))
	if back == nil || len(back.Satellites[0].Batteries[0].Readings) != 3 {
		t.Fatalf("round trip lost readings: %+v", back)
	}
}

func TestSnapshotDropsOldReadings(t *testing.T) {
	snap := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow.Add(snapshotHistory+48*time.Hour)), nil, []satellite{uwe4(t)})
	s := snap.Satellites[0]
	if len(s.Batteries) != 0 || !s.Stale {
		t.Fatalf("readings older than the history window should go: %+v", s.Batteries)
	}
}

// A satellite whose fetch fails keeps its old readings and says why.
func TestSnapshotKeepsOldDataWhenFetchFails(t *testing.T) {
	first := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow), nil, []satellite{uwe4(t)})
	second := buildSnapshot(context.Background(), fakeSource(t, nil, errors.New("HTTP 500"), snapNow.Add(time.Hour)), first, []satellite{uwe4(t)})
	s := second.Satellites[0]
	if len(s.Batteries[0].Readings) != 3 || !strings.Contains(s.Error, "HTTP 500") {
		t.Fatalf("readings %d, error %q", len(s.Batteries[0].Readings), s.Error)
	}
}

// With no orbit data the lighting is unknown, and the rules that need it stay off.
func TestSnapshotWithoutOrbitData(t *testing.T) {
	src := fakeSource(t, uwe4Frames(t), nil, snapNow)
	src.tle = func(ctx context.Context, norad int) (string, error) { return "", errors.New("celestrak down") }
	s := buildSnapshot(context.Background(), src, nil, []satellite{uwe4(t)}).Satellites[0]
	if s.SunlitNow != nil || len(s.Shadows) != 0 || !strings.Contains(s.Error, "celestrak down") {
		t.Fatalf("%+v", s)
	}
	for _, b := range s.Batteries {
		if b.Anomalies["discharging_in_sunlight"] || b.Anomalies["voltage_below_baseline"] {
			t.Errorf("a rule that needs the lighting fired without it: %+v", b.Anomalies)
		}
		for _, r := range b.Readings {
			if r.Sunlit != nil {
				t.Errorf("lighting claimed without orbit data: %+v", r)
			}
		}
	}
}

func TestShadowIntervals(t *testing.T) {
	text, _ := os.ReadFile("testdata/uwe4.tle")
	el, _ := parseTLE(string(text))
	m, _ := newOrbitModel(el)
	from := snapNow.Add(-6 * time.Hour)
	iv := shadowIntervals(m, from, snapNow)
	// A low orbit spends about a third of each 95-minute lap in the shadow.
	if len(iv) < 3 || len(iv) > 5 {
		t.Fatalf("%d shadow stretches in 6 hours", len(iv))
	}
	for _, p := range iv {
		d := p[1].Sub(p[0])
		if d < 15*time.Minute || d > 45*time.Minute || p[0].Before(from) || p[1].After(snapNow) {
			t.Errorf("odd stretch %v to %v", p[0], p[1])
		}
	}
}

func TestReadingIDSameBeaconSameID(t *testing.T) {
	f := []byte{1, 2, 3}
	if readingID(f, "a") != readingID([]byte{1, 2, 3}, "a") || readingID(f, "a") == readingID(f, "b") {
		t.Fatal("ids must follow the bytes and the battery")
	}
}

func TestReadPreviousToleratesNothing(t *testing.T) {
	if readPrevious("") != nil || readPrevious(filepath.Join(t.TempDir(), "missing.json")) != nil {
		t.Fatal("a missing file is the first run")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if readPrevious(bad) != nil {
		t.Fatal("a damaged file starts the history again")
	}
}

func TestSnapshotJSONAndPage(t *testing.T) {
	dir := t.TempDir()
	snap := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow), nil, []satellite{uwe4(t)})
	if err := writeSnapshot(dir, snap); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "status.json"))
	if err := json.Unmarshal(b, &v); err != nil || v["generated_at"] == nil {
		t.Fatalf("status.json: %v", err)
	}
	page, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	if !strings.Contains(string(page), "<title>Satellite batteries</title>") || !strings.Contains(string(page), "status.json") {
		t.Fatal("the page does not read status.json")
	}
}

// When orbit data cannot be fetched, the last run's element set is used, so the
// page keeps its lighting; and the page says so.
func TestSnapshotFallsBackToThePreviousOrbitData(t *testing.T) {
	first := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow), nil, []satellite{uwe4(t)})
	if first.Satellites[0].TLE == "" {
		t.Fatal("the element set used is not kept in the snapshot")
	}
	src := fakeSource(t, nil, nil, snapNow.Add(time.Hour))
	src.tle = func(ctx context.Context, norad int) (string, error) { return "", errors.New("timeout") }
	s := buildSnapshot(context.Background(), src, first, []satellite{uwe4(t)}).Satellites[0]
	if s.SunlitNow == nil || len(s.Shadows) == 0 {
		t.Fatalf("the lighting was lost: %+v", s)
	}
	if !strings.Contains(s.Error, "orbit data of the previous run") || !strings.Contains(s.Error, "timeout") {
		t.Fatalf("the page is not told: %q", s.Error)
	}
	if s.TLE != first.Satellites[0].TLE {
		t.Error("the fallback element set should be kept for the run after")
	}
}

// With no orbit data at all, and none from the last run, the lighting is unknown.
func TestSnapshotWithoutAnyOrbitDataHasNoLighting(t *testing.T) {
	src := fakeSource(t, uwe4Frames(t), nil, snapNow)
	src.tle = func(ctx context.Context, norad int) (string, error) { return "", errors.New("down") }
	s := buildSnapshot(context.Background(), src, nil, []satellite{uwe4(t)}).Satellites[0]
	if s.SunlitNow != nil || s.TLE != "" {
		t.Fatalf("%+v", s)
	}
}

func TestTracks(t *testing.T) {
	snap := buildSnapshot(context.Background(), fakeSource(t, uwe4Frames(t), nil, snapNow), nil, []satellite{uwe4(t)})
	want := int((trackBefore+trackAfter)/trackStep) + 1
	if len(snap.Sun) != want || snap.TrackStepS != 30 || !snap.TrackStart.Equal(snapNow.Add(-trackBefore)) {
		t.Fatalf("%d sun points, step %d, start %s", len(snap.Sun), snap.TrackStepS, snap.TrackStart)
	}
	// The point under the Sun at noon UTC on 22 October: the declination is
	// about -11.2 degrees, and the Sun runs about 15.5 minutes ahead of the
	// clock (the equation of time), so it is 3.9 degrees west of Greenwich.
	noon := snap.Sun[int(trackBefore/trackStep)]
	if math.Abs(noon[0]+11.2) > 0.4 || math.Abs(noon[1]+3.9) > 0.5 {
		t.Errorf("Sun overhead at %v, want about -11.2, -3.9", noon)
	}
	track := snap.Satellites[0].Track
	if len(track) != want {
		t.Fatalf("%d track points, want %d", len(track), want)
	}
	shadow := 0
	for i, p := range track {
		if p[2] < 350 || p[2] > 450 {
			t.Fatalf("point %d: altitude %v km", i, p[2])
		}
		if i > 0 && math.Abs(p[0]-track[i-1][0]) > 2.5 { // 7.7 km/s for 30 s is about 2 degrees
			t.Fatalf("point %d: latitude jumps from %v to %v", i, track[i-1][0], p[0])
		}
		if p[3] == 0 {
			shadow++
		}
	}
	// A 93-minute orbit spends about a third of it in the shadow.
	if f := float64(shadow) / float64(len(track)); f < 0.25 || f > 0.45 {
		t.Errorf("in the shadow %.0f%% of the time", 100*f)
	}
}
