// Command plotbattery draws UWE-4's battery A current against time as an SVG,
// with the Earth's shadow (from SGP4) shaded behind it. It is how the figure
// in the README is made, from the readings in the database:
//
//	go run ./cmd/plotbattery -out docs/img/battery-current.svg
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/supremeinferno/orbitscreen/pkg/sgp4"
	"github.com/supremeinferno/orbitscreen/pkg/tle"
	"github.com/supremeinferno/satnogs-pipeline/internal/orbit"
)

type point struct {
	at     time.Time
	mA     int
	sunlit bool
}

func main() {
	dsn := flag.String("dsn", "postgres://satnogs:satnogs@localhost:5432/satnogs", "Postgres connection string")
	tlePath := flag.String("tle", "testdata/uwe4.tle", "element set (name and two lines)")
	norad := flag.Int("norad", 43880, "satellite")
	out := flag.String("out", "docs/img/battery-current.svg", "output file")
	flag.Parse()

	m, err := loadModel(*tlePath)
	if err != nil {
		log.Fatal(err)
	}
	pts, err := loadPoints(context.Background(), *dsn, *norad, m)
	if err != nil {
		log.Fatal(err)
	}
	if len(pts) < 2 {
		log.Fatalf("only %d readings in the database", len(pts))
	}
	// For the README's claim that the sunlit discharges are not artefacts of the
	// shadow edge: how far each one is, in time, from a change of lighting.
	for _, p := range pts {
		if p.sunlit && p.mA < dischargeMA {
			fmt.Printf("sunlit, discharging: %s UTC %4d mA, nearest shadow edge %s\n",
				p.at.UTC().Format("2 Jan 15:04"), p.mA, nearestEdge(m, p))
		}
	}
	svg := draw(pts)
	if err := os.WriteFile(*out, []byte(svg), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d readings)\n", *out, len(pts))
}

func loadModel(path string) (*sgp4.Satellite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimRight(l, " \r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		return nil, fmt.Errorf("%s: want a name and two lines, got %d lines", path, len(lines))
	}
	el, err := tle.Parse(strings.TrimSpace(lines[0]), lines[1], lines[2])
	if err != nil {
		return nil, err
	}
	return sgp4.New(el)
}

// nearestEdge says how far from p the lighting changes, looking 30 minutes
// either way in 5-second steps.
func nearestEdge(m *sgp4.Satellite, p point) string {
	for d := 5 * time.Second; d <= 30*time.Minute; d += 5 * time.Second {
		for _, sign := range []time.Duration{-1, 1} {
			if lit, err := sunlit(m, p.at.Add(sign*d)); err == nil && lit != p.sunlit {
				return d.Round(time.Second).String()
			}
		}
	}
	return "more than 30m0s"
}

func sunlit(m *sgp4.Satellite, at time.Time) (bool, error) {
	r, _, err := m.PropagateTo(at)
	if err != nil {
		return false, err
	}
	return orbit.InSunlight(r, at), nil
}

func loadPoints(ctx context.Context, dsn string, norad int, m *sgp4.Satellite) ([]point, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT received_at, batt_a_ma FROM telemetry
		WHERE norad_id = $1 AND station_id IS DISTINCT FROM -1 ORDER BY received_at`, norad)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pts []point
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.at, &p.mA); err != nil {
			return nil, err
		}
		if p.sunlit, err = sunlit(m, p.at); err != nil {
			return nil, err
		}
		pts = append(pts, p)
	}
	return pts, rows.Err()
}

// Layout, in SVG user units.
const (
	width, height         = 760, 470
	left, right, top, bot = 64, 24, 96, 72
	yMin, yMax            = -400.0, 250.0
	dischargeMA           = -20 // a reading below this counts as discharging
)

// jitter spreads the dots of a panel sideways so they don't hide each other.
// It depends only on the time of the beacon, so the figure is the same every
// time it is drawn.
func jitter(t time.Time) float64 {
	h := uint64(t.UnixNano()) * 0x9E3779B97F4A7C15
	return float64(h>>40)/float64(1<<24) - 0.5 // -0.5 .. 0.5
}

func draw(pts []point) string {
	plotW := float64(width - left - right)
	plotH := float64(height - top - bot)
	panelW := plotW / 2
	y := func(mA float64) float64 { return top + plotH*(yMax-mA)/(yMax-yMin) }

	type tally struct{ n, charging, discharging, flat int }
	var shade, lit tally
	for _, p := range pts {
		c := &shade
		if p.sunlit {
			c = &lit
		}
		c.n++
		switch {
		case p.mA > -dischargeMA:
			c.charging++
		case p.mA < dischargeMA:
			c.discharging++
		default:
			c.flat++
		}
	}
	span := pts[len(pts)-1].at.Sub(pts[0].at)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-labelledby="t d">`, width, height)
	w(`<title id="t">UWE-4 battery A current, by lighting</title>`)
	w(`<desc id="d">%d beacons over %.1f days. Of %d in the Earth's shadow, %d were charging and %d discharging. Of %d in sunlight, %d were charging and %d discharging.</desc>`,
		len(pts), span.Hours()/24, shade.n, shade.charging, shade.discharging, lit.n, lit.charging, lit.discharging)
	w(`<style>
 svg{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
 .bg{fill:#fcfcfb}.shadow{fill:#e4e3de}.grid{stroke:#e4e3de;stroke-width:1}.zero{stroke:#8a8983;stroke-width:1.5;stroke-dasharray:5 4}
 .ink{fill:#0b0b0b}.muted{fill:#52514e}.dot{fill:#2a78d6;stroke:#fcfcfb;stroke-width:2}.ring{fill:none;stroke:#eb6834;stroke-width:2.5}
 @media (prefers-color-scheme:dark){.bg{fill:#1a1a19}.shadow{fill:#2b2b29}.grid{stroke:#333331}.zero{stroke:#8f8e86}
  .ink{fill:#f0efec}.muted{fill:#c3c2b7}.dot{fill:#3987e5;stroke:#1a1a19}.ring{stroke:#d95926}}
</style>`)
	w(`<rect class="bg" width="%d" height="%d" rx="6"/>`, width, height)
	w(`<text class="ink" x="%d" y="30" font-size="18" font-weight="600">UWE-4 battery A: it charges in sunlight, not in the Earth's shadow</text>`, left)
	w(`<text class="muted" x="%d" y="52" font-size="13">%d beacons heard by SatNOGS ground stations over %.1f days, %s to %s UTC.</text>`,
		left, len(pts), span.Hours()/24, pts[0].at.UTC().Format("2 Jan"), pts[len(pts)-1].at.UTC().Format("2 Jan"))
	w(`<text class="muted" x="%d" y="70" font-size="13">Lighting at each beacon comes from SGP4 and the Sun's position, not from the battery.</text>`, left)

	// The shadow panel has a shaded background, as the shadow it stands for.
	w(`<rect class="shadow" x="%d" y="%d" width="%.1f" height="%.1f"/>`, left, top, panelW, plotH)
	for _, v := range []float64{-400, -200, 200} {
		w(`<line class="grid" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, left, width-right, y(v), y(v))
	}
	for _, v := range []float64{-400, -200, 0, 200} {
		label := fmt.Sprintf("%+d", int(v))
		if v == 0 {
			label = "0"
		}
		w(`<text class="muted" x="%d" y="%.1f" font-size="12" text-anchor="end">%s</text>`, left-8, y(v)+4, label)
	}
	w(`<line class="zero" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, left, width-right, y(0), y(0))
	w(`<text class="muted" x="18" y="%d" font-size="12" transform="rotate(-90 18 %d)" text-anchor="middle">current, mA  (+ charging, − discharging)</text>`, top+int(plotH/2), top+int(plotH/2))

	for _, p := range pts {
		cx := float64(left) + panelW/2 + jitter(p.at)*panelW*0.8
		if p.sunlit {
			cx += panelW
		}
		cy := y(math.Max(math.Min(float64(p.mA), yMax), yMin))
		state := "in the shadow"
		if p.sunlit {
			state = "in sunlight"
		}
		if p.sunlit && p.mA < dischargeMA {
			w(`<circle class="ring" cx="%.1f" cy="%.1f" r="9"/>`, cx, cy)
		}
		w(`<circle class="dot" cx="%.1f" cy="%.1f" r="4.5"><title>%s UTC, %s: %d mA</title></circle>`, cx, cy, p.at.UTC().Format("2 Jan 15:04"), state, p.mA)
	}

	// Panel labels with the counts under them.
	for i, t := range []struct {
		name string
		c    tally
	}{{"In the Earth's shadow", shade}, {"In sunlight", lit}} {
		cx := float64(left) + panelW*float64(i) + panelW/2
		w(`<text class="ink" x="%.1f" y="%d" font-size="14" font-weight="600" text-anchor="middle">%s (%d beacons)</text>`, cx, height-bot+22, t.name, t.c.n)
		w(`<text class="muted" x="%.1f" y="%d" font-size="12" text-anchor="middle">charging %d · discharging %d · flat %d</text>`, cx, height-bot+40, t.c.charging, t.c.discharging, t.c.flat)
	}
	w(`<circle class="dot" cx="%d" cy="%d" r="4.5"/><text class="ink" x="%d" y="%d" font-size="12">one beacon</text>`, left+4, height-10, left+16, height-6)
	w(`<circle class="dot" cx="%d" cy="%d" r="4.5"/><circle class="ring" cx="%d" cy="%d" r="9"/><text class="ink" x="%d" y="%d" font-size="12">in sunlight but discharging (%d)</text>`,
		left+112, height-10, left+112, height-10, left+130, height-6, lit.discharging)
	w(`<text class="muted" x="%d" y="%d" font-size="11" text-anchor="end">flat: within 20 mA of zero</text>`, width-right, height-6)
	w(`</svg>`)
	return b.String()
}
