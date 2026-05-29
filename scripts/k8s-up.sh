#!/usr/bin/env bash
# k8s-up.sh — stand up the 3-node raft-kv StatefulSet on a local kind cluster.
#
#   1. create (or reuse) a kind cluster named "raft-kv"
#   2. build the container image and load it into the cluster's node
#   3. apply the headless Service + StatefulSet
#   4. wait for all 3 pods to roll out Ready
#
# Requires docker, kind and kubectl on PATH.
#
#   ./scripts/k8s-up.sh         # bring it up
#   kubectl get pods -l app=raft-kv -o wide
#   kubectl logs raft-kv-0      # see elections / replication
#   ./scripts/k8s-down.sh       # tear the kind cluster down
set -euo pipefail

cd "$(dirname "$0")/.."

CLUSTER=raft-kv
IMAGE=raft-kv:dev

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "==> reusing existing kind cluster '$CLUSTER'"
else
  echo "==> creating kind cluster '$CLUSTER'"
  kind create cluster --name "$CLUSTER"
fi

echo "==> building image $IMAGE"
docker build -t "$IMAGE" .

echo "==> loading image into kind"
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> applying manifests"
kubectl apply -f deploy/k8s/raft-kv.yaml

echo "==> waiting for the StatefulSet to become Ready"
kubectl rollout status statefulset/raft-kv --timeout=120s

echo
kubectl get pods -l app=raft-kv -o wide
echo
echo "cluster up. Try:"
echo "  kubectl logs raft-kv-0                       # watch elections / replication"
echo "  kubectl port-forward pod/raft-kv-0 8080:8080 # then talk the KV text protocol"
