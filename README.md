# satnogs-pipeline

A small backend that collects satellite observations from the [SatNOGS Network](https://network.satnogs.org/) and stores them in PostgreSQL. SatNOGS is a worldwide network of volunteer ground stations; each *observation* is one station listening to one satellite during one pass.

Work in progress. The goal is a telemetry pipeline (fetch, decode, queue, store, alert) in the spirit of a small mission control.

## What works now

- Polls the SatNOGS observations API on an interval and follows its pagination.
- Skips passes that haven't happened yet.
- Stores observations in Postgres. Running it again never duplicates a row; a row is updated only when its status or frame count changed.
- Keeps running when the API or the database goes down, and shuts down cleanly on Ctrl-C or SIGTERM.

## Run it

Needs Go 1.26+ and Docker.

```sh
docker compose up -d                 # PostgreSQL, with the schema applied
go run . -interval 0                 # poll once and exit
go run . -interval 5m                # poll every 5 minutes
go test ./...
```

Flags: `-interval` (0 = once), `-norad` (satellite catalog number, 0 = all; default 25544, the ISS), `-max-pages` (25 observations per page). The database is `DATABASE_URL`, defaulting to the one in `docker-compose.yml`.

## Notes on the data

- The API lists newest first, and for a busy satellite the first pages are all scheduled (future) passes, so requests carry a `start__lt=<now>` cutoff.
- Many recent observations are `unknown` until someone vets them. Observations older than the first page with no changes are not re-checked, so a late status change on an old row is missed.
- Frame data (`demoddata`) are links to files, not JSON. The telemetry endpoint on db.satnogs.org needs an account token.
