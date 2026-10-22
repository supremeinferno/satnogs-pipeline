// satnogs-pipeline polls the SatNOGS Network API for observations and stores
// them in PostgreSQL.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	interval := flag.Duration("interval", 5*time.Minute, "time between polls; 0 = poll once and exit")
	norad := flag.Int("norad", 25544, "only this satellite (0 = all)")
	maxPages := flag.Int("max-pages", 10, "most pages (25 observations each) to read per poll")
	flag.Parse()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://satnogs:satnogs@localhost:5432/satnogs" // docker-compose.yml
	}

	// Ctrl-C or SIGTERM (what Docker and Kubernetes send) cancels ctx, which
	// stops any request or query in flight and ends the loop below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A pool, not one connection: it reconnects by itself if Postgres restarts.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	for {
		err := poll(ctx, pool, observationsURL(*norad, time.Now()), *maxPages)
		if *interval == 0 {
			if err != nil {
				log.Fatal(err)
			}
			return
		}
		if err != nil && ctx.Err() == nil {
			// One bad poll (API down, database restarting) must not stop the
			// service: log it and try again at the next tick.
			log.Printf("poll failed, retrying in %s: %v", *interval, err)
		}
		if sleep(ctx, *interval) != nil {
			log.Print("shutting down")
			return
		}
	}
}

// poll reads pages from the newest observation backwards and saves them.
// It stops at maxPages, at the last page, or at the first page that brings
// nothing new: the API lists newest first, so everything older is already
// stored. After downtime, the pages in between are what it catches up on.
func poll(ctx context.Context, pool *pgxpool.Pool, url string, maxPages int) error {
	var seen int
	var changed int64
	pages := 0
	for url != "" && pages < maxPages {
		obs, next, err := fetchPage(ctx, url)
		if err != nil {
			return err
		}
		pages++
		obs = keep(obs)
		n, err := save(ctx, pool, obs)
		if err != nil {
			return err
		}
		seen += len(obs)
		changed += n
		// A page of only future passes is empty after keep: not a sign that
		// we've caught up, so keep going.
		if len(obs) > 0 && n == 0 {
			break
		}
		url = next
		if url != "" {
			if err := sleep(ctx, time.Second); err != nil { // be polite to the API
				return err
			}
		}
	}
	log.Printf("polled %d page(s): %d observations seen, %d new or updated", pages, seen, changed)
	return nil
}

// sleep waits for d, or returns ctx's error as soon as ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
