#!/usr/bin/env bash
# chaos-inject-network-partition.sh — Phase F #24 (M8 gate) for chaos-harness.sh
#
# Time-bounded bidirectional NetworkChaos: isolate the current Raft leader from
# both followers. Proves (while default-deny NetworkPolicy stays applied):
#   - old leader cannot commit a new PUT (NOT_LEADER or commit timeout)
#   - majority elects and accepts a PUT (OK)
# Then waits for Chaos duration, deletes the CR, and returns so the harness can
# measure post-heal Ready / integrity.
#
# Requires: ./scripts/chaos-mesh-up.sh and Calico (or Cilium).
#
# Usage:
#   ./scripts/chaos-harness.sh --trials 3 \
#     --inject './scripts/chaos-inject-network-partition.sh'
#
# Env (harness): CHAOS_HARNESS_NS / CHAOS_HARNESS_LABEL / CHAOS_HARNESS_TRIAL
# Optional: PARTITION_DURATION (default 30s)
set -euo pipefail

cd "$(dirname "$0")/.."

NS=${CHAOS_HARNESS_NS:-default}
LABEL_KV=${CHAOS_HARNESS_LABEL:-raft-kv-chaos=true}
TRIAL=${CHAOS_HARNESS_TRIAL:-0}
RELEASE=raft-kv
METRICS_BASE=39200
CLIENT_BASE=19200
DURATION=${PARTITION_DURATION:-30s}

label_key=${LABEL_KV%%=*}
label_val=${LABEL_KV#*=}

PF_PIDS=""
cleanup_pf() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    # shellcheck disable=SC2086
    kill $PF_PIDS 2>/dev/null || true
  fi
  PF_PIDS=""
}
trap cleanup_pf EXIT INT TERM

log() { echo "  [partition] $*" >&2; }
fail() { echo "FAIL: partition: $*" >&2; exit 1; }

chaos_mesh_ready() {
  kubectl get crd networkchaos.chaos-mesh.org >/dev/null 2>&1
}

np_default_deny_present() {
  kubectl -n "$NS" get networkpolicy raft-kv-default-deny-ingress >/dev/null 2>&1 \
    && kubectl -n "$NS" get networkpolicy raft-kv-default-deny-egress >/dev/null 2>&1
}

metric_gauge() {
  local pod=$1 name=$2 local_port val i
  local_port=$((METRICS_BASE + ${pod##*-}))
  kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:2112" >/tmp/chaos-part-m-${pod}.log 2>&1 &
  local pf=$!
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

find_leader() {
  local p is i
  for i in $(seq 1 20); do
    for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
      is=$(metric_gauge "$p" raft_is_leader 2>/dev/null || echo 0)
      if [[ "$is" == "1" || "$is" == "1.0" ]]; then
        echo "$p"
        return 0
      fi
    done
    sleep 0.5
  done
  return 1
}

followers_of() {
  local leader=$1 p
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    [[ "$p" != "$leader" ]] && echo "$p"
  done
}

start_client_pfs() {
  cleanup_pf
  local i=0 p port up t
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    port=$((CLIENT_BASE + i))
    kubectl -n "$NS" port-forward "pod/${p}" "${port}:8080" >/tmp/chaos-part-c-${p}.log 2>&1 &
    PF_PIDS+=" $!"
    i=$((i + 1))
  done
  for t in $(seq 1 50); do
    up=0
    for i in 0 1 2; do
      port=$((CLIENT_BASE + i))
      if python3 -c "import socket; s=socket.create_connection(('127.0.0.1',${port}),0.5); s.close()" 2>/dev/null; then
        up=$((up + 1))
      fi
    done
    [[ "$up" -ge 3 ]] && return 0
    sleep 0.2
  done
  fail "client port-forwards not ready"
}

kv_put_pod() {
  local pod=$1 key=$2 val=$3 port idx
  idx=${pod##*-}
  port=$((CLIENT_BASE + idx))
  python3 - "$port" "PUT $key $val" <<'PY'
import socket, sys
port = int(sys.argv[1]); cmd = sys.argv[2]
try:
    s = socket.create_connection(("127.0.0.1", port), 5)
    s.sendall((cmd + "\n").encode())
    s.settimeout(12)
    buf = b""
    while b"\n" not in buf:
        chunk = s.recv(4096)
        if not chunk:
            break
        buf += chunk
    s.close()
    print(buf.decode().strip())
except Exception as e:
    print(f"ERROR: {e}")
PY
}

duration_seconds() {
  python3 - "$DURATION" <<'PY'
import sys, re
s = sys.argv[1].strip()
m = re.fullmatch(r"(\d+)(s|m|h)?", s)
if not m:
    print(30); raise SystemExit
n, u = int(m.group(1)), m.group(2) or "s"
print(n * {"s": 1, "m": 60, "h": 3600}[u])
PY
}

wait_chaos_injected() {
  local name=$1 i st
  for i in $(seq 1 90); do
    st=$(kubectl -n "$NS" get networkchaos "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="AllInjected")].status}' 2>/dev/null || true)
    if [[ "$st" == "True" ]]; then
      log "NetworkChaos/$name AllInjected=True"
      return 0
    fi
    sleep 1
  done
  kubectl -n "$NS" get networkchaos "$name" -o yaml >&2 || true
  fail "NetworkChaos $name never reached AllInjected=True"
}

assert_old_leader_cannot_commit() {
  local leader=$1 key=$2 resp
  resp=$(kv_put_pod "$leader" "$key" "isolated")
  log "PUT to isolated leader $leader → ${resp:-empty}"
  case "$resp" in
    OK)
      fail "isolated leader $leader accepted commit (OK) — partition ineffective or Raft bug"
      ;;
    *)
      log "old leader did not commit (${resp:-empty})"
      ;;
  esac
}

assert_majority_can_commit() {
  local old_leader=$1 key=$2 p resp i
  # Try every pod that is not the isolated old leader. Refresh PFs periodically.
  for i in $(seq 1 60); do
    if (( i == 1 || i % 15 == 0 )); then
      start_client_pfs >/dev/null 2>&1 || true
    fi
    for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
      [[ "$p" == "$old_leader" ]] && continue
      resp=$(kv_put_pod "$p" "$key" "majority")
      log "try majority via $p → ${resp:-empty}"
      if [[ "$resp" == "OK" ]]; then
        log "majority commit via $p → OK (key=$key)"
        return 0
      fi
    done
    sleep 1
  done
  fail "majority did not accept a PUT during partition"
}

# ---- main ----
command -v kubectl >/dev/null || fail "need kubectl"
command -v curl >/dev/null || fail "need curl"
command -v python3 >/dev/null || fail "need python3"

chaos_mesh_ready || fail "NetworkChaos CRD missing — run ./scripts/chaos-mesh-up.sh first"
np_default_deny_present || fail "default-deny NetworkPolicy missing in $NS — enable chart networkPolicy (ADR-011)"

leader=$(find_leader) || fail "no leader found"
mapfile -t FOLLOWERS < <(followers_of "$leader")
[[ ${#FOLLOWERS[@]} -eq 2 ]] || fail "expected 2 followers, got ${#FOLLOWERS[@]}"

log "leader=$leader followers=${FOLLOWERS[*]} duration=$DURATION trial=$TRIAL"
log "NetworkPolicy default-deny ingress+egress present in $NS (unchanged)"

name="raft-kv-net-part-t${TRIAL}-$(date -u +%s)"
secs=$(duration_seconds)
t0=$(date +%s)

kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    ${label_key}: "${label_val}"
spec:
  action: partition
  mode: all
  duration: "${DURATION}"
  direction: both
  selector:
    namespaces:
      - ${NS}
    pods:
      ${NS}:
        - ${leader}
  target:
    mode: all
    selector:
      namespaces:
        - ${NS}
      pods:
        ${NS}:
          - ${FOLLOWERS[0]}
          - ${FOLLOWERS[1]}
EOF

log "applied NetworkChaos/$name — waiting for AllInjected"
wait_chaos_injected "$name"

start_client_pfs
# Give the majority time to notice heartbeat loss and elect.
log "waiting 8s for majority election under partition"
sleep 8

key_iso="part-iso-t${TRIAL}-$(date +%s)"
key_maj="part-maj-t${TRIAL}-$(date +%s)"
assert_old_leader_cannot_commit "$leader" "$key_iso"
assert_majority_can_commit "$leader" "$key_maj"

# Wait out the Chaos duration, then delete CR so heal is explicit for harness.
elapsed=$(( $(date +%s) - t0 ))
remain=$((secs - elapsed))
if [[ "$remain" -gt 0 ]]; then
  log "holding until duration elapses (${remain}s left)"
  sleep "$remain"
fi

log "deleting NetworkChaos/$name to heal partition"
kubectl -n "$NS" delete networkchaos "$name" --wait=true --timeout=60s >/dev/null

np_default_deny_present || fail "default-deny NetworkPolicy disappeared during partition"
log "default-deny NetworkPolicy still present after partition"
log "partition inject complete"
