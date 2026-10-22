package main

import (
	"context"
	"errors"
	"log"
	"time"
)

// fingerprint is what can change about an observation after we first see it.
type fingerprint struct {
	status string
	frames int
}

// fresh returns the observations that are new, or whose status or frame
// count differ from what we last published.
func fresh(obs []Observation, seen map[int]fingerprint) []Observation {
	var out []Observation
	for _, o := range obs {
		if fp, ok := seen[o.ID]; !ok || fp != (fingerprint{o.Status, len(o.DemodData)}) {
			out = append(out, o)
		}
	}
	return out
}

// retryAfterFailure is how long a fetcher waits after a failed poll.
const retryAfterFailure = 30 * time.Second

// pageDelay is the pause between two page requests, to be polite to the APIs
// (the DB one rate-limits quickly). A variable so tests can set it to 0.
var pageDelay = 2 * time.Second

// pollLoop calls poll now and then every interval until ctx is cancelled (or
// once, if interval is 0).
func pollLoop(ctx context.Context, interval time.Duration, poll func(context.Context) error) error {
	for {
		err := poll(ctx)
		if err == nil {
			lastPoll.SetToCurrentTime()
		} else if ctx.Err() == nil {
			pollFailures.Inc()
		}
		if interval == 0 {
			return err
		}
		wait := interval
		if err != nil && ctx.Err() == nil {
			// One bad poll (API down, broker restarting) must not stop the
			// service. Retry soon rather than after a whole interval: at
			// startup a dependency is often just a few seconds late. If the
			// API said to slow down, wait as long as it asked.
			wait = min(interval, retryAfterFailure)
			var rl rateLimitError
			if errors.As(err, &rl) {
				wait = max(wait, rl.after)
			}
			log.Printf("poll failed, retrying in %s: %v", wait, err)
		}
		if sleep(ctx, wait) != nil {
			log.Print("shutting down")
			return nil
		}
	}
}

// runFetch polls the observations API every interval and publishes what is
// new to the queue. It knows nothing about the database; storing is the
// other program's job.
func runFetch(ctx context.Context, amqpURL string, interval time.Duration, norad, maxPages int) error {
	pub := &publisher{url: amqpURL}
	defer pub.close()
	seen := map[int]fingerprint{} // lost on restart; the store side ignores repeats
	var partial bool

	return pollLoop(ctx, interval, func(ctx context.Context) error {
		return poll(ctx, pub.publish, observationsURL(norad, time.Now()), maxPages, seen, &partial)
	})
}

// poll reads pages from the newest observation backwards and publishes the
// fresh ones. It stops at maxPages, at the last page, or at the first page
// with nothing fresh: the API lists newest first, so everything older was
// published before. After downtime, the pages in between are what it catches
// up on.
func poll(ctx context.Context, publish publishFunc, url string, maxPages int, seen map[int]fingerprint, partial *bool) (err error) {
	defer func() {
		if err != nil {
			*partial = true // see pollTelemetry
		}
	}()
	var got, sent int
	pages := 0
	for url != "" && pages < maxPages {
		obs, next, err := fetchPage(ctx, url)
		if err != nil {
			return err
		}
		pages++
		obs = keep(obs)
		got += len(obs)
		news := fresh(obs, seen)
		for _, o := range news {
			if err := publish(ctx, queueObservations, o); err != nil {
				return err
			}
			seen[o.ID] = fingerprint{o.Status, len(o.DemodData)} // only after the broker confirmed
			sent++
		}
		// A page of only future passes is empty after keep: not a sign that
		// we've caught up, so keep going.
		if len(obs) > 0 && len(news) == 0 && !*partial {
			break
		}
		url = next
		if url != "" {
			if err := sleep(ctx, pageDelay); err != nil {
				return err
			}
		}
	}
	*partial = false // reached the end, or the page limit, without failing
	log.Printf("polled %d page(s): %d observations seen, %d published", pages, got, sent)
	return nil
}
