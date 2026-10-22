package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
