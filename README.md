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
docker compose up -d --build         # PostgreSQL, RabbitMQ, fetch (every 5 minutes), store, Prometheus, Grafana
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

## Monitoring

Both programs serve Prometheus metrics on `:9100/metrics`. `docker compose up` also starts Prometheus (http://localhost:9090) and Grafana (http://localhost:3000, no login, local use only) with a ready dashboard, *SatNOGS pipeline*:

| Panel | What it tells you |
|---|---|
| Queue depth | Messages waiting for `store`. Rises when `store` is slow or stopped, drains when it catches up. |
| Dead queue | Messages that could never be stored. Should stay at 0. |
| Published / handled per second | Throughput of `fetch` and `store`, the latter split into stored, unchanged, rejected and failed. |
| Time since last successful poll | Should never be far above the poll interval. The thing to alert on. |
| SatNOGS API requests | Successes and errors. |
| Write to Postgres p95, pass end to stored p95 | Latency of the database write, and how long after a pass ended it reached the database (mostly ground stations uploading late). |

Counters are per process and restart from zero. The first poll after an empty start backfills a day or more of passes, which inflates the "pass end to stored" delay until real traffic dominates it.

## Alerts

Prometheus evaluates the rules in `monitoring/alerts.yml` and hands firing alerts to Alertmanager (http://localhost:9093), which posts them to the `notify` service. Today `notify` only logs one line per alert (`docker compose logs notify`); `alertHandler` in `notify.go` is where a Telegram or e-mail message would be sent.

| Alert | Fires when |
|---|---|
| ServiceDown | `fetch` or `store` can't be scraped for 1 minute |
| PollStale | no successful SatNOGS poll for 15 minutes (the interval is 5) |
| APIErrors | more than 3 failed API requests in 10 minutes |
| QueueBacklog | more than 100 messages waiting for 2 minutes |
| DeadQueueNotEmpty | any message in the dead queue |
| StoreFailing | `store` failed to write a message in the last 5 minutes |

A stopped program exposes no metrics, so "time since last poll" can't notice it; that is what ServiceDown (`up == 0`) is for.

## Kubernetes

`k8s/` runs PostgreSQL, RabbitMQ, `fetch` and `store` on a local [kind](https://kind.sigs.k8s.io/) cluster (Kubernetes inside Docker). Needs Docker, kind and kubectl.

```sh
k8s/deploy.sh                                   # create the cluster, build and load the image, apply k8s/
kubectl --context kind-satnogs -n satnogs get pods
kubectl --context kind-satnogs -n satnogs scale deploy/store --replicas=4
kind delete cluster --name satnogs              # remove everything
```

- PostgreSQL and RabbitMQ are StatefulSets with a persistent volume each; `fetch` and `store` are Deployments (`store` runs 2 replicas that share the queue).
- Nothing waits for anything: Kubernetes starts all pods at once and the programs retry until Postgres and RabbitMQ answer. `fetch` retries a failed poll after 30 seconds, not after a whole interval.
- Checked on kind: deleting a `store` pod brings up a replacement, scaling `store` to 4 gives 4 consumers on the queue, deleting `postgres-0` keeps all rows.
- Not in the cluster: Prometheus, Grafana and Alertmanager (the programs already serve `/metrics` on :9100), and any Ingress. Secrets are plain development values in `k8s/05-secrets.yaml`.

## Notes on the data

- The API lists newest first, and for a busy satellite the first pages are all scheduled (future) passes, so requests carry a `start__lt=<now>` cutoff.
- Many recent observations are `unknown` until someone vets them. Observations older than the first page with no changes are not re-checked, so a late status change on an old row is missed.
- Frame data (`demoddata`) are links to files, not JSON. The telemetry endpoint on db.satnogs.org needs an account token.
