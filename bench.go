package main

import (
	"context"
	"fmt"
	"time"
)

// benchObservation is a made-up observation that ends at `at`. stored_at minus
// ended_at in the database is then the time it spent in the pipeline.
func benchObservation(id int, at time.Time) Observation {
	return Observation{ID: id, NoradID: 25544, Start: at.Add(-5 * time.Minute), End: at, Status: "good"}
}

// runBenchPublish publishes n made-up observations to the observations queue:
// as fast as the broker confirms them if rate is 0, otherwise rate per second.
// Used by scripts/bench.sh, which runs it in the same Docker network as
// PostgreSQL, so both read the same clock.
func runBenchPublish(ctx context.Context, amqpURL string, n int, rate float64, firstID int) error {
	pub := &publisher{url: amqpURL}
	defer pub.close()

	start := time.Now()
	for i := 0; i < n; i++ {
		if rate > 0 {
			// The i-th message is due at start + i/rate; a late one goes at once.
			due := start.Add(time.Duration(float64(i) / rate * float64(time.Second)))
			if err := sleep(ctx, time.Until(due)); err != nil {
				return err
			}
		}
		if err := pub.publish(ctx, queueObservations, benchObservation(firstID+i, time.Now().UTC())); err != nil {
			return err
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("published %d observations in %.2fs (%.0f per second)\n", n, elapsed.Seconds(), float64(n)/elapsed.Seconds())
	return nil
}
