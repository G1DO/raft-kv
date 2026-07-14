#!/usr/bin/env bash
# chaos-mesh-up.sh — install Chaos Mesh 2.8.3 into chaos-mesh (ADR-013).
#
# Prerequisites: kubectl, helm; Calico (or Cilium) already enforcing NetworkPolicy.
# Does not install Calico — see docs/runbooks/networkpolicy.md.
#
# Usage:
#   ./scripts/chaos-mesh-up.sh
#   ./scripts/chaos-mesh-up.sh --version 2.8.3
#
# Idempotent: upgrades/installs the pinned chart into namespace chaos-mesh only.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION=2.8.3
NS=chaos-mesh
RELEASE=chaos-mesh
RUNTIME=containerd
SOCKET=/run/containerd/containerd.sock

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION=$2; shift 2 ;;
    --namespace) NS=$2; shift 2 ;;
    --runtime) RUNTIME=$2; shift 2 ;;
    --socket) SOCKET=$2; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need kubectl
need helm

log() { echo "==> $*" >&2; }
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*" >&2; }

cni_enforces_policy() {
  kubectl get pods -A -l k8s-app=calico-node --no-headers 2>/dev/null | grep -q Running && return 0
  kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q Running && return 0
  return 1
}

log "preflight: NetworkPolicy-capable CNI (ADR-013)"
cni_enforces_policy || fail "Calico/Cilium not detected — install Calico before Chaos Mesh (docs/runbooks/networkpolicy.md)"
ok "CNI with NetworkPolicy enforcement detected"

log "helm repo chaos-mesh"
helm repo add chaos-mesh https://charts.chaos-mesh.org >/dev/null 2>&1 || true
helm repo update chaos-mesh >/dev/null

kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

log "kind inotify limits (Chaos Mesh controllers need this on kind)"
# "too many open files" is the usual kind failure mode for chaos-controller-manager.
for node in $(kubectl get nodes -o name 2>/dev/null | sed 's|node/||'); do
  docker exec "$node" sh -c \
    'sysctl -w fs.inotify.max_user_watches=524288 fs.inotify.max_user_instances=8192 fs.file-max=2097152' \
    >/dev/null 2>&1 || true
done

log "install/upgrade $RELEASE chart $VERSION into $NS (runtime=$RUNTIME)"
helm upgrade --install "$RELEASE" chaos-mesh/chaos-mesh \
  --namespace "$NS" \
  --version "$VERSION" \
  --set chaosDaemon.runtime="$RUNTIME" \
  --set chaosDaemon.socketPath="$SOCKET" \
  --set dashboard.securityMode=false \
  --set controllerManager.replicaCount=1 \
  --wait --timeout 5m

log "wait for Chaos Mesh pods"
kubectl -n "$NS" wait --for=condition=Ready pod -l app.kubernetes.io/instance="$RELEASE" --timeout=180s >/dev/null \
  || kubectl -n "$NS" get pods -o wide >&2

kubectl get crd podchaos.chaos-mesh.org networkchaos.chaos-mesh.org >/dev/null \
  || fail "Chaos Mesh CRDs missing after install"

ok "Chaos Mesh $VERSION ready in namespace $NS"
kubectl -n "$NS" get pods -o wide >&2
