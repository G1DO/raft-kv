#!/usr/bin/env bash
# chaos-inject-clock-skew.sh — Phase F #26 fault inject for chaos-harness.sh
#
# Bounded TimeChaos on one non-leader first. Observe election/availability under
# skew, then force rollback (duration end + CR delete). Never leaves clock
# changes active. Leader targeting requires CLOCK_SKEW_ALLOW_LEADER=1.
#
# Requires: ./scripts/chaos-mesh-up.sh (TimeChaos CRD).
#
# Usage:
#   ./scripts/chaos-harness.sh --trials 3 \
#     --inject './scripts/chaos-inject-clock-skew.sh'
#
# Env (harness): CHAOS_HARNESS_NS / CHAOS_HARNESS_LABEL / CHAOS_HARNESS_TRIAL
# Optional:
#   CLOCK_SKEW_OFFSET        e.g. +5m or -5m (default +5m)
#   CLOCK_SKEW_DURATION      e.g. 30s (default 30s)
#   CLOCK_SKEW_PROBES        probe PUT count (default 8)
#   CLOCK_SKEW_ALLOW_LEADER  set to 1 to allow skewing the leader (default refuse)
set -euo pipefail

cd "$(dirname "$0")/.."

NS=${CHAOS_HARNESS_NS:-default}
LABEL_KV=${CHAOS_HARNESS_LABEL:-raft-kv-chaos=true}
TRIAL=${CHAOS_HARNESS_TRIAL:-0}
RELEASE=raft-kv
METRICS_BASE=39400
CLIENT_BASE=19400
DURATION=${CLOCK_SKEW_DURATION:-30s}
OFFSET=${CLOCK_SKEW_OFFSET:-+5m}
PROBE_PUTS=${CLOCK_SKEW_PROBES:-8}
ALLOW_LEADER=${CLOCK_SKEW_ALLOW_LEADER:-0}

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
    log "cleanup: deleting TimeChaos/${CHAOS_NAME}"
    kubectl -n "$NS" delete timechaos "$CHAOS_NAME" --ignore-not-found --wait=true --timeout=90s >/dev/null 2>&1 || true
    CHAOS_NAME=""
  fi
}
trap cleanup EXIT INT TERM

log() { echo "  [clock-skew] $*" >&2; }
fail() { echo "FAIL: clock-skew: $*" >&2; exit 1; }

chaos_mesh_ready() {
  kubectl get crd timechaos.chaos-mesh.org >/dev/null 2>&1
}

metric_gauge() {
  local pod=$1 name=$2 local_port val i
  local_port=$((METRICS_BASE + ${pod##*-}))
  kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:2112" >/tmp/chaos-skew-m-${pod}.log 2>&1 &
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
  local p is i port resp
  for i in $(seq 1 20); do
    for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
      is=$(metric_gauge "$p" raft_is_leader 2>/dev/null || echo 0)
      if [[ "$is" == "1" || "$is" == "1.0" ]]; then
        echo "$p"
        return 0
      fi
    done
    # Fallback: who accepts a PUT (port-forward metrics can miss a Ready leader).
    start_client_pfs >/dev/null 2>&1 || true
    for idx in 0 1 2; do
      p="${RELEASE}-${idx}"
      port=$((CLIENT_BASE + idx))
      resp=$(python3 - "$port" <<'PY'
import socket, sys
port = int(sys.argv[1])
try:
    s = socket.create_connection(("127.0.0.1", port), 3)
    s.sendall(b"PUT __skew_probe__ 1\n")
    s.settimeout(5)
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
)
      if [[ "$resp" == "OK" ]]; then
        echo "$p"
        return 0
      fi
    done
    sleep 0.5
  done
  return 1
}

pick_follower() {
  local leader=$1 p ready
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    [[ "$p" == "$leader" ]] && continue
    ready=$(kubectl -n "$NS" get pod "$p" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)
    if [[ "$ready" == "true" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

start_client_pfs() {
  cleanup_pf
  local i=0 p port up t
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    port=$((CLIENT_BASE + i))
    kubectl -n "$NS" port-forward "pod/${p}" "${port}:8080" >/tmp/chaos-skew-c-${p}.log 2>&1 &
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
    st=$(kubectl -n "$NS" get timechaos "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="AllInjected")].status}' 2>/dev/null || true)
    if [[ "$st" == "True" ]]; then
      log "TimeChaos/$name AllInjected=True"
      return 0
    fi
    # Some Chaos Mesh builds populate records before the condition flips.
    if kubectl -n "$NS" get timechaos "$name" -o jsonpath='{.status.experiment.desiredPhase}' 2>/dev/null | grep -q Run; then
      if kubectl -n "$NS" get timechaos "$name" -o yaml 2>/dev/null | grep -qE 'Injected|injected'; then
        sleep 2
        log "TimeChaos/$name appears injecting (desiredPhase=Run)"
        return 0
      fi
    fi
    sleep 1
  done
  kubectl -n "$NS" get timechaos "$name" -o yaml >&2 || true
  fail "TimeChaos $name never reached AllInjected"
}

assert_no_leftover_timechaos() {
  local left
  left=$(kubectl get timechaos -A -l "$LABEL_KV" --no-headers 2>/dev/null | wc -l | tr -d ' ')
  [[ "${left:-0}" -eq 0 ]] || fail "TimeChaos leftovers still present (label $LABEL_KV) — skew must be rolled back"
}

# ---- main ----
command -v kubectl >/dev/null || fail "need kubectl"
command -v curl >/dev/null || fail "need curl"
command -v python3 >/dev/null || fail "need python3"

chaos_mesh_ready || fail "TimeChaos CRD missing — run ./scripts/chaos-mesh-up.sh first"

leader_before=$(find_leader) || fail "no leader found before skew"
target=$(pick_follower "$leader_before") || fail "no Ready follower to skew"

if [[ "$ALLOW_LEADER" == "1" ]]; then
  # Escalation path (optional): allow overriding target to the leader after the
  # non-leader case is proven. Default v1 still skews a follower only.
  log "CLOCK_SKEW_ALLOW_LEADER=1 set — v1 still targets follower $target; override by editing inject if escalating"
fi
[[ "$target" != "$leader_before" ]] || fail "internal error: refused to skew leader $leader_before"

log "leader_before=$leader_before target=$target offset=$OFFSET duration=$DURATION trial=$TRIAL"

name="raft-kv-time-skew-t${TRIAL}-$(date -u +%s)"
CHAOS_NAME=$name
secs=$(duration_seconds)
t0=$(date +%s)

kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: TimeChaos
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    ${label_key}: "${label_val}"
spec:
  mode: all
  duration: "${DURATION}"
  timeOffset: "${OFFSET}"
  containerNames:
    - raft-kv
  selector:
    namespaces:
      - ${NS}
    pods:
      ${NS}:
        - ${target}
EOF

log "applied TimeChaos/$name — waiting for inject"
wait_chaos_injected "$name"
start_client_pfs
sleep 3

ok_n=0
fail_n=0
latencies=()
for i in $(seq 1 "$PROBE_PUTS"); do
  key="skew-t${TRIAL}-p${i}-$(date +%s%N)"
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
    log "probe $i/$PROBE_PUTS OK ${ms}ms via $who"
  else
    fail_n=$((fail_n + 1))
    log "probe $i/$PROBE_PUTS FAIL ${ms}ms (${who})"
  fi
done

[[ "$ok_n" -ge 1 ]] || fail "cluster unavailable under clock skew — zero successful PUTs (ok=$ok_n fail=$fail_n)"

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

elapsed=$(( $(date +%s) - t0 ))
remain=$((secs - elapsed))
if [[ "$remain" -gt 0 ]]; then
  log "holding until duration elapses (${remain}s left)"
  sleep "$remain"
fi

log "deleting TimeChaos/$name to roll back clock skew"
kubectl -n "$NS" delete timechaos "$name" --wait=true --timeout=60s >/dev/null
CHAOS_NAME=""
assert_no_leftover_timechaos

start_client_pfs >/dev/null 2>&1 || true
set +e
rec=$(kv_put_any "skew-recover-t${TRIAL}-$(date +%s)" "1")
set -e
log "post-skew recovery write: $rec"
[[ "$rec" == OK* ]] || fail "no successful PUT after clock skew rollback"

log "clock-skew inject complete (target=$target ok=$ok_n fail=$fail_n leader_changed=$leader_changed)"
