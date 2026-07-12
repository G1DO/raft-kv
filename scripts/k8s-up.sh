#!/usr/bin/env bash
# k8s-up.sh — stand up the 3-node raft-kv StatefulSet on a local kind cluster.
#
#   1. create (or reuse) a kind cluster named "raft-kv"
#   2. build the container image and load it into the cluster's node
#   3. install the Helm chart (headless Service + StatefulSet)
#   4. wait for all 3 pods to roll out Ready
#
# Requires docker, kind, kubectl and helm on PATH.
#
#   ./scripts/k8s-up.sh         # bring it up
#   kubectl get pods -l app=raft-kv -o wide
#   kubectl logs raft-kv-0      # see elections / replication
#   ./scripts/k8s-down.sh       # tear the kind cluster down
set -euo pipefail

cd "$(dirname "$0")/.."

CLUSTER=raft-kv
RELEASE=raft-kv
CHART=deploy/helm/raft-kv
IMAGE=raft-kv:dev

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "==> reusing existing kind cluster '$CLUSTER'"
else
  echo "==> creating kind cluster '$CLUSTER'"
  kind create cluster --name "$CLUSTER" --config deploy/kind-config.yaml
fi

echo "==> building image $IMAGE"
docker build -t "$IMAGE" .

echo "==> loading image into kind"
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> installing the Helm chart ($RELEASE)"
# Chart defaults tls.enabled=true (ADR-009/010). Lab Secrets match ADR-009 naming
# (scripts/gen-ordinal-tls-secrets.sh); production uses Helm tls.eso.enabled + Vault.
# --wait blocks until the StatefulSet's pods are Ready (replaces a separate
# rollout-status step); --install makes the first run and re-runs idempotent.
./scripts/gen-ordinal-tls-secrets.sh --name "$RELEASE" --replicas 3 --ca-dir data/lab-ca
helm upgrade --install "$RELEASE" "$CHART" --wait --timeout 120s

echo
kubectl get pods -l app=raft-kv -o wide
echo
echo "cluster up. Try:"
echo "  kubectl logs raft-kv-0                       # watch elections / replication"
echo "  kubectl port-forward pod/raft-kv-0 8080:8080 # then talk the KV text protocol"
echo "  ./scripts/tls-rotation-drill.sh              # Phase B #9 leaf cert rotation drill"
