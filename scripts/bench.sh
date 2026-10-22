#!/usr/bin/env bash
# Measures the store side of the pipeline on Docker Compose, with made-up
# observations (the SatNOGS API is never called):
#
#   throughput  how long N queued messages take to reach PostgreSQL, with 1, 2
#               and 4 store replicas
#   latency     how long a message spends in the pipeline (from being published
#               to being stored) while it arrives at a steady rate
#
# The delay is stored_at - ended_at in the database, where ended_at is the time
# of publishing. bench-publish runs in the same Docker network as PostgreSQL, so
# both read the same clock.
#
#   scripts/bench.sh            N=20000 messages for the throughput runs
#   N=5000 scripts/bench.sh     a quicker run
set -euo pipefail
cd "$(dirname "$0")/.."

N=${N:-20000}
FIRST=9100000000
DURATION=15 # seconds of each latency run

dc()     { docker compose "$@"; }
psql_q() { dc exec -T postgres psql -U satnogs -tAc "$1"; }
log()    { printf '\n\033[1m== %s\033[0m\n' "$*"; }

depth() {
  dc exec -T rabbitmq rabbitmqctl list_queues name messages 2>/dev/null | awk '$1 == "observations" { print $2 }' | head -n 1
}
wait_until() { # DESCRIPTION SECONDS COMMAND...
  local what=$1 limit=$2 t=0; shift 2
  until "$@" >/dev/null 2>&1; do
    ((t >= limit)) && { echo "timed out: $what" >&2; exit 1; }
    sleep 0.5; t=$((t + 1))
  done
}
rows_at_least() { [[ "$(psql_q "SELECT count(*) FROM observations WHERE id >= $FIRST")" -ge "$1" ]]; }
depth_is()      { [[ "$(depth)" == "$1" ]]; }

# reset: no store running, an empty queue, no bench rows. store goes first: a
# purge leaves messages it holds unacknowledged, and they would come back.
reset() {
  dc stop store >/dev/null 2>&1 || true
  curl -fsS -u satnogs:satnogs -X DELETE "http://localhost:15672/api/queues/%2F/observations/contents" >/dev/null 2>&1 || true
  psql_q "DELETE FROM observations WHERE id >= $FIRST" >/dev/null
}
trap 'reset' EXIT

publish() { dc run --rm --no-deps -T store bench-publish "$@" 2>&1 | grep published; }

log "Machine"
echo "$(uname -m), Docker: $(docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes of memory')"

dc up -d postgres rabbitmq >/dev/null
dc build store >/dev/null
wait_until "postgres" 60 dc exec -T postgres pg_isready -U satnogs
wait_until "rabbitmq" 120 curl -fsS -u satnogs:satnogs http://localhost:15672/api/overview

# ---- throughput ---------------------------------------------------------------
log "Throughput: $N queued messages, store started afterwards"
printf '| store replicas | messages | seconds to store them all | per second |\n|---|---|---|---|\n'
for replicas in 1 2 4; do
  reset
  publish -n "$N" -rate 0 -first-id "$FIRST" >/dev/null
  wait_until "queue holds $N" 60 depth_is "$N"
  dc up -d --no-deps --scale store="$replicas" store >/dev/null 2>&1
  wait_until "all $N stored" 600 rows_at_least "$N"
  span=$(psql_q "SELECT extract(epoch FROM max(stored_at) - min(stored_at)) FROM observations WHERE id >= $FIRST")
  awk -v r="$replicas" -v n="$N" -v s="$span" 'BEGIN { printf "| %d | %d | %.1f | %.0f |\n", r, n, s, n / s }'
done

# ---- latency ------------------------------------------------------------------
log "Latency: one store replica, messages arriving at a steady rate for ${DURATION}s"
printf '| messages per second | p50 ms | p95 ms | p99 ms | max ms |\n|---|---|---|---|---|\n'
for rate in 100 500 1000 2000; do
  reset
  dc up -d --no-deps --scale store=1 store >/dev/null 2>&1
  sleep 6 # let store connect
  n=$((rate * DURATION))
  publish -n "$n" -rate "$rate" -first-id "$FIRST" >/dev/null
  wait_until "all $n stored" 300 rows_at_least "$n"
  psql_q "SELECT '| $rate | ' ||
      round((percentile_cont(0.50) WITHIN GROUP (ORDER BY extract(epoch FROM stored_at - ended_at)) * 1000)::numeric, 1) || ' | ' ||
      round((percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM stored_at - ended_at)) * 1000)::numeric, 1) || ' | ' ||
      round((percentile_cont(0.99) WITHIN GROUP (ORDER BY extract(epoch FROM stored_at - ended_at)) * 1000)::numeric, 1) || ' | ' ||
      round((max(extract(epoch FROM stored_at - ended_at)) * 1000)::numeric, 1) || ' |'
    FROM observations WHERE id >= $FIRST"
done
