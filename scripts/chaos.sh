#!/usr/bin/env bash
# Breaks the pipeline on purpose and checks that it recovers.
#
# It feeds made-up observations straight into the queue (the SatNOGS API is
# never called) and after each break checks what ended up in the database and
# the queues. Made-up observations have ids from 9,000,000,000 up and are
# deleted at the end.
#
#   scripts/chaos.sh                 Docker Compose: starts postgres, rabbitmq and store
#   scripts/chaos.sh --no-build      ...without rebuilding the images first
#   scripts/chaos.sh --k8s           the kind cluster made by k8s/deploy.sh (context kind-satnogs)
#
# A wait that times out stops the script (later scenarios depend on the state
# the earlier ones leave), after cleaning up. A check that simply does not hold
# is counted and the run goes on. The exit status is 0 only if everything passed.
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_ID=9000000000
failures=0
mode=compose build=yes
for arg in "$@"; do
  case $arg in
    --k8s) mode=k8s ;;
    --no-build) build=no ;;
    *) echo "usage: $0 [--k8s] [--no-build]" >&2; exit 2 ;;
  esac
done

log()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
pass() { printf '   \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '   \033[31mFAIL\033[0m %s\n' "$*"; failures=$((failures + 1)); }

# ---- backends: the same operations on Compose or on Kubernetes --------------

dc() { docker compose "$@"; }
k()  { kubectl --context kind-satnogs -n satnogs "$@"; }

if [[ $mode == compose ]]; then
  RABBIT_API=http://localhost:15672
  psql_q()    { dc exec -T postgres psql -U satnogs -tAc "$1"; }
  rabbitctl() { dc exec -T rabbitmq rabbitmqctl "$@" 2>/dev/null; }
  pf_ensure() { :; }
  postgres_up() { dc exec -T postgres pg_isready -U satnogs; }
  store_up()  { [[ "$(dc ps --status running --services | grep -cx store)" == 1 ]]; }
  svc_stop()  { dc stop "$1" >/dev/null; }
  svc_start() { dc start "$1" >/dev/null; }
  rabbit_restart() { dc restart rabbitmq >/dev/null; }
else
  # The RabbitMQ management API is reached through a port-forward, which dies
  # with the pod, so pf_ensure starts a fresh one whenever the API stops answering.
  RABBIT_API=http://localhost:25672
  pf_pid=""
  store_run=2   # as in k8s/30-app.yaml
  store_orig=0  # what the cluster had; put back at the end
  psql_q()    { k exec postgres-0 -- psql -U satnogs -tAc "$1"; }
  rabbitctl() { k exec rabbitmq-0 -- rabbitmqctl "$@" 2>/dev/null; }
  pf_ensure() {
    curl -fsS -m 3 -u satnogs:satnogs "$RABBIT_API/api/overview" >/dev/null 2>&1 && return 0
    [[ -n $pf_pid ]] && kill "$pf_pid" 2>/dev/null || true
    k port-forward svc/rabbitmq 25672:15672 >/dev/null 2>&1 &
    pf_pid=$!
    sleep 2
    curl -fsS -m 3 -u satnogs:satnogs "$RABBIT_API/api/overview" >/dev/null 2>&1
  }
  postgres_up() { k exec postgres-0 -- pg_isready -U satnogs; }
  store_up()  { [[ "$(k get deploy store -o jsonpath='{.status.readyReplicas}')" -ge 1 ]]; }
  svc_stop() {
    case $1 in
      store) k scale deploy/store --replicas=0 >/dev/null; k wait --for=delete pod -l app=store --timeout=90s >/dev/null ;;
      postgres) k scale statefulset/postgres --replicas=0 >/dev/null; k wait --for=delete pod/postgres-0 --timeout=90s >/dev/null ;;
    esac
  }
  svc_start() {
    case $1 in
      store) k scale deploy/store --replicas="$store_run" >/dev/null; k rollout status deploy/store --timeout=120s >/dev/null ;;
      postgres) k scale statefulset/postgres --replicas=1 >/dev/null; k rollout status statefulset/postgres --timeout=180s >/dev/null ;;
      rabbitmq) k rollout status statefulset/rabbitmq --timeout=180s >/dev/null ;;
    esac
  }
  rabbit_restart() { k rollout restart statefulset/rabbitmq >/dev/null; k rollout status statefulset/rabbitmq --timeout=180s >/dev/null; }
fi

db_count() { psql_q "SELECT count(*) FROM observations WHERE id >= $BASE_ID"; }

# queue_depth NAME: messages in a queue, ready and unacknowledged.
queue_depth() {
  rabbitctl list_queues name messages | awk -v q="$1" '$1 == q { print $2 }' | head -n 1
}

# publish BODY QUEUE: puts a message on a queue through the management API.
publish() {
  local payload=${1//\\/\\\\}; payload=${payload//\"/\\\"}
  pf_ensure
  curl -fsS -u satnogs:satnogs -H 'content-type: application/json' \
    -X POST "$RABBIT_API/api/exchanges/%2F/amq.default/publish" \
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
rabbit_up()   { pf_ensure && curl -fsS -m 3 -u satnogs:satnogs "$RABBIT_API/api/overview"; }

cleanup() {
  local code=$?
  # Order matters. Stop store first: a message it holds but has not acknowledged
  # is not touched by a purge and goes back to the queue when store stops, so a
  # purge while store runs can leave made-up observations that a later store
  # would write into the real database.
  svc_stop store >/dev/null 2>&1 || true
  if [[ $mode == compose ]]; then dc start postgres rabbitmq >/dev/null 2>&1 || true
  else svc_start postgres >/dev/null 2>&1 || true; svc_start rabbitmq >/dev/null 2>&1 || true; fi
  wait_for "postgres back for cleanup" 60 postgres_up >/dev/null 2>&1 || true
  pf_ensure >/dev/null 2>&1 || true
  for q in observations observations.dead; do
    curl -fsS -u satnogs:satnogs -X DELETE "$RABBIT_API/api/queues/%2F/$q/contents" >/dev/null 2>&1 || true
  done
  psql_q "DELETE FROM observations WHERE id >= $BASE_ID" >/dev/null 2>&1 || true
  if [[ $mode == compose ]]; then dc start store >/dev/null 2>&1 || true
  else
    pkill -f "port-forward svc/rabbitmq 25672" 2>/dev/null || true
    k scale deploy/store --replicas="$store_orig" >/dev/null 2>&1 || true
  fi
  if ((code != 0)); then
    printf '\n\033[31mFAILED: %d check(s) did not hold; the run stopped there.\033[0m\n' "$failures"
  fi
}
trap cleanup EXIT

# ---- start ------------------------------------------------------------------
if [[ $mode == compose ]]; then
  log "Starting postgres, rabbitmq and store (Docker Compose)"
  if [[ $build == no ]]; then dc up -d postgres rabbitmq store >/dev/null
  else dc up -d --build postgres rabbitmq store >/dev/null; fi
else
  log "Using the Kubernetes cluster (context kind-satnogs, namespace satnogs)"
  k get statefulset postgres >/dev/null 2>&1 || { echo "no cluster: run k8s/deploy.sh first" >&2; exit 2; }
  # Run with 2 store pods, as the manifests say, and put it back how it was found.
  store_orig=$(k get deploy store -o jsonpath='{.spec.replicas}')
  k scale deploy/store --replicas="$store_run" >/dev/null
  k rollout status deploy/store --timeout=120s >/dev/null
fi
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
svc_stop store
publish_obs 20 $((BASE_ID + 200))
wait_for "20 messages wait in the queue" 20 depth_is observations 20
[[ "$(db_count)" == 5 ]] && pass "nothing was stored while store was down" || fail "rows appeared without store"
svc_start store
wait_for "all 25 rows stored" 60 count_is 25
wait_for "queue drained" 10 depth_is observations 0

# ---- 3. duplicates -----------------------------------------------------------
log "3. The same observations again: no duplicate rows"
publish_obs 5 $((BASE_ID + 100))
wait_for "queue drained" 30 depth_is observations 0
count_is 25 && pass "still 25 rows" || fail "expected 25 rows, got $(db_count)"

# ---- 4. rabbitmq restarts ----------------------------------------------------
log "4. rabbitmq restarts: store reconnects by itself"
rabbit_restart
wait_for "rabbitmq answers again" 120 rabbit_up
sleep 8 # store retries every 5 seconds
publish_obs 3 $((BASE_ID + 400))
wait_for "3 new rows stored after the restart" 60 count_is 28

# ---- 5. postgres down --------------------------------------------------------
log "5. postgres is stopped: messages are requeued, nothing is lost"
svc_stop postgres
publish_obs 4 $((BASE_ID + 500))
sleep 6
[[ "$(queue_depth observations)" -ge 1 ]] && pass "messages stay in the queue while the database is down" \
  || fail "queue empty although the database is down"
svc_start postgres
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
