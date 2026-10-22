package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/supremeinferno/orbitscreen/pkg/sgp4"
	"github.com/supremeinferno/orbitscreen/pkg/tle"
	"github.com/supremeinferno/satnogs-pipeline/internal/orbit"
)

// The alert rules, with the numbers they come from. They were set from three
// days of UWE-4 data (89 readings): a battery that is in sunlight is charging
// (positive current) almost all the time, but 5 of 41 sunlit readings showed a
// small discharge of 26 to 112 mA, none near the edge of the Earth's shadow
// and never two in a row. So one such reading means nothing; a run does.
const (
	// The newest dischargeRun readings taken in sunlight must all show a
	// current below dischargeMA, and span at least dischargeSpan, so that
	// several beacons of one pass don't count as several events.
	dischargeRun  = 3
	dischargeMA   = -50
	dischargeSpan = 30 * time.Minute

	// A lithium cell this low is in trouble in any light. The lowest value
	// seen was 3732 mV, in the shadow at -300 mA.
	lowVoltageMV = 3500

	// How far back to read, and how old a TLE may be before the sunlight
	// answer stops being trusted (it drifts by about a kilometre a day).
	watchWindow = 24 * time.Hour
	tleMaxAge   = 10 * 24 * time.Hour
	tleRefresh  = 6 * time.Hour
)

// Reading is a decoded beacon with the satellite's lighting at that moment.
type Reading struct {
	At           time.Time
	BattA, BattB Battery
	Sunlit       bool
}

func battA(r Reading) Battery { return r.BattA }
func battB(r Reading) Battery { return r.BattB }

// dischargingInSunlight is the "unexplained drop" rule. newestFirst is the
// readings, newest first. Readings in the shadow are skipped: discharging
// there is normal and says nothing about this rule.
func dischargingInSunlight(newestFirst []Reading, pick func(Reading) Battery) bool {
	var n int
	var newest, oldest time.Time
	for _, r := range newestFirst {
		if !r.Sunlit {
			continue
		}
		if pick(r).CurrentMA >= dischargeMA {
			return false
		}
		if n == 0 {
			newest = r.At
		}
		oldest = r.At
		if n++; n == dischargeRun {
			return newest.Sub(oldest) >= dischargeSpan
		}
	}
	return false
}

// lowVoltage reports whether the newest reading is below lowVoltageMV.
func lowVoltage(newestFirst []Reading, pick func(Reading) Battery) bool {
	return len(newestFirst) > 0 && pick(newestFirst[0]).VoltageMV < lowVoltageMV
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

// watcher keeps the current element set and refreshes it now and then.
type watcher struct {
	pool    *pgxpool.Pool
	norad   int
	mu      sync.Mutex
	model   *orbitModel
	fetched time.Time
}

// currentModel returns the orbit model, fetching a new TLE when the one in
// hand is older than tleRefresh. If the fetch fails it keeps the old one.
func (w *watcher) currentModel(ctx context.Context) *orbitModel {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.model == nil || time.Since(w.fetched) > tleRefresh {
		t, err := fetchTLE(ctx, w.norad)
		if err == nil {
			var m *orbitModel
			if m, err = newOrbitModel(t); err == nil {
				w.model, w.fetched = m, time.Now()
			}
		}
		if err != nil {
			log.Printf("could not refresh the TLE (keeping %s): %v", w.describeTLE(), err)
		}
	}
	return w.model
}

func (w *watcher) describeTLE() string {
	if w.model == nil {
		return "none"
	}
	return "epoch " + w.model.tle.Epoch.Format(time.RFC3339)
}

// loadReadings reads the last watchWindow of beacons, newest first, and adds
// the lighting at each one.
func (w *watcher) loadReadings(ctx context.Context, m *orbitModel) ([]Reading, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT received_at, batt_a_mv, batt_a_ma, batt_a_temp_c, batt_a_pct,
		       batt_b_mv, batt_b_ma, batt_b_temp_c, batt_b_pct
		FROM telemetry
		WHERE norad_id = $1 AND received_at > $2
		ORDER BY received_at DESC`, w.norad, time.Now().Add(-watchWindow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reading
	for rows.Next() {
		var r Reading
		if err := rows.Scan(&r.At,
			&r.BattA.VoltageMV, &r.BattA.CurrentMA, &r.BattA.TempC, &r.BattA.ChargePct,
			&r.BattB.VoltageMV, &r.BattB.CurrentMA, &r.BattB.TempC, &r.BattB.ChargePct); err != nil {
			return nil, err
		}
		if m != nil {
			if lit, err := m.sunlit(r.At); err == nil {
				r.Sunlit = lit
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// check runs one evaluation and publishes the result as metrics.
func (w *watcher) check(ctx context.Context) error {
	m := w.currentModel(ctx)
	rs, err := w.loadReadings(ctx, m)
	if err != nil {
		return err
	}

	if m != nil {
		tleAge.Set(time.Since(m.tle.Epoch).Seconds())
		if lit, err := m.sunlit(time.Now()); err == nil {
			satelliteSunlit.Set(b2f(lit))
		}
	}
	if len(rs) == 0 {
		// Report "at least a whole window old" rather than keeping the last,
		// smaller value: this is exactly when the stale-data alert must fire.
		telemetryAge.Set(watchWindow.Seconds())
		log.Printf("watch: no readings in the last %s", watchWindow)
		return nil
	}
	telemetryAge.Set(time.Since(rs[0].At).Seconds())

	// Without a recent TLE "sunlit" is a guess, so don't raise that alert.
	trustLight := m != nil && time.Since(m.tle.Epoch) < tleMaxAge

	for _, b := range []struct {
		name string
		pick func(Reading) Battery
	}{{"a", battA}, {"b", battB}} {
		cur := b.pick(rs[0])
		batteryMV.WithLabelValues(b.name).Set(float64(cur.VoltageMV))
		batteryMA.WithLabelValues(b.name).Set(float64(cur.CurrentMA))
		batteryAnomaly.WithLabelValues(b.name, "discharging_in_sunlight").Set(b2f(trustLight && dischargingInSunlight(rs, b.pick)))
		batteryAnomaly.WithLabelValues(b.name, "low_voltage").Set(b2f(lowVoltage(rs, b.pick)))
	}
	log.Printf("watch: %d readings, newest %s ago, satellite sunlit now: %v, TLE %s",
		len(rs), time.Since(rs[0].At).Round(time.Minute), fmt.Sprint(m != nil && sunlitNow(m)), w.describeTLE())
	return nil
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
func runWatch(ctx context.Context, dsn string, interval time.Duration, norad int) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	w := &watcher{pool: pool, norad: norad}
	return pollLoop(ctx, interval, w.check)
}
