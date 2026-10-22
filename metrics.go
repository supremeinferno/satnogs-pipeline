package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics are numbers the program keeps about itself; Prometheus reads them
// from /metrics every few seconds and Grafana draws them. Counters only go
// up (a graph shows their rate), gauges go up and down, histograms count
// values into buckets so a percentile can be computed.
var (
	apiRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pipeline_api_requests_total",
		Help: "SatNOGS API page requests, by outcome (ok or error).",
	}, []string{"outcome"})

	pollFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pipeline_poll_failures_total",
		Help: "Polls that ended with an error.",
	})

	lastPoll = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pipeline_last_poll_success_timestamp_seconds",
		Help: "Unix time of the last poll that finished without error.",
	})

	published = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pipeline_published_total",
		Help: "Messages the broker confirmed, by queue.",
	}, []string{"queue"})

	telemetryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pipeline_telemetry_total",
		Help: "Telemetry frames handled by store, by result: decoded (new reading), duplicate (already stored), not_beacon, bad_checksum, rejected (sent to the dead queue), failed (will be retried).",
	}, []string{"result"})

	stored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pipeline_stored_total",
		Help: "Messages handled by store, by result: stored (new or changed row), unchanged, rejected (sent to the dead queue), failed (will be retried).",
	}, []string{"result"})

	storeSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "pipeline_store_duration_seconds",
		Help:    "Time to write one observation to Postgres.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12), // 1 ms .. 2 s
	})

	// What watch reports about the satellite. The alerts are rules on these.
	batteryMV = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pipeline_battery_voltage_millivolts",
		Help: "Voltage in the newest beacon, by battery.",
	}, []string{"battery"})

	batteryMA = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pipeline_battery_current_milliamps",
		Help: "Current in the newest beacon, by battery (positive charging).",
	}, []string{"battery"})

	batteryAnomaly = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pipeline_battery_anomaly",
		Help: "1 while a battery rule is violated: discharging_in_sunlight, low_voltage.",
	}, []string{"battery", "rule"})

	satelliteSunlit = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pipeline_satellite_in_sunlight",
		Help: "1 if SGP4 puts the satellite in sunlight right now, 0 in the Earth's shadow.",
	})

	telemetryAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pipeline_telemetry_age_seconds",
		Help: "Seconds since the newest stored beacon was received.",
	})

	tleAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pipeline_tle_age_seconds",
		Help: "Age of the element set used for sunlight, measured from its epoch.",
	})

	// How long after a pass ended its data reached the database. Dominated by
	// how late ground stations upload, which the pipeline cannot control.
	ingestDelay = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "pipeline_ingest_delay_seconds",
		Help:    "Seconds from the end of a pass to the moment it was stored.",
		Buckets: []float64{60, 300, 900, 1800, 3600, 7200, 21600, 86400},
	})
)

// serveMetrics exposes /metrics on addr until ctx is cancelled.
func serveMetrics(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metrics server: %v", err)
		}
	}()
}
