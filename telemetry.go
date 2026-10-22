package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// uwe4NORAD is the satellite the pipeline has a decoder for.
const uwe4NORAD = 43880

// TelemetryFrame is one radio frame a ground station received, as the
// SatNOGS DB telemetry API returns it. It is also the message on the queue.
type TelemetryFrame struct {
	NoradID       int       `json:"norad_cat_id"`
	Frame         string    `json:"frame"` // hex
	Timestamp     time.Time `json:"timestamp"`
	ObservationID int       `json:"observation_id"`
	StationID     int       `json:"station_id"`
}

func telemetryURL(norad int) string {
	return fmt.Sprintf("https://db.satnogs.org/api/telemetry/?format=json&satellite=%d", norad)
}

// fetchTelemetryPage reads one page of frames, newest first. Unlike the
// observations API, this one puts the next page's URL in the body. The DB API
// needs the account token.
func fetchTelemetryPage(ctx context.Context, url, token string) (frames []TelemetryFrame, next string, err error) {
	defer func() {
		outcome := "ok"
		var rl rateLimitError
		switch {
		case errors.As(err, &rl):
			outcome = "rate_limited"
		case err != nil:
			outcome = "error"
		}
		apiRequests.WithLabelValues(outcome).Inc()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "satnogs-pipeline (learning project)")
	req.Header.Set("Authorization", "Token "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, "", fmt.Errorf("get %s: %w", url, err)
	}
	var page struct {
		Next    string           `json:"next"`
		Results []TelemetryFrame `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", url, err)
	}
	return page.Results, page.Next, nil
}

// freshFrames returns the frames not published before. A frame is identified
// by its bytes and the time it was heard.
func freshFrames(frames []TelemetryFrame, seen map[string]bool) []TelemetryFrame {
	var out []TelemetryFrame
	for _, f := range frames {
		if !seen[f.Frame+"@"+f.Timestamp.Format(time.RFC3339)] {
			out = append(out, f)
		}
	}
	return out
}

// runFetchTelemetry polls the telemetry API for one satellite and publishes
// the frames it hasn't published before.
func runFetchTelemetry(ctx context.Context, amqpURL, token string, interval time.Duration, norad, maxPages int) error {
	pub := &publisher{url: amqpURL}
	defer pub.close()
	seen := map[string]bool{} // lost on restart; the store side ignores repeats
	var partial bool

	return pollLoop(ctx, interval, func(ctx context.Context) error {
		return pollTelemetry(ctx, pub.publish, token, telemetryURL(norad), maxPages, seen, &partial)
	})
}

// publishFunc is what the pollers need from a publisher.
type publishFunc func(ctx context.Context, queue string, v any) error

// pollTelemetry reads pages newest first and stops at maxPages, at the last
// page, or at the first page with nothing new (see poll). *partial remembers
// that an earlier poll failed part-way: the pages it never reached would
// look "already seen" from the top, so the next poll reads on regardless.
func pollTelemetry(ctx context.Context, publish publishFunc, token, url string, maxPages int, seen map[string]bool, partial *bool) (err error) {
	defer func() {
		if err != nil {
			*partial = true
		}
	}()
	var got, sent int
	pages := 0
	for url != "" && pages < maxPages {
		var frames []TelemetryFrame
		var next string
		if err := retryRateLimited(ctx, func() (err error) {
			frames, next, err = fetchTelemetryPage(ctx, url, token)
			return err
		}); err != nil {
			return err
		}
		pages++
		got += len(frames)
		news := freshFrames(frames, seen)
		for _, f := range news {
			if err := publish(ctx, queueTelemetry, f); err != nil {
				return err
			}
			seen[f.Frame+"@"+f.Timestamp.Format(time.RFC3339)] = true // only after the broker confirmed
			sent++
		}
		if len(frames) > 0 && len(news) == 0 && !*partial {
			break
		}
		url = next
		if url != "" {
			if err := sleep(ctx, telemetryPageDelay); err != nil {
				return err
			}
		}
	}
	*partial = false // reached the end, or the page limit, without failing
	log.Printf("telemetry: polled %d page(s): %d frames seen, %d published", pages, got, sent)
	return nil
}

// decodeTelemetryMessage turns a queue message into the frame and its
// reading. Messages that can never work (bad JSON, bad hex, a satellite we
// have no decoder for) come back as permanent errors; ErrNotBeacon and
// ErrBadChecksum are passed through for the caller to count and drop.
func decodeTelemetryMessage(body []byte) (TelemetryFrame, []byte, UWE4, error) {
	var tf TelemetryFrame
	if err := json.Unmarshal(body, &tf); err != nil {
		return tf, nil, UWE4{}, permanent(err)
	}
	if tf.NoradID != uwe4NORAD {
		return tf, nil, UWE4{}, permanent(fmt.Errorf("no decoder for satellite %d", tf.NoradID))
	}
	// observation_id and station_id are 0 for frames that did not come
	// through SatNOGS Network; those are real frames all the same.
	if tf.Timestamp.IsZero() {
		return tf, nil, UWE4{}, permanent(errors.New("frame without a time"))
	}
	frame, err := hex.DecodeString(tf.Frame)
	if err != nil {
		return tf, nil, UWE4{}, permanent(fmt.Errorf("frame is not hex: %w", err))
	}
	r, err := DecodeUWE4(frame)
	return tf, frame, r, err
}

func telemetryHandler(pool *pgxpool.Pool) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		tf, frame, r, err := decodeTelemetryMessage(body)
		switch {
		case errors.Is(err, ErrNotBeacon):
			telemetryTotal.WithLabelValues("not_beacon").Inc()
			return nil // radio traffic that isn't a beacon: nothing wrong, drop it
		case errors.Is(err, ErrBadChecksum):
			telemetryTotal.WithLabelValues("bad_checksum").Inc()
			return nil // damaged on the way down: drop it
		case err != nil:
			telemetryTotal.WithLabelValues("rejected").Inc()
			return err
		}
		inserted, err := saveTelemetry(ctx, pool, tf, frame, r)
		if err != nil {
			telemetryTotal.WithLabelValues("failed").Inc()
			return err
		}
		if inserted == 0 {
			telemetryTotal.WithLabelValues("duplicate").Inc()
		} else {
			telemetryTotal.WithLabelValues("decoded").Inc()
		}
		return nil
	}
}

// saveTelemetry stores one reading and reports how many rows it added: 0 if
// another station already reported the same frame.
func saveTelemetry(ctx context.Context, pool *pgxpool.Pool, tf TelemetryFrame, frame []byte, r UWE4) (int64, error) {
	tag, err := pool.Exec(ctx, `
		INSERT INTO telemetry (norad_id, frame, received_at, observation_id, station_id,
			batt_a_mv, batt_a_ma, batt_a_temp_c, batt_a_pct,
			batt_b_mv, batt_b_ma, batt_b_temp_c, batt_b_pct,
			power_mw, obc_temp_c, uptime_s)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (norad_id, frame) DO NOTHING`,
		tf.NoradID, frame, tf.Timestamp, nullIfZero(tf.ObservationID), nullIfZero(tf.StationID),
		r.BattA.VoltageMV, r.BattA.CurrentMA, r.BattA.TempC, r.BattA.ChargePct,
		r.BattB.VoltageMV, r.BattB.CurrentMA, r.BattB.TempC, r.BattB.ChargePct,
		r.PowerMW, r.OBCTempC, r.UptimeS)
	if err != nil {
		return 0, fmt.Errorf("save telemetry frame: %w", err)
	}
	return tag.RowsAffected(), nil
}

// nullIfZero stores 0 as NULL: "unknown", not an id.
func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
