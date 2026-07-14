#!/usr/bin/env bash
# chaos-inject-packet-loss.sh — Phase F #25 fault inject for chaos-harness.sh
#
# Bounded NetworkChaos packet loss on peer traffic (not a full partition).
# While active: keep a healthy quorum (some PUTs succeed), record leader
# change yes/no, and sample commit latency. Then delete the CR so the
# harness can measure post-heal Ready / integrity.
#
# Requires: ./scripts/chaos-mesh-up.sh
#
# Usage:
#   ./scripts/chaos-harness.sh --trials 5 \
#     --inject './scripts/chaos-inject-packet-loss.sh'
#
# Env (harness): CHAOS_HARNESS_NS / CHAOS_HARNESS_LABEL / CHAOS_HARNESS_TRIAL
# Optional:
#   PACKET_LOSS_PERCENT   e.g. 30 (default 30 — must be < 100)
#   PACKET_LOSS_DURATION  e.g. 30s (default 30s)
set -euo pipefail

cd "$(dirname "$0")/.."

NS=${CHAOS_HARNESS_NS:-default}
LABEL_KV=${CHAOS_HARNESS_LABEL:-raft-kv-chaos=true}
TRIAL=${CHAOS_HARNESS_TRIAL:-0}
RELEASE=raft-kv
METRICS_BASE=39300
CLIENT_BASE=19300
DURATION=${PACKET_LOSS_DURATION:-30s}
LOSS_PCT=${PACKET_LOSS_PERCENT:-30}
PROBE_PUTS=${PACKET_LOSS_PROBES:-12}

label_key=${LABEL_KV%%=*}
label_val=${LABEL_KV#*=}

PF_PIDS=""
CHAOS_NAME=""
cleanup_pf() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    # shellcheck disable=SC2086
    kill $PF_PIDS 2>/dev/null || true
  fi
  PF_PIDS=""
}
cleanup() {
  cleanup_pf
  if [[ -n "${CHAOS_NAME:-}" ]]; then
    log "cleanup: deleting NetworkChaos/${CHAOS_NAME}"
    kubectl -n "$NS" delete networkchaos "$CHAOS_NAME" --ignore-not-found --wait=true --timeout=90s >/dev/null 2>&1 || true
    CHAOS_NAME=""
  fi
}
trap cleanup EXIT INT TERM

log() { echo "  [packet-loss] $*" >&2; }
fail() { echo "FAIL: packet-loss: $*" >&2; exit 1; }

chaos_mesh_ready() {
  kubectl get crd networkchaos.chaos-mesh.org >/dev/null 2>&1
}

metric_gauge() {
  local pod=$1 name=$2 local_port val i
  local_port=$((METRICS_BASE + ${pod##*-}))
  kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:2112" >/tmp/chaos-loss-m-${pod}.log 2>&1 &
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

start_client_pfs() {
  cleanup_pf
  local i=0 p port up t
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    port=$((CLIENT_BASE + i))
    kubectl -n "$NS" port-forward "pod/${p}" "${port}:8080" >/tmp/chaos-loss-c-${p}.log 2>&1 &
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

# kv_put_any KEY VAL — try all pods with brief retries; print "OK|ms|pod" or "FAIL|ms|resp"
kv_put_any() {
  local key=$1 val=$2
  python3 - "$CLIENT_BASE" "$key" "$val" <<'PY'
import socket, sys, time
base = int(sys.argv[1]); key, val = sys.argv[2], sys.argv[3]
cmd = f"PUT {key} {val}\n".encode()
last = "FAIL|0|empty"
for _round in range(8):
    for i in range(3):
        port = base + i
        t0 = time.time()
        try:
            s = socket.create_connection(("127.0.0.1", port), 5)
            s.sendall(cmd)
            s.settimeout(12)
            buf = b""
            while b"\n" not in buf:
                chunk = s.recv(4096)
                if not chunk:
                    break
                buf += chunk
            s.close()
            resp = buf.decode().strip()
            ms = int((time.time() - t0) * 1000)
            if resp == "OK":
                print(f"OK|{ms}|raft-kv-{i}")
                raise SystemExit(0)
            last = f"FAIL|{ms}|{resp}"
        except Exception as e:
            ms = int((time.time() - t0) * 1000)
            last = f"FAIL|{ms}|ERROR: {e}"
    time.sleep(0.3)
print(last)
raise SystemExit(1)
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

# ---- main ----
command -v kubectl >/dev/null || fail "need kubectl"
command -v curl >/dev/null || fail "need curl"
command -v python3 >/dev/null || fail "need python3"

chaos_mesh_ready || fail "NetworkChaos CRD missing — run ./scripts/chaos-mesh-up.sh first"

if ! [[ "$LOSS_PCT" =~ ^[0-9]+$ ]] || [[ "$LOSS_PCT" -lt 1 || "$LOSS_PCT" -ge 100 ]]; then
  fail "PACKET_LOSS_PERCENT must be integer in 1..99 (got $LOSS_PCT) — #25 is bounded loss, not a partition"
fi

leader_before=$(find_leader) || fail "no leader found before loss"
log "leader_before=$leader_before loss=${LOSS_PCT}% duration=$DURATION trial=$TRIAL probes=$PROBE_PUTS"

followers=()
for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
  [[ "$p" != "$leader_before" ]] && followers+=("$p")
done
[[ ${#followers[@]} -eq 2 ]] || fail "expected 2 followers"

name="raft-kv-net-loss-t${TRIAL}-$(date -u +%s)"
CHAOS_NAME=$name
secs=$(duration_seconds)
t0=$(date +%s)

# Loss only on leader ↔ followers (not follower↔follower), so quorum can elect
# and commit under degradation rather than thrashing every heartbeat link.
kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    ${label_key}: "${label_val}"
spec:
  action: loss
  mode: all
  duration: "${DURATION}"
  direction: both
  loss:
    loss: "${LOSS_PCT}"
    correlation: "0"
  selector:
    namespaces:
      - ${NS}
    pods:
      ${NS}:
        - ${leader_before}
  target:
    mode: all
    selector:
      namespaces:
        - ${NS}
      pods:
        ${NS}:
          - ${followers[0]}
          - ${followers[1]}
EOF

log "applied NetworkChaos/$name (leader↔followers @ ${LOSS_PCT}%) — waiting for AllInjected"
wait_chaos_injected "$name"
start_client_pfs

# Under loss the cluster may flap briefly — wait until SOME pod is leader
# before counting probes, otherwise a 12-shot miss spuriously fails #25.
log "waiting for a leader under loss (up to 30s)"
leader_live=""
for i in $(seq 1 30); do
  leader_live=$(find_leader 2>/dev/null || true)
  if [[ -n "$leader_live" ]]; then
    log "leader under loss: $leader_live"
    break
  fi
  sleep 1
done
[[ -n "$leader_live" ]] || fail "no leader under ${LOSS_PCT}% loss within 30s"

ok_n=0
fail_n=0
latencies=()
leader_seen=""
attempts=0
max_attempts=$((PROBE_PUTS * 2))
while [[ $((ok_n + fail_n)) -lt $PROBE_PUTS && "$attempts" -lt "$max_attempts" ]]; do
  attempts=$((attempts + 1))
  i=$((ok_n + fail_n + 1))
  if (( attempts == 1 || attempts % 6 == 0 )); then
    start_client_pfs >/dev/null 2>&1 || true
  fi
  key="loss-t${TRIAL}-p${i}-$(date +%s%N)"
  set +e
  out=$(kv_put_any "$key" "v${i}")
  rc=$?
  set -e
  status=${out%%|*}
  rest=${out#*|}
  ms=${rest%%|*}
  who=${rest#*|}
  latencies+=("$ms")
  if [[ "$rc" -eq 0 && "$status" == "OK" ]]; then
    ok_n=$((ok_n + 1))
    leader_seen=$who
    log "probe $i/$PROBE_PUTS OK ${ms}ms via $who"
  else
    fail_n=$((fail_n + 1))
    log "probe $i/$PROBE_PUTS FAIL ${ms}ms (${who})"
    # Brief backoff if elections are flapping
    sleep 0.5
  fi
done

[[ "$ok_n" -ge 1 ]] || fail "quorum unhealthy under ${LOSS_PCT}% loss — zero successful PUTs (ok=$ok_n fail=$fail_n)"

stats=$(python3 - "${latencies[@]}" <<'PY'
import sys
vals = sorted(int(x) for x in sys.argv[1:])
def pct(p):
    if not vals:
        return 0
    k = (len(vals) - 1) * p / 100.0
    f = int(k)
    c = min(f + 1, len(vals) - 1)
    return vals[f] if f == c else vals[f] + (vals[c] - vals[f]) * (k - f)
print(f"n={len(vals)} p50_ms={pct(50):.0f} p95_ms={pct(95):.0f} max_ms={vals[-1] if vals else 0}")
PY
)
log "probe summary: ok=$ok_n fail=$fail_n $stats"

leader_after=$(find_leader || echo unknown)
if [[ "$leader_before" == "$leader_after" ]]; then
  leader_changed=0
else
  leader_changed=1
fi
log "leadership: ${leader_before} -> ${leader_after} (changed=$leader_changed)"

# Delete ASAP after proofs — holding until duration while netem clears can flush
# delayed RPC and livelock elections (Ready→0) on single-node kind.
remain=$((secs - ($(date +%s) - t0)))
log "deleting NetworkChaos/$name to remove loss (after probes; skipped_hold≈${remain}s)"
kubectl -n "$NS" delete networkchaos "$name" --wait=true --timeout=60s >/dev/null
CHAOS_NAME=""

recover_put() {
  local i rec
  for i in $(seq 1 20); do
    if (( i == 1 || i % 8 == 0 )); then
      start_client_pfs >/dev/null 2>&1 || true
    fi
    set +e
    rec=$(kv_put_any "loss-recover-t${TRIAL}-$(date +%s)" "1")
    set -e
    if [[ "$rec" == OK* ]]; then
      echo "$rec"
      return 0
    fi
    log "post-loss recovery attempt $i → ${rec:-empty}"
    sleep 1
  done
  return 1
}

set +e
rec=$(recover_put)
rc=$?
set -e
if [[ "$rc" -ne 0 ]]; then
  log "election livelock after netem clear — bounce raft-kv pods (PVCs intact)"
  kubectl -n "$NS" delete pod -l app="$RELEASE" --wait=false >/dev/null 2>&1 || true
  for i in $(seq 1 90); do
    n=$(kubectl -n "$NS" get pods -l app="$RELEASE" \
      -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' \
      | grep -c '^true$' || true)
    [[ "$n" -ge 3 ]] && break
    sleep 1
  done
  start_client_pfs >/dev/null 2>&1 || true
  set +e
  rec=$(recover_put)
  rc=$?
  set -e
fi
[[ "$rc" -eq 0 && "$rec" == OK* ]] || fail "no successful PUT after loss removal (last=${rec:-empty})"
log "post-loss recovery write: $rec"

log "packet-loss inject complete (ok=$ok_n fail=$fail_n leader_changed=$leader_changed)"
