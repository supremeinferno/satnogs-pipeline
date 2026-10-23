package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/supremeinferno/orbitscreen/pkg/sgp4"
	"github.com/supremeinferno/orbitscreen/pkg/tle"
	"github.com/supremeinferno/satnogs-pipeline/internal/orbit"
)

// The battery rules, with the numbers they come from. UWE-4's were set from
// the first three days of its data (89 readings) and replayed over 137
// readings in 5.1 days: a battery in sunlight is charging (positive current)
// in 61 of 77 readings, but 8 sunlit readings showed a small discharge of 26 to
// 162 mA, none near the edge of the Earth's shadow. So one such reading means
// nothing; a run does. The rules that apply to the other satellites are
// described in the README, with how they were checked.
const (
	// The newest dischargeRun readings taken in sunlight must all show a
	// current below dischargeMA, and span at least minSpan, so that several
	// beacons of one pass don't count as several events. Only for batteries
	// whose current is known (and whose sign has been checked).
	dischargeRun = 3
	dischargeMA  = -50
	minSpan      = 30 * time.Minute

	// A sunlit battery whose voltage, as the median of its newest
	// dischargeRun sunlit readings, is below its own median over the earlier
	// sunlit readings by more than the larger of dropFloor of that median and
	// madFactor times the readings' own spread (median absolute deviation).
	// Comparing a battery with itself in the same light needs no unit, no
	// chemistry and no pack size, and the spread makes it forgiving of a
	// battery that normally varies a lot. The baseline must be at least
	// baselineMin readings over baselineSpan: replayed over real UWE-4 data,
	// a baseline of 8 readings gave 14 false alarms in 138 readings, because
	// its sunlit voltage sits at 4.20 V when charging and near 4.05 V when not,
	// and a short baseline sees only one of the two. With 40 readings over 3
	// days there were none. Until a satellite has that much history the rule
	// stays quiet.
	dropFloor    = 0.02
	madFactor    = 8
	baselineMin  = 40
	baselineSpan = 72 * time.Hour

	// How far back to read, and how old a TLE may be before the sunlight
	// answer stops being trusted (it drifts by about a kilometre a day).
	watchWindow = 8 * 24 * time.Hour
	tleMaxAge   = 10 * 24 * time.Hour
	tleRefresh  = 6 * time.Hour
)

// Point is one reading of one battery with the lighting at that moment.
type Point struct {
	At        time.Time
	VoltageMV int
	CurrentMA *int
	Sunlit    bool
}

// dischargingInSunlight is the "unexplained drop" rule. pts are the readings,
// newest first. Readings in the shadow are skipped: discharging there is
// normal and says nothing about this rule. So are readings without a current.
func dischargingInSunlight(pts []Point) bool {
	var n int
	var newest, oldest time.Time
	for _, p := range pts {
		if !p.Sunlit || p.CurrentMA == nil {
			continue
		}
		if *p.CurrentMA >= dischargeMA {
			return false
		}
		if n == 0 {
			newest = p.At
		}
		oldest = p.At
		if n++; n == dischargeRun {
			return newest.Sub(oldest) >= minSpan
		}
	}
	return false
}

// lowVoltage reports whether the newest reading is below limitMV (0 = no limit).
func lowVoltage(pts []Point, limitMV int) bool {
	return limitMV > 0 && len(pts) > 0 && pts[0].VoltageMV < limitMV
}

// voltageBelowBaseline is the rule that needs no current: in sunlight, the
// battery voltage fell clearly below where the same battery usually is in
// sunlight. pts are newest first.
func voltageBelowBaseline(pts []Point) bool {
	var recent, older []int
	var newest, oldest, oldestBase time.Time
	for _, p := range pts {
		if !p.Sunlit {
			continue
		}
		if len(recent) < dischargeRun {
			if len(recent) == 0 {
				newest = p.At
			}
			oldest = p.At
			recent = append(recent, p.VoltageMV)
			continue
		}
		older = append(older, p.VoltageMV)
		oldestBase = p.At
	}
	if len(recent) < dischargeRun || newest.Sub(oldest) < minSpan {
		return false
	}
	if len(older) < baselineMin || oldest.Sub(oldestBase) < baselineSpan {
		return false // not enough history yet to know what is normal
	}
	base := float64(median(older))
	return base-float64(median(recent)) > max(dropFloor*base, madFactor*float64(mad(older)))
}

// mad is the median absolute deviation: how far readings typically are from their median.
func mad(v []int) int {
	m := median(v)
	d := make([]int, len(v))
	for i, x := range v {
		d[i] = max(x-m, m-x)
	}
	return median(d)
}

func median(v []int) int {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

// orbitModel turns a TLE into answers about lighting.
type orbitModel struct {
	sat *sgp4.Satellite
	tle *tle.TLE
}

func newOrbitModel(t *tle.TLE) (*orbitModel, error) {
	sat, err := sgp4.New(t)
	if err != nil {
		return nil, err
	}
	return &orbitModel{sat: sat, tle: t}, nil
}

func (m *orbitModel) sunlit(at time.Time) (bool, error) {
	r, _, err := m.sat.PropagateTo(at)
	if err != nil {
		return false, err
	}
	return orbit.InSunlight(r, at), nil
}

// watcher keeps an element set for every satellite and refreshes them now and then.
type watcher struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	models  map[int]*orbitModel
	fetched map[int]time.Time
}

// currentModel returns the orbit model of a satellite, fetching a new TLE when
// the one in hand is older than tleRefresh. If the fetch fails it keeps the old one.
func (w *watcher) currentModel(ctx context.Context, norad int) *orbitModel {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.models == nil {
		w.models, w.fetched = map[int]*orbitModel{}, map[int]time.Time{}
	}
	m := w.models[norad]
	if m == nil || time.Since(w.fetched[norad]) > tleRefresh {
		t, err := fetchTLEAny(ctx, norad)
		if err == nil {
			var nm *orbitModel
			if nm, err = newOrbitModel(t); err == nil {
				m, w.models[norad], w.fetched[norad] = nm, nm, time.Now()
			}
		}
		if err != nil {
			log.Printf("could not refresh the TLE of %d (keeping %s): %v", norad, describeTLE(m), err)
		}
		sleep(ctx, time.Second) // CelesTrak asks for few and slow requests
	}
	return m
}

func describeTLE(m *orbitModel) string {
	if m == nil {
		return "none"
	}
	return "epoch " + m.tle.Epoch.Format(time.RFC3339)
}

// series is one battery of one satellite.
type seriesKey struct {
	norad   int
	battery string
}

// loadReadings reads the last watchWindow of readings, newest first, grouped by battery.
func (w *watcher) loadReadings(ctx context.Context) (map[seriesKey][]Point, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT norad_id, battery, received_at, voltage_mv, current_ma
		FROM battery_readings
		WHERE received_at > $1
		ORDER BY norad_id, battery, received_at DESC`, time.Now().Add(-watchWindow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[seriesKey][]Point{}
	for rows.Next() {
		var k seriesKey
		var p Point
		if err := rows.Scan(&k.norad, &k.battery, &p.At, &p.VoltageMV, &p.CurrentMA); err != nil {
			return nil, err
		}
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

// check runs one evaluation of every satellite and publishes the result as metrics.
func (w *watcher) check(ctx context.Context) error {
	all, err := w.loadReadings(ctx)
	if err != nil {
		return err
	}
	for _, sat := range satellites {
		w.checkSatellite(ctx, sat, all)
	}
	return nil
}

func (w *watcher) checkSatellite(ctx context.Context, sat satellite, all map[seriesKey][]Point) {
	m := w.currentModel(ctx, sat.NORAD)
	staleAfter.WithLabelValues(sat.Name).Set(sat.StaleAfter.Seconds())
	if m != nil {
		tleAge.WithLabelValues(sat.Name).Set(time.Since(m.tle.Epoch).Seconds())
		if lit, err := m.sunlit(time.Now()); err == nil {
			satelliteSunlit.WithLabelValues(sat.Name).Set(b2f(lit))
		}
	}
	// Without a recent TLE "sunlit" is a guess, so no rule that needs it fires.
	trustLight := m != nil && time.Since(m.tle.Epoch) < tleMaxAge

	var newest time.Time
	var batteries []string
	for k, pts := range all {
		if k.norad != sat.NORAD {
			continue
		}
		batteries = append(batteries, k.battery)
		if m != nil {
			for i := range pts {
				if lit, err := m.sunlit(pts[i].At); err == nil {
					pts[i].Sunlit = lit
				}
			}
		}
		if pts[0].At.After(newest) {
			newest = pts[0].At
		}
		b := k.battery
		batteryMV.WithLabelValues(sat.Name, b).Set(float64(pts[0].VoltageMV))
		if pts[0].CurrentMA != nil {
			batteryMA.WithLabelValues(sat.Name, b).Set(float64(*pts[0].CurrentMA))
		}
		anomaly := func(rule string, on bool) { batteryAnomaly.WithLabelValues(sat.Name, b, rule).Set(b2f(on)) }
		anomaly("discharging_in_sunlight", trustLight && dischargingInSunlight(pts))
		anomaly("voltage_below_baseline", trustLight && voltageBelowBaseline(pts))
		anomaly("low_voltage", lowVoltage(pts, sat.LowMV))
	}
	if len(batteries) == 0 {
		// Report "at least a whole window old" rather than keeping the last,
		// smaller value: this is exactly when the stale-data alert must fire.
		telemetryAge.WithLabelValues(sat.Name).Set(watchWindow.Seconds())
		log.Printf("watch: %s: no readings in the last %s", sat.Name, watchWindow)
		return
	}
	telemetryAge.WithLabelValues(sat.Name).Set(time.Since(newest).Seconds())
	slices.Sort(batteries)
	log.Printf("watch: %s: batteries %v, newest %s ago, sunlit now: %v, TLE %s",
		sat.Name, batteries, time.Since(newest).Round(time.Minute), fmt.Sprint(m != nil && sunlitNow(m)), describeTLE(m))
}

func sunlitNow(m *orbitModel) bool {
	lit, _ := m.sunlit(time.Now())
	return lit
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// runWatch evaluates the battery rules every interval.
func runWatch(ctx context.Context, dsn string, interval time.Duration) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	w := &watcher{pool: pool}
	return pollLoop(ctx, interval, w.check)
}
