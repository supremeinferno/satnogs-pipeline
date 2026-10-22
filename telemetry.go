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

// uwe4NORAD is UWE-4, the first satellite the pipeline decoded and the one
// whose battery current is checked against the lighting.
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

// frameKey identifies a frame: the satellite, its bytes and when it was heard.
func frameKey(f TelemetryFrame) string {
	return fmt.Sprintf("%d:%s@%s", f.NoradID, f.Frame, f.Timestamp.Format(time.RFC3339))
}

// freshFrames returns the frames not published before.
func freshFrames(frames []TelemetryFrame, seen map[string]bool) []TelemetryFrame {
	var out []TelemetryFrame
	for _, f := range frames {
		if !seen[frameKey(f)] {
			out = append(out, f)
		}
	}
	return out
}

// runFetchTelemetry polls the telemetry API for each satellite in turn and
// publishes the frames it hasn't published before. A satellite that fails
// (rate limit, API error) does not stop the others; the poll reports every
// failure at its end.
func runFetchTelemetry(ctx context.Context, amqpURL, token string, interval time.Duration, norads []int, maxPages int) error {
	pub := &publisher{url: amqpURL}
	defer pub.close()
	seen := map[string]bool{} // lost on restart; the store side ignores repeats
	partial := map[int]*bool{}
	for _, n := range norads {
		partial[n] = new(bool)
	}

	return pollLoop(ctx, interval, func(ctx context.Context) error {
		var errs []error
		for _, n := range norads {
			if err := pollTelemetry(ctx, pub.publish, token, telemetryURL(n), maxPages, seen, partial[n]); err != nil {
				errs = append(errs, fmt.Errorf("satellite %d: %w", n, err))
			}
			if sleep(ctx, telemetryPageDelay) != nil { // the DB API rate-limits quickly
				break
			}
		}
		return errors.Join(errs...)
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
			seen[frameKey(f)] = true // only after the broker confirmed
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

// decodeTelemetryMessage turns a queue message into the frame and what the
// satellite's decoder reads from it. Messages that can never work (bad JSON,
// bad hex, a satellite with no decoder) come back as permanent errors;
// ErrNotBeacon, ErrBadChecksum and ErrImplausible are passed through for the
// caller to count and drop.
func decodeTelemetryMessage(body []byte) (TelemetryFrame, satellite, []byte, Decoded, error) {
	var tf TelemetryFrame
	if err := json.Unmarshal(body, &tf); err != nil {
		return tf, satellite{}, nil, Decoded{}, permanent(err)
	}
	sat, ok := lookupSatellite(tf.NoradID)
	if !ok {
		return tf, satellite{}, nil, Decoded{}, permanent(fmt.Errorf("no decoder for satellite %d", tf.NoradID))
	}
	// observation_id and station_id are 0 for frames that did not come
	// through SatNOGS Network; those are real frames all the same.
	if tf.Timestamp.IsZero() {
		return tf, sat, nil, Decoded{}, permanent(errors.New("frame without a time"))
	}
	frame, err := hex.DecodeString(tf.Frame)
	if err != nil {
		return tf, sat, nil, Decoded{}, permanent(fmt.Errorf("frame is not hex: %w", err))
	}
	d, err := sat.Decode(frame)
	return tf, sat, frame, d, err
}

func telemetryHandler(pool *pgxpool.Pool) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		tf, sat, frame, d, err := decodeTelemetryMessage(body)
		name := sat.Name
		if name == "" {
			name = "unknown"
		}
		count := func(result string) { telemetryTotal.WithLabelValues(result, name).Inc() }
		switch {
		case errors.Is(err, ErrNotBeacon):
			count("not_beacon")
			return nil // radio traffic that isn't a battery beacon: nothing wrong, drop it
		case errors.Is(err, ErrBadChecksum):
			count("bad_checksum")
			return nil // damaged on the way down: drop it
		case errors.Is(err, ErrImplausible):
			count("implausible")
			return nil // parsed, but not a believable battery: drop it
		case err != nil:
			count("rejected")
			return err
		}
		inserted, err := saveReadings(ctx, pool, tf, frame, d)
		if err != nil {
			count("failed")
			return err
		}
		if inserted == 0 {
			count("duplicate")
		} else {
			count("decoded")
		}
		return nil
	}
}

// saveReadings stores one row per battery in one transaction and reports how
// many rows it added: 0 if another station already reported the same frame.
func saveReadings(ctx context.Context, pool *pgxpool.Pool, tf TelemetryFrame, frame []byte, d Decoded) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var added int64
	for _, b := range d.Batteries {
		tag, err := tx.Exec(ctx, `
			INSERT INTO battery_readings (norad_id, battery, frame, received_at, observation_id, station_id,
				voltage_mv, current_ma, temp_c, charge_pct)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (norad_id, frame, battery) DO NOTHING`,
			tf.NoradID, b.Battery, frame, tf.Timestamp, nullIfZero(tf.ObservationID), nullIfZero(tf.StationID),
			b.VoltageMV, b.CurrentMA, b.TempC, b.ChargePct)
		if err != nil {
			return 0, fmt.Errorf("save reading: %w", err)
		}
		added += tag.RowsAffected()
	}
	return added, tx.Commit(ctx)
}

// nullIfZero stores 0 as NULL: "unknown", not an id.
func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
