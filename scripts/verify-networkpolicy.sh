#!/usr/bin/env bash
# verify-networkpolicy.sh — Phase C #12 NetworkPolicy semantics (live cluster).
#
# Proves default-deny + named allows from ADR-011:
#   - unlabeled pod cannot reach 8080/9090/2112
#   - labeled client in raft-kv-clients reaches 8080 only
#   - Prometheus reaches 2112 only
#   - raft-kv pods stay Ready (DNS, peers, probes)
#
# Usage:
#   ./scripts/verify-networkpolicy.sh
#   ./scripts/verify-networkpolicy.sh --namespace default
#
# Requires: kubectl, helm, a cluster with NetworkPolicy enforcement (Calico/Cilium).
# Stock kindnet does NOT enforce — the script fails fast with an install hint.
set -euo pipefail

cd "$(dirname "$0")/.."

CHART=deploy/helm/raft-kv
RELEASE=raft-kv
RAFT_NS=default
CLIENT_NS=raft-kv-clients
DENY_POD=np-deny-probe-$$
ALLOW_POD=np-allow-client-$$

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) RAFT_NS=$2; shift 2 ;;
    -h|--help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

command -v kubectl >/dev/null || { echo "need kubectl" >&2; exit 1; }
command -v helm >/dev/null || { echo "need helm" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }
info() { echo "==> $*"; }

RAFT_SVC=raft-kv
RAFT_HOST="${RAFT_SVC}.${RAFT_NS}.svc.cluster.local"
TARGET_POD="${RELEASE}-0"
TARGET_FQDN="${TARGET_POD}.${RAFT_SVC}.${RAFT_NS}.svc.cluster.local"

cleanup() {
  kubectl delete pod -n "$RAFT_NS" -l np-verify=deny --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete pod -n "$CLIENT_NS" -l np-verify=allow --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete pod -n observability -l np-verify=prom --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

cni_enforces_policy() {
  if kubectl get pods -A -l k8s-app=calico-node --no-headers 2>/dev/null | grep -q Running; then
    return 0
  fi
  if kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q Running; then
    return 0
  fi
  return 1
}

pod_ready() {
  kubectl get pod -n "$1" "$2" -o jsonpath='{.status.phase}' 2>/dev/null | grep -qx Running
}

wait_pod() {
  kubectl wait --for=condition=Ready "pod/$2" -n "$1" --timeout=90s >/dev/null
}

# Returns 0 when TCP connect succeeds, 1 when blocked/refused.
tcp_probe() {
  local ns=$1 pod=$2 host=$3 port=$4
  kubectl exec -n "$ns" "$pod" -- sh -c "nc -zv -w 3 '$host' '$port'" >/dev/null 2>&1
}

info "static: helm renders four NetworkPolicy objects when enabled"
NP_COUNT=$(helm template rk "$CHART" --namespace "$RAFT_NS" --set replicaCount=3 \
  | grep -c '^kind: NetworkPolicy$' || true)
[[ "$NP_COUNT" -eq 4 ]] || fail "expected 4 NetworkPolicy manifests, got $NP_COUNT"
ok "helm renders 4 NetworkPolicies"

info "live: checking cluster + raft-kv release in namespace $RAFT_NS"
kubectl cluster-info >/dev/null 2>&1 || fail "kubectl cannot reach a cluster"

NP_LIVE=$(kubectl get networkpolicy -n "$RAFT_NS" -l app=raft-kv --no-headers 2>/dev/null | wc -l)
[[ "$NP_LIVE" -ge 4 ]] || fail "expected >=4 raft-kv NetworkPolicies in $RAFT_NS (got $NP_LIVE); run helm upgrade first"

READY=$(kubectl get pods -n "$RAFT_NS" -l app=raft-kv --no-headers 2>/dev/null | awk '$2=="1/1"{n++} END{print n+0}')
[[ "$READY" -ge 3 ]] || fail "expected 3 Ready raft-kv pods in $RAFT_NS (got $READY)"

if ! cni_enforces_policy; then
  fail "no Calico/Cilium detected — kindnet does not enforce NetworkPolicy. Install Calico (ADR-013) and re-run."
fi
ok "CNI with NetworkPolicy enforcement detected"

info "live: enforcement sanity — unlabeled pod must not reach client port before full suite"
kubectl run "$DENY_POD" -n "$RAFT_NS" --restart=Never --image=busybox:1.36 \
  -l np-verify=deny --command -- sleep 600 >/dev/null
wait_pod "$RAFT_NS" "$DENY_POD"
if tcp_probe "$RAFT_NS" "$DENY_POD" "$TARGET_POD.$RAFT_SVC" 8080; then
  fail "unlabeled pod reached :8080 — policies are not enforced"
fi
ok "unlabeled pod blocked on :8080 (enforcement active)"

info "live: negative tests — unlabeled pod denied on 8080, 9090, 2112"
for port in 8080 9090 2112; do
  if tcp_probe "$RAFT_NS" "$DENY_POD" "$TARGET_POD.$RAFT_SVC" "$port"; then
    fail "unlabeled pod reached :$port (expected deny)"
  fi
  ok "unlabeled pod denied :$port"
done

info "live: positive client — labeled pod in $CLIENT_NS may use :8080 only"
kubectl create namespace "$CLIENT_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl run "$ALLOW_POD" -n "$CLIENT_NS" --restart=Never --image=busybox:1.36 \
  -l raft-kv.client=true,np-verify=allow --command -- sleep 600 >/dev/null
wait_pod "$CLIENT_NS" "$ALLOW_POD"

tcp_probe "$CLIENT_NS" "$ALLOW_POD" "$TARGET_FQDN" 8080 || fail "labeled client could not reach :8080"
ok "labeled client allowed :8080"

for port in 9090 2112; do
  if tcp_probe "$CLIENT_NS" "$ALLOW_POD" "$TARGET_FQDN" "$port"; then
    fail "labeled client reached :$port (expected deny)"
  fi
  ok "labeled client denied :$port"
done

RESP=$(kubectl exec -n "$CLIENT_NS" "$ALLOW_POD" -- sh -c \
  "printf 'PUT np-verify-key np-verify-val\n' | nc -w 3 '$TARGET_FQDN' 8080" 2>/dev/null || true)
echo "$RESP" | grep -q OK || fail "labeled client PUT failed (got: ${RESP:-empty})"
ok "labeled client PUT returned OK"

info "live: Prometheus may scrape :2112 only"
PROM_NS=observability
PROM_POD=np-prom-probe-$$
kubectl run "$PROM_POD" -n "$PROM_NS" --restart=Never --image=busybox:1.36 \
  -l app.kubernetes.io/name=prometheus,np-verify=prom --command -- sleep 600 >/dev/null
wait_pod "$PROM_NS" "$PROM_POD"
tcp_probe "$PROM_NS" "$PROM_POD" "$TARGET_FQDN" 2112 || fail "Prometheus-labeled pod could not reach :2112"
ok "Prometheus-labeled pod allowed :2112"

if tcp_probe "$PROM_NS" "$PROM_POD" "$TARGET_FQDN" 8080; then
  fail "Prometheus-labeled pod reached :8080 (expected deny)"
fi
ok "Prometheus-labeled pod denied :8080"
kubectl delete pod "$PROM_POD" -n "$PROM_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true

info "live: raft-kv cluster health after policies"
for i in 0 1 2; do
  pod="${RELEASE}-$i"
  kubectl get pod -n "$RAFT_NS" "$pod" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' | grep -qx True \
    || fail "pod $pod not Ready"
done
ok "all 3 raft-kv pods Ready (Kubernetes Ready condition)"

echo
ok "NetworkPolicy verification complete"
