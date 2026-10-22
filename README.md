# satnogs-pipeline

A small backend that collects satellite observations from the [SatNOGS Network](https://network.satnogs.org/) and stores them in PostgreSQL. SatNOGS is a worldwide network of volunteer ground stations; each *observation* is one station listening to one satellite during one pass.

Work in progress. The goal is a telemetry pipeline (fetch, decode, queue, store, alert) in the spirit of a small mission control.

## How it fits together

```
SatNOGS API --> fetch --> RabbitMQ queue --> store --> PostgreSQL
                              |
                              +--> dead queue (messages that can never be stored)
```

Two programs in one binary, so a slow or dead database never stops the fetching, and the other way round.

- `fetch` polls the observations API on an interval, follows its pagination, skips passes that haven't happened yet, and publishes each new or changed observation. It waits for the broker's confirmation (publisher confirms) before counting a message as sent.
- `store` reads the queue and writes to Postgres. A message is acknowledged only after it is saved, so a crash loses nothing; an undecodable or invalid message goes to the dead queue instead of blocking the rest.
- Running either twice never duplicates a row; a row is updated only when its status or frame count changed.
- Both survive their dependencies going down (API, broker, database) and shut down cleanly on Ctrl-C or SIGTERM.

## Run it

Needs Docker. Go 1.26+ is needed only for development.

```sh
docker compose up -d --build         # PostgreSQL, RabbitMQ, fetch (every 5 minutes) and store
docker compose logs -f fetch store
docker compose down                  # stop; add -v to also delete the data
```

For development, run only the infrastructure in Docker and the programs from source:

```sh
docker compose up -d postgres rabbitmq
go run . store                       # terminal 1: queue -> Postgres
go run . fetch -interval 0           # terminal 2: poll once and exit
go test ./...
```

`fetch` flags: `-interval` (0 = once), `-norad` (satellite catalog number, 0 = all; default 25544, the ISS), `-max-pages` (25 observations per page). Connections come from `DATABASE_URL` and `AMQP_URL`, defaulting to the ones in `docker-compose.yml`. The RabbitMQ management UI is at http://localhost:15672 (user and password `satnogs`).

## Notes on the data

- The API lists newest first, and for a busy satellite the first pages are all scheduled (future) passes, so requests carry a `start__lt=<now>` cutoff.
- Many recent observations are `unknown` until someone vets them. Observations older than the first page with no changes are not re-checked, so a late status change on an old row is missed.
- Frame data (`demoddata`) are links to files, not JSON. The telemetry endpoint on db.satnogs.org needs an account token.
