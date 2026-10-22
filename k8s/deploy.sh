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
$K apply -f k8s/

# Same tag, new image: restart the pods so they pick it up.
$K -n satnogs rollout restart deployment/fetch deployment/store
