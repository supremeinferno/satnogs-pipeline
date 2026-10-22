package main

import (
	"context"
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

// runFetch polls the API every interval and publishes what is new to the
// queue. It knows nothing about the database; storing is the other program's
// job.
func runFetch(ctx context.Context, amqpURL string, interval time.Duration, norad, maxPages int) error {
	pub := &publisher{url: amqpURL}
	defer pub.close()
	seen := map[int]fingerprint{} // lost on restart; the store side ignores repeats

	for {
		err := poll(ctx, pub, observationsURL(norad, time.Now()), maxPages, seen)
		if err == nil {
			lastPoll.SetToCurrentTime()
		} else if ctx.Err() == nil {
			pollFailures.Inc()
		}
		if interval == 0 {
			return err
		}
		if err != nil && ctx.Err() == nil {
			// One bad poll (API down, broker restarting) must not stop the
			// service: log it and try again at the next tick.
			log.Printf("poll failed, retrying in %s: %v", interval, err)
		}
		if sleep(ctx, interval) != nil {
			log.Print("shutting down")
			return nil
		}
	}
}

// poll reads pages from the newest observation backwards and publishes the
// fresh ones. It stops at maxPages, at the last page, or at the first page
// with nothing fresh: the API lists newest first, so everything older was
// published before. After downtime, the pages in between are what it catches
// up on.
func poll(ctx context.Context, pub *publisher, url string, maxPages int, seen map[int]fingerprint) error {
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
			if err := pub.publish(ctx, o); err != nil {
				return err
			}
			seen[o.ID] = fingerprint{o.Status, len(o.DemodData)} // only after the broker confirmed
			sent++
		}
		// A page of only future passes is empty after keep: not a sign that
		// we've caught up, so keep going.
		if len(obs) > 0 && len(news) == 0 {
			break
		}
		url = next
		if url != "" {
			if err := sleep(ctx, time.Second); err != nil { // be polite to the API
				return err
			}
		}
	}
	log.Printf("polled %d page(s): %d observations seen, %d published", pages, got, sent)
	return nil
}
