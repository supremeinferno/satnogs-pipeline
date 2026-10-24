package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/supremeinferno/orbitscreen/pkg/sgp4"

	"github.com/supremeinferno/satnogs-pipeline/internal/orbit"
)

// site is the page that shows status.json: index.html, its style, app.js for
// the cards and charts, and globe.js for the three.js globe. With status.json
// that is the whole published site.
//
//go:embed site
var site embed.FS

const (
	snapshotHistory = 14 * 24 * time.Hour // readings kept in status.json
	shadowWindow    = 5 * 24 * time.Hour  // how far back the shadow is drawn
	firstPages      = 10                  // pages (25 frames each) read when there is no earlier snapshot
	nextPages       = 3                   // ...and when there is one: only what is new
	trackBefore     = 15 * time.Minute    // the globe's tracks start this long before the snapshot
	trackAfter      = 12 * time.Hour      // ...and end this long after: GitHub runs the "hourly" job every 1 to 5 hours
	trackStep       = 30 * time.Second
	meanEarthKm     = 6371.0 // the globe's Earth: a sphere of the mean radius
)

// The status.json format. The page reads it; so does the next run, which adds
// the new beacons to the readings of the last one. That is how a stateless job
// (a GitHub Action, with no database) builds up the history the baseline rule
// needs.
type snapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Satellites  []snapSatellite `json:"satellites"`
	// For the globe on the page: each satellite's Track and the point under
	// the Sun, every TrackStepS seconds from TrackStart.
	TrackStart time.Time    `json:"track_start"`
	TrackStepS int          `json:"track_step_s"`
	Sun        [][2]float64 `json:"sun,omitempty"` // latitude, longitude (degrees)
}

type snapSatellite struct {
	Name            string         `json:"name"`
	NORAD           int            `json:"norad"`
	SunlitNow       *bool          `json:"sunlit_now,omitempty"`
	TLEEpoch        *time.Time     `json:"tle_epoch,omitempty"`
	TLE             string         `json:"tle,omitempty"` // the element set used: the fallback for the next run
	LastBeacon      *time.Time     `json:"last_beacon,omitempty"`
	StaleAfterHours float64        `json:"stale_after_hours"`
	Stale           bool           `json:"stale"`
	Error           string         `json:"error,omitempty"`
	Shadows         [][2]time.Time `json:"shadows,omitempty"` // when the satellite was in the Earth's shadow
	// Where it will be: latitude, longitude (degrees), altitude (km), and 1 in
	// sunlight or 0 in the shadow, from the same SGP4 and shadow model as the
	// rules. Made fresh each run, not merged.
	Track     [][4]float64  `json:"track,omitempty"`
	Batteries []snapBattery `json:"batteries"`
}

type snapBattery struct {
	Name      string          `json:"battery"`
	Readings  []snapReading   `json:"readings"` // oldest first
	Anomalies map[string]bool `json:"anomalies"`
}

type snapReading struct {
	ID     string    `json:"id"` // identifies the beacon and battery, to merge snapshots
	T      time.Time `json:"t"`
	MV     int       `json:"mv"`
	MA     *int      `json:"ma,omitempty"`
	Sunlit *bool     `json:"sunlit,omitempty"` // worked out again each run
}

// snapSource is what a snapshot needs from outside, so tests can replace it.
type snapSource struct {
	// frames returns up to `pages` pages of the newest frames of a satellite.
	// On an error it returns the frames it got before it.
	frames func(ctx context.Context, norad, pages int) ([]TelemetryFrame, error)
	// tle returns an element set as text: a name and two lines.
	tle func(ctx context.Context, norad int) (string, error)
	now func() time.Time
}

// buildSnapshot makes the next snapshot from the last one (or nil) and what the
// source returns now. A satellite that cannot be fetched keeps its old
// readings and says why; it does not stop the others.
func buildSnapshot(ctx context.Context, src snapSource, prev *snapshot, sats []satellite) *snapshot {
	out := &snapshot{GeneratedAt: src.now().UTC()}
	for _, sat := range sats {
		var old *snapSatellite
		if prev != nil {
			for i := range prev.Satellites {
				if prev.Satellites[i].NORAD == sat.NORAD {
					old = &prev.Satellites[i]
				}
			}
		}
		out.Satellites = append(out.Satellites, buildSatellite(ctx, src, sat, old))
	}
	addTracks(out)
	return out
}

// addTracks fills in where the satellites and the Sun will be, from the element
// set each satellite carries. One start time for all, so the page can step
// through them together.
func addTracks(snap *snapshot) {
	start := snap.GeneratedAt.Add(-trackBefore).Truncate(trackStep)
	n := int((trackBefore+trackAfter)/trackStep) + 1
	snap.TrackStart, snap.TrackStepS = start, int(trackStep/time.Second)
	snap.Sun = make([][2]float64, n)
	for i := range n {
		t := start.Add(time.Duration(i) * trackStep)
		lat, lon, _ := earthFixed(orbit.SunPosition(t), t)
		snap.Sun[i] = [2]float64{round2(lat), round2(lon)}
	}
	for i := range snap.Satellites {
		ss := &snap.Satellites[i]
		if ss.TLE == "" {
			continue
		}
		t, err := parseTLE(ss.TLE)
		if err != nil {
			continue
		}
		m, err := newOrbitModel(t)
		if err != nil {
			continue
		}
		var track [][4]float64
		for j := range n {
			at := start.Add(time.Duration(j) * trackStep)
			r, _, err := m.sat.PropagateTo(at)
			if err != nil {
				break // a decayed orbit: the track ends here
			}
			lat, lon, alt := earthFixed(r, at)
			lit := 0.0
			if orbit.InSunlight(r, at) {
				lit = 1
			}
			track = append(track, [4]float64{round2(lat), round2(lon), math.Round(alt), lit})
		}
		ss.Track = track
	}
}

// earthFixed turns a position in SGP4's frame (km) into latitude and longitude
// (degrees) over a spherical Earth, as the globe draws it, and altitude (km).
func earthFixed(r [3]float64, t time.Time) (lat, lon, alt float64) {
	d := math.Sqrt(r[0]*r[0] + r[1]*r[1] + r[2]*r[2])
	lat = math.Asin(r[2]/d) * 180 / math.Pi
	lon = math.Remainder(math.Atan2(r[1], r[0])-sgp4.GMST(t), 2*math.Pi) * 180 / math.Pi
	return lat, lon, d - meanEarthKm
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func buildSatellite(ctx context.Context, src snapSource, sat satellite, old *snapSatellite) snapSatellite {
	now := src.now().UTC()
	ss := snapSatellite{Name: sat.Name, NORAD: sat.NORAD, StaleAfterHours: sat.StaleAfter.Hours()}

	// The readings so far, by battery and id.
	series := map[string]map[string]snapReading{}
	add := func(battery string, r snapReading) {
		if series[battery] == nil {
			series[battery] = map[string]snapReading{}
		}
		if _, ok := series[battery][r.ID]; !ok {
			series[battery][r.ID] = r
		}
	}
	pages := firstPages
	if old != nil {
		for _, b := range old.Batteries {
			for _, r := range b.Readings {
				add(b.Name, r)
			}
		}
		if len(old.Batteries) > 0 {
			pages = nextPages
		}
	}

	frames, err := src.frames(ctx, sat.NORAD, pages)
	if err != nil {
		ss.Error = "could not fetch the newest beacons: " + err.Error()
	}
	for _, f := range frames {
		raw, herr := hex.DecodeString(f.Frame)
		if herr != nil {
			continue
		}
		d, derr := sat.Decode(raw)
		if derr != nil {
			continue // not a battery beacon, damaged, or implausible: dropped, as in store
		}
		for _, b := range d.Batteries {
			add(b.Battery, snapReading{ID: readingID(raw, b.Battery), T: f.Timestamp.UTC(), MV: b.VoltageMV, MA: b.CurrentMA})
		}
	}

	var model *orbitModel
	text, terr := src.tle(ctx, sat.NORAD)
	if terr != nil && old != nil && old.TLE != "" {
		// Orbit data could not be fetched: use what the last run had. It ages by
		// about a kilometre a day, and tleMaxAge switches the lighting rules off
		// when it is too old.
		ss.Error = joinErrors(ss.Error, "using the orbit data of the previous run: "+terr.Error())
		text, terr = old.TLE, nil
	}
	if terr != nil {
		ss.Error = joinErrors(ss.Error, "no orbit data: "+terr.Error())
	} else if t, perr := parseTLE(text); perr != nil {
		ss.Error = joinErrors(ss.Error, "bad orbit data: "+perr.Error())
	} else if m, merr := newOrbitModel(t); merr != nil {
		ss.Error = joinErrors(ss.Error, "bad orbit data: "+merr.Error())
	} else {
		model = m
		epoch := t.Epoch
		ss.TLEEpoch = &epoch
		ss.TLE = text
		if lit, lerr := m.sunlit(now); lerr == nil {
			ss.SunlitNow = &lit
		}
		ss.Shadows = shadowIntervals(m, now.Add(-shadowWindow), now)
	}
	// Without a recent TLE "sunlit" is a guess, so the rules that need it stay off (as in watch).
	trustLight := model != nil && now.Sub(model.tle.Epoch) < tleMaxAge

	names := make([]string, 0, len(series))
	for n := range series {
		names = append(names, n)
	}
	slices.Sort(names)
	var newest time.Time
	for _, name := range names {
		var rs []snapReading
		for _, r := range series[name] {
			if now.Sub(r.T) <= snapshotHistory {
				rs = append(rs, r)
			}
		}
		slices.SortFunc(rs, func(a, b snapReading) int { return a.T.Compare(b.T) })
		if len(rs) == 0 {
			continue
		}
		pts := make([]Point, len(rs)) // newest first, as the rules want
		for i := range rs {
			rs[i].Sunlit = nil
			if model != nil {
				if lit, lerr := model.sunlit(rs[i].T); lerr == nil {
					rs[i].Sunlit = &lit
				}
			}
			p := Point{At: rs[i].T, VoltageMV: rs[i].MV, CurrentMA: rs[i].MA, Sunlit: rs[i].Sunlit != nil && *rs[i].Sunlit}
			pts[len(rs)-1-i] = p
		}
		if rs[len(rs)-1].T.After(newest) {
			newest = rs[len(rs)-1].T
		}
		ss.Batteries = append(ss.Batteries, snapBattery{Name: name, Readings: rs, Anomalies: map[string]bool{
			"discharging_in_sunlight": trustLight && dischargingInSunlight(pts),
			"voltage_below_baseline":  trustLight && voltageBelowBaseline(pts),
			"low_voltage":             lowVoltage(pts, sat.LowMV),
		}})
	}
	if !newest.IsZero() {
		ss.LastBeacon = &newest
		ss.Stale = now.Sub(newest) > sat.StaleAfter
	} else {
		ss.Stale = true
	}
	return ss
}

// shadowIntervals walks from..to in 30-second steps and returns the stretches
// in which the satellite is in the Earth's shadow.
func shadowIntervals(m *orbitModel, from, to time.Time) [][2]time.Time {
	var out [][2]time.Time
	var start time.Time
	in := false
	for t := from; !t.After(to); t = t.Add(30 * time.Second) {
		lit, err := m.sunlit(t)
		if err != nil {
			continue
		}
		switch {
		case !lit && !in:
			start, in = t, true
		case lit && in:
			out = append(out, [2]time.Time{start, t})
			in = false
		}
	}
	if in {
		out = append(out, [2]time.Time{start, to})
	}
	return out
}

// readingID names a beacon's battery reading. The same beacon heard by two
// ground stations has the same bytes and so the same id.
func readingID(frame []byte, battery string) string {
	h := sha256.Sum256(append(slices.Clone(frame), battery...))
	return hex.EncodeToString(h[:6])
}

func joinErrors(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// readPrevious reads the last snapshot. A missing or unreadable file is not an
// error: the first run, or a damaged file, simply starts the history again.
func readPrevious(path string) *snapshot {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("previous snapshot: %v", err)
		}
		return nil
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("previous snapshot unreadable, starting again: %v", err)
		return nil
	}
	return &s
}

// writeSnapshot writes status.json and the page into dir.
func writeSnapshot(dir string, s *snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "status.json"), b, 0o644); err != nil {
		return err
	}
	return fs.WalkDir(site, "site", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := site.ReadFile(path)
		if err != nil {
			return err
		}
		out := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, "site/")))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}

// liveSource reads from the real APIs. CelesTrak asks for the same element set
// at most every couple of hours, so one that is younger than that is read from
// tleCache instead of fetched again.
func liveSource(token, tleCache string) snapSource {
	return snapSource{
		now: time.Now,
		frames: func(ctx context.Context, norad, pages int) ([]TelemetryFrame, error) {
			var all []TelemetryFrame
			url := telemetryURL(norad)
			for p := 0; p < pages && url != ""; p++ {
				var frames []TelemetryFrame
				var next string
				if err := retryRateLimited(ctx, func() (err error) {
					frames, next, err = fetchTelemetryPage(ctx, url, token)
					return err
				}); err != nil {
					return all, err
				}
				all = append(all, frames...)
				url = next
				if url != "" {
					if err := sleep(ctx, telemetryPageDelay); err != nil {
						return all, err
					}
				}
			}
			return all, nil
		},
		tle: func(ctx context.Context, norad int) (string, error) {
			path := ""
			if tleCache != "" {
				path = filepath.Join(tleCache, fmt.Sprintf("%d.tle", norad))
				if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < 2*time.Hour {
					if b, err := os.ReadFile(path); err == nil {
						return string(b), nil
					}
				}
			}
			text, err := fetchTLETextAny(ctx, norad)
			if err == nil && path != "" {
				os.MkdirAll(tleCache, 0o755)
				os.WriteFile(path, []byte(text), 0o644)
			}
			return text, err
		},
	}
}

// runSnapshot builds the snapshot and writes the site.
func runSnapshot(ctx context.Context, token, out, previous, tleCache string, norads []int) error {
	var sats []satellite
	for _, n := range norads {
		s, ok := lookupSatellite(n)
		if !ok {
			return fmt.Errorf("no decoder for satellite %d", n)
		}
		sats = append(sats, s)
	}
	prev := readPrevious(previous)
	snap := buildSnapshot(ctx, liveSource(token, tleCache), prev, sats)
	for _, s := range snap.Satellites {
		n := 0
		for _, b := range s.Batteries {
			n += len(b.Readings)
		}
		log.Printf("snapshot: %s: %d readings, last beacon %v, stale %v, error %q", s.Name, n, s.LastBeacon, s.Stale, s.Error)
	}
	return writeSnapshot(out, snap)
}
