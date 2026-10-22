package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runStore reads observations and telemetry frames from their queues and
// writes them to Postgres. Each queue has its own consumer, so a problem with
// one doesn't hold up the other, and each reconnects to the broker whenever
// its connection drops.
func runStore(ctx context.Context, dsn, amqpURL string) error {
	// A pool, not one connection: it reconnects by itself if Postgres restarts.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	var wg sync.WaitGroup
	for queue, handle := range map[string]func(context.Context, []byte) error{
		queueObservations: observationHandler(pool),
		queueTelemetry:    telemetryHandler(pool),
	} {
		wg.Go(func() {
			for ctx.Err() == nil {
				if err := consume(ctx, amqpURL, queue, handle); err != nil && ctx.Err() == nil {
					log.Printf("%s: consumer stopped, reconnecting in 5s: %v", queue, err)
					sleep(ctx, 5*time.Second)
				}
			}
		})
	}
	wg.Wait()
	log.Print("shutting down")
	return nil
}

// validate rejects observations that decode but make no sense.
func validate(o Observation) error {
	if o.ID <= 0 || o.NoradID <= 0 || o.Status == "" || o.End.Before(o.Start) {
		return errors.New("incomplete or inconsistent observation")
	}
	return nil
}

func observationHandler(pool *pgxpool.Pool) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var o Observation
		err := json.Unmarshal(body, &o)
		if err == nil {
			err = validate(o)
		}
		if err != nil {
			stored.WithLabelValues("rejected").Inc()
			return permanent(err)
		}

		start := time.Now()
		changed, err := save(ctx, pool, []Observation{o})
		if err != nil {
			stored.WithLabelValues("failed").Inc()
			return err
		}
		storeSeconds.Observe(time.Since(start).Seconds())
		if changed == 0 {
			stored.WithLabelValues("unchanged").Inc()
			return nil
		}
		stored.WithLabelValues("stored").Inc()
		ingestDelay.Observe(time.Since(o.End).Seconds())
		return nil
	}
}

// save writes the observations in one transaction and returns how many rows
// were inserted or changed.
//
// An observation we already have is skipped, unless its status or frame count
// moved: a pass is "unknown" until someone vets it, and its frames can appear
// after the pass ended. Running save twice on the same data changes nothing.
func save(ctx context.Context, pool *pgxpool.Pool, obs []Observation) (changed int64, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	for _, o := range obs {
		tag, err := tx.Exec(ctx, `
			INSERT INTO observations (id, norad_id, started_at, ended_at, status, frame_files)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO UPDATE
			SET status = EXCLUDED.status, frame_files = EXCLUDED.frame_files
			WHERE observations.status IS DISTINCT FROM EXCLUDED.status
			   OR observations.frame_files IS DISTINCT FROM EXCLUDED.frame_files`,
			o.ID, o.NoradID, o.Start, o.End, o.Status, len(o.DemodData))
		if err != nil {
			return 0, fmt.Errorf("save observation %d: %w", o.ID, err)
		}
		changed += tag.RowsAffected()
	}
	return changed, tx.Commit(ctx)
}
