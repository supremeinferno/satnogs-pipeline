#!/usr/bin/env bash
# Breaks the pipeline on purpose and checks that it recovers.
#
# It starts postgres, rabbitmq and store from docker-compose.yml, feeds made-up
# observations straight into the queue (the SatNOGS API is never called) and
# after each break checks what ended up in the database and the queues. Made-up
# observations have ids from 9,000,000,000 up and are deleted at the end.
#
#   scripts/chaos.sh              all scenarios
#   scripts/chaos.sh --no-build   do not rebuild the images first
#
# A wait that times out stops the script (later scenarios depend on the state
# the earlier ones leave), after cleaning up. A check that simply does not hold
# is counted and the run goes on. The exit status is 0 only if everything passed.
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_ID=9000000000
failures=0

log()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
pass() { printf '   \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '   \033[31mFAIL\033[0m %s\n' "$*"; failures=$((failures + 1)); }

dc()      { docker compose "$@"; }
psql_q()  { dc exec -T postgres psql -U satnogs -tAc "$1"; }
db_count() { psql_q "SELECT count(*) FROM observations WHERE id >= $BASE_ID"; }

# queue_depth NAME: messages in a queue, ready and unacknowledged.
queue_depth() {
  dc exec -T rabbitmq rabbitmqctl list_queues name messages 2>/dev/null \
    | awk -v q="$1" '$1 == q { print $2 }' | head -n 1
}

# publish BODY QUEUE: puts a message on a queue through the management API.
publish() {
  local payload=${1//\\/\\\\}; payload=${payload//\"/\\\"}
  curl -fsS -u satnogs:satnogs -H 'content-type: application/json' \
    -X POST "http://localhost:15672/api/exchanges/%2F/amq.default/publish" \
    -d "{\"properties\":{\"delivery_mode\":2},\"routing_key\":\"$2\",\"payload\":\"$payload\",\"payload_encoding\":\"string\"}" \
    >/dev/null
}

# publish_obs N FIRST_ID: N valid observations with consecutive ids.
publish_obs() {
  local i
  for ((i = 0; i < $1; i++)); do
    publish "{\"id\":$(($2 + i)),\"norad_cat_id\":25544,\"start\":\"2026-10-22T08:00:00Z\",\"end\":\"2026-10-22T08:05:00Z\",\"status\":\"good\",\"demoddata\":[]}" observations
  done
}

# wait_for DESCRIPTION SECONDS COMMAND...: runs the command until it succeeds.
wait_for() {
  local what=$1 limit=$2 t=0; shift 2
  until "$@" >/dev/null 2>&1; do
    ((t >= limit)) && { fail "$what (waited ${limit}s)"; return 1; }
    sleep 1; t=$((t + 1))
  done
  pass "$what (${t}s)"
}

count_is()    { [[ "$(db_count)" == "$1" ]]; }
depth_is()    { [[ "$(queue_depth "$1")" == "$2" ]]; }
rabbit_up()   { curl -fsS -u satnogs:satnogs http://localhost:15672/api/overview; }
postgres_up() { dc exec -T postgres pg_isready -U satnogs; }
store_up()    { [[ "$(dc ps --status running --services | grep -cx store)" == 1 ]]; }

cleanup() {
  local code=$?
  dc start postgres rabbitmq store >/dev/null 2>&1 || true
  wait_for "postgres back for cleanup" 60 postgres_up >/dev/null 2>&1 || true
  psql_q "DELETE FROM observations WHERE id >= $BASE_ID" >/dev/null 2>&1 || true
  for q in observations observations.dead; do
    curl -fsS -u satnogs:satnogs -X DELETE "http://localhost:15672/api/queues/%2F/$q/contents" >/dev/null 2>&1 || true
  done
  if ((code != 0)); then
    printf '\n\033[31mFAILED: %d check(s) did not hold; the run stopped there.\033[0m\n' "$failures"
  fi
}
trap cleanup EXIT

# ---- start ------------------------------------------------------------------
log "Starting postgres, rabbitmq and store"
if [[ "${1:-}" == "--no-build" ]]; then dc up -d postgres rabbitmq store >/dev/null
else dc up -d --build postgres rabbitmq store >/dev/null; fi
wait_for "postgres answers" 90 postgres_up
wait_for "rabbitmq answers" 120 rabbit_up
wait_for "store is running" 30 store_up
sleep 5 # let store connect to the broker
psql_q "DELETE FROM observations WHERE id >= $BASE_ID" >/dev/null

# ---- 1. normal ---------------------------------------------------------------
log "1. Normal operation: 5 observations reach the database"
publish_obs 5 $((BASE_ID + 100))
wait_for "5 rows stored" 30 count_is 5
wait_for "queue is empty" 10 depth_is observations 0

# ---- 2. store down -----------------------------------------------------------
log "2. store is stopped: messages wait in the queue, then are stored"
dc stop store >/dev/null
publish_obs 20 $((BASE_ID + 200))
wait_for "20 messages wait in the queue" 20 depth_is observations 20
[[ "$(db_count)" == 5 ]] && pass "nothing was stored while store was down" || fail "rows appeared without store"
dc start store >/dev/null
wait_for "all 25 rows stored" 60 count_is 25
wait_for "queue drained" 10 depth_is observations 0

# ---- 3. duplicates -----------------------------------------------------------
log "3. The same observations again: no duplicate rows"
publish_obs 5 $((BASE_ID + 100))
wait_for "queue drained" 30 depth_is observations 0
count_is 25 && pass "still 25 rows" || fail "expected 25 rows, got $(db_count)"

# ---- 4. rabbitmq restarts ----------------------------------------------------
log "4. rabbitmq restarts: store reconnects by itself"
dc restart rabbitmq >/dev/null
wait_for "rabbitmq answers again" 120 rabbit_up
sleep 8 # store retries every 5 seconds
publish_obs 3 $((BASE_ID + 400))
wait_for "3 new rows stored after the restart" 60 count_is 28

# ---- 5. postgres down --------------------------------------------------------
log "5. postgres is stopped: messages are requeued, nothing is lost"
dc stop postgres >/dev/null
publish_obs 4 $((BASE_ID + 500))
sleep 6
[[ "$(queue_depth observations)" -ge 1 ]] && pass "messages stay in the queue while the database is down" \
  || fail "queue empty although the database is down"
dc start postgres >/dev/null
wait_for "postgres answers again" 90 postgres_up
wait_for "all 4 stored once postgres is back" 90 count_is 32
wait_for "queue drained" 20 depth_is observations 0

# ---- 6. poison message -------------------------------------------------------
log "6. A message that can never work goes to the dead queue and blocks nothing"
publish "this is not json" observations
publish_obs 2 $((BASE_ID + 600))
wait_for "the bad message is in the dead queue" 30 depth_is observations.dead 1
wait_for "the good messages behind it were stored" 30 count_is 34
wait_for "main queue is empty" 10 depth_is observations 0

# ---- result ------------------------------------------------------------------
echo
if ((failures == 0)); then printf '\033[32mAll checks passed.\033[0m\n'
else printf '\033[31m%d check(s) failed.\033[0m\n' "$failures"; exit 1; fi
