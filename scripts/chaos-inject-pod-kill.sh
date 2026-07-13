#!/usr/bin/env bash
# chaos-inject-pod-kill.sh — Phase F #23 fault inject for chaos-harness.sh
#
# Kills the current Raft leader once. The StatefulSet recreates the pod on the
# same PVC (data retained). Prefer Chaos Mesh PodChaos when CRDs exist; otherwise
# fall back to kubectl delete pod (same K8s failure domain for #23).
#
# Used as:
#   ./scripts/chaos-harness.sh --trials 5 --inject './scripts/chaos-inject-pod-kill.sh'
#
# Env (set by chaos-harness.sh):
#   CHAOS_HARNESS_NS      workload namespace (default: default)
#   CHAOS_HARNESS_LABEL   Chaos CR label selector (default: raft-kv-chaos=true)
#   CHAOS_HARNESS_TRIAL   trial number (for CR naming)
set -euo pipefail

cd "$(dirname "$0")/.."

NS=${CHAOS_HARNESS_NS:-default}
LABEL_KV=${CHAOS_HARNESS_LABEL:-raft-kv-chaos=true}
TRIAL=${CHAOS_HARNESS_TRIAL:-0}
RELEASE=raft-kv
METRICS_BASE=39100

label_key=${LABEL_KV%%=*}
label_val=${LABEL_KV#*=}

log() { echo "  [pod-kill] $*" >&2; }
fail() { echo "FAIL: pod-kill: $*" >&2; exit 1; }

metric_gauge() {
  local pod=$1 name=$2 local_port
  local_port=$((METRICS_BASE + ${pod##*-}))
  kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:2112" >/tmp/chaos-podkill-m-${pod}.log 2>&1 &
  local pf=$!
  local i val=""
  for i in $(seq 1 40); do
    val=$(curl -sf --max-time 1 "http://127.0.0.1:${local_port}/metrics" 2>/dev/null \
      | awk -v n="$name" '$1==n {print $2; exit}') || true
    if [[ -n "$val" ]]; then
      kill "$pf" 2>/dev/null || true
      wait "$pf" 2>/dev/null || true
      echo "$val"
      return 0
    fi
    sleep 0.1
  done
  kill "$pf" 2>/dev/null || true
  wait "$pf" 2>/dev/null || true
  echo ""
  return 1
}

# find_leader — prefer raft_is_leader metric; fallback: PUT via short TCP probes.
find_leader() {
  local p is port i resp
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    is=$(metric_gauge "$p" raft_is_leader 2>/dev/null || echo 0)
    if [[ "$is" == "1" || "$is" == "1.0" ]]; then
      echo "$p"
      return 0
    fi
  done

  # Fallback: client-port PUT (uses kubectl exec busybox only if metrics fail).
  for i in 0 1 2; do
    p="${RELEASE}-${i}"
    kubectl -n "$NS" get pod "$p" >/dev/null 2>&1 || continue
    resp=$(kubectl -n "$NS" exec "$p" -- sh -c \
      "printf 'PUT __podkill_probe__ 1\n' | nc -w 2 127.0.0.1 8080" 2>/dev/null \
      | tr -d '\r' | tail -1 || true)
    if [[ "$resp" == "OK" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

chaos_mesh_ready() {
  kubectl get crd podchaos.chaos-mesh.org >/dev/null 2>&1
}

kill_via_podchaos() {
  local leader=$1 uid before name
  before=$(kubectl -n "$NS" get pod "$leader" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
  [[ -n "$before" ]] || fail "could not read UID of $leader"
  name="raft-kv-pod-kill-t${TRIAL}-$(date -u +%s)"

  log "PodChaos pod-kill targeting $leader (uid=$before)"
  kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: PodChaos
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    ${label_key}: "${label_val}"
spec:
  action: pod-kill
  mode: all
  duration: 30s
  selector:
    namespaces:
      - ${NS}
    pods:
      ${NS}:
        - ${leader}
EOF

  # Wait until the pod UID changes (killed + replaced) or the object is gone.
  local i uid
  for i in $(seq 1 90); do
    uid=$(kubectl -n "$NS" get pod "$leader" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
    if [[ -z "$uid" || "$uid" != "$before" ]]; then
      log "pod $leader killed/replaced (uid ${before} -> ${uid:-gone})"
      return 0
    fi
    sleep 1
  done
  fail "PodChaos applied but $leader UID did not change within 90s"
}

kill_via_kubectl() {
  local leader=$1 before uid i
  before=$(kubectl -n "$NS" get pod "$leader" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
  [[ -n "$before" ]] || fail "could not read UID of $leader"
  log "kubectl delete pod $leader (uid=$before; PVC kept by StatefulSet)"
  kubectl -n "$NS" delete pod "$leader" --wait=false >/dev/null
  for i in $(seq 1 90); do
    uid=$(kubectl -n "$NS" get pod "$leader" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)
    if [[ -z "$uid" || "$uid" != "$before" ]]; then
      log "pod $leader killed/replaced (uid ${before} -> ${uid:-gone})"
      return 0
    fi
    sleep 1
  done
  fail "kubectl delete issued but $leader UID did not change within 90s"
}

# ---- main ----
command -v kubectl >/dev/null || fail "need kubectl"
command -v curl >/dev/null || fail "need curl"

leader=$(find_leader) || fail "no leader found (cluster unhealthy?)"
log "leader=$leader ns=$NS trial=$TRIAL"

if chaos_mesh_ready; then
  kill_via_podchaos "$leader"
else
  log "Chaos Mesh PodChaos CRD not present — using kubectl delete pod"
  kill_via_kubectl "$leader"
fi
