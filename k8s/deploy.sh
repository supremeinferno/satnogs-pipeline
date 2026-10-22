#!/usr/bin/env bash
# Builds the image, loads it into a local kind cluster and applies k8s/.
# Needs Docker, kind and kubectl.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=satnogs
K="kubectl --context kind-$CLUSTER"

kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER"

docker build -t satnogs-pipeline:dev .
# kind nodes are containers with their own image store: load the image in.
kind load docker-image satnogs-pipeline:dev --name "$CLUSTER"

$K apply -f k8s/00-namespace.yaml
# One copy of the schema: the ConfigMap is made from ../schema.sql.
$K -n satnogs create configmap schema --from-file=schema.sql --dry-run=client -o yaml | $K apply -f -

# Secrets that must not be in git come from .env (git-ignored), if it exists.
# Both are created even when empty, so the manifests always find them.
if [[ -f .env ]]; then set -a; source .env; set +a; fi
$K -n satnogs create secret generic satnogs-db --from-literal=token="${SATNOGS_DB_TOKEN:-}" \
  --dry-run=client -o yaml | $K apply -f -
$K -n satnogs create secret generic telegram \
  --from-literal=TELEGRAM_TOKEN="${TELEGRAM_TOKEN:-}" --from-literal=TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-}" \
  --dry-run=client -o yaml | $K apply -f -

# The monitoring configuration is the same files Docker Compose uses.
cm() { $K -n satnogs create configmap "$@" --dry-run=client -o yaml | $K apply -f -; }
cm prometheus-config --from-file=monitoring/prometheus.yml --from-file=monitoring/alerts.yml
cm alertmanager-config --from-file=monitoring/alertmanager.yml
cm grafana-datasources --from-file=monitoring/grafana/provisioning/datasources
cm grafana-dashboard-providers --from-file=monitoring/grafana/provisioning/dashboards
cm grafana-dashboards --from-file=monitoring/grafana/dashboards

$K apply -f k8s/

# Postgres runs schema.sql only on an empty volume. The file can be applied
# again safely, so do it every time: a database from an older deploy gets the
# tables added since.
$K -n satnogs wait --for=condition=ready pod/postgres-0 --timeout=180s
$K -n satnogs exec -i postgres-0 -- psql -U satnogs -q < schema.sql 2>&1 | grep -v NOTICE || true

# Same tag, new image: restart the pods so they pick it up.
$K -n satnogs rollout restart deployment/fetch deployment/fetch-telemetry deployment/store deployment/watch deployment/notify

if [[ -z "${SATNOGS_DB_TOKEN:-}" ]]; then
  echo "SATNOGS_DB_TOKEN is not set (.env): fetch-telemetry is switched off" >&2
  $K -n satnogs scale deployment/fetch-telemetry --replicas=0
fi
