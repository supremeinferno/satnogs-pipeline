// satnogs-pipeline collects observations from the SatNOGS Network API and
// stores them in PostgreSQL, with RabbitMQ between the two steps:
//
//	satnogs-pipeline fetch   API -> queue
//	satnogs-pipeline fetch-telemetry   SatNOGS DB telemetry API -> queue (needs SATNOGS_DB_TOKEN)
//	satnogs-pipeline store   queues -> Postgres
//	satnogs-pipeline watch   battery rules with eclipse context -> metrics
//	satnogs-pipeline notify  receives Alertmanager's webhook
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "fetch" && os.Args[1] != "fetch-telemetry" && os.Args[1] != "store" && os.Args[1] != "watch" && os.Args[1] != "notify") {
		fmt.Fprintln(os.Stderr, "usage: satnogs-pipeline fetch|fetch-telemetry|store|watch|notify [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	interval := fs.Duration("interval", 5*time.Minute, "fetch: time between polls; 0 = poll once and exit")
	norad := fs.Int("norad", 25544, "fetch: only this satellite (0 = all)")
	satellite := fs.Int("satellite", uwe4NORAD, "fetch-telemetry: NORAD id of the satellite")
	maxPages := fs.Int("max-pages", 10, "fetch: most pages (25 observations each) to read per poll")
	listen := fs.String("listen", ":9200", "notify: address for the Alertmanager webhook")
	metricsAddr := fs.String("metrics", ":9100", "address for the Prometheus /metrics endpoint")
	fs.Parse(os.Args[2:])

	dsn := env("DATABASE_URL", "postgres://satnogs:satnogs@localhost:5432/satnogs") // docker-compose.yml
	amqpURL := env("AMQP_URL", "amqp://satnogs:satnogs@localhost:5672/")            // docker-compose.yml

	// Ctrl-C or SIGTERM (what Docker and Kubernetes send) cancels ctx, which
	// stops any request, query or wait in flight.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveMetrics(ctx, *metricsAddr)

	var err error
	switch cmd {
	case "fetch":
		err = runFetch(ctx, amqpURL, *interval, *norad, *maxPages)
	case "fetch-telemetry":
		token := os.Getenv("SATNOGS_DB_TOKEN")
		if token == "" {
			log.Fatal("SATNOGS_DB_TOKEN is not set (get one from your account on db.satnogs.org)")
		}
		err = runFetchTelemetry(ctx, amqpURL, token, *interval, *satellite, *maxPages)
	case "store":
		err = runStore(ctx, dsn, amqpURL)
	case "watch":
		err = runWatch(ctx, dsn, *interval, *satellite)
	case "notify":
		err = runNotify(ctx, *listen, os.Getenv("TELEGRAM_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"))
	}
	if err != nil {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
