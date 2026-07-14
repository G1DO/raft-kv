#!/usr/bin/env bash
# chaos-harness.sh — Phase F #22 reusable Chaos Mesh measurement harness (kind).
#
# Runs N bounded trials of: steady unique writes → inject fault → measure recovery.
# Experiments (#23–#26) supply --inject; this script owns load, timing, integrity,
# percentiles, and ADR-013 cleanup proof.
#
# Metrics (per trial):
#   mttr_write_s  = inject_start → first committed PUT after inject_start
#   mttr_ready_s  = inject_start → StatefulSet 3/3 Ready (separate)
# Also records inject end, leader before/after, Ready count samples, integrity.
#
# Usage:
#   ./scripts/chaos-harness.sh --trials 3 --inject 'true'          # noop smoke
#   ./scripts/chaos-harness.sh --trials 5 --inject './scripts/chaos-inject-pod-kill.sh'
#   ./scripts/chaos-harness.sh --trials 5 --inject 'kubectl …' --namespace default
#
# Writes (gitignored):
#   backups/chaos-harness-<stamp>.tsv
#   backups/chaos-harness-<stamp>.events.jsonl
#
# Requires: kubectl, python3; 3/3 raft-kv Ready. Chaos Mesh optional until --inject
# creates CRs (cleanup still checks for leftovers labeled raft-kv-chaos=true).
set -euo pipefail

cd "$(dirname "$0")/.."

NS=default
LABEL=app=raft-kv
RELEASE=raft-kv
CLIENT_BASE=19080
TRIALS=3
INJECT_CMD=""
WRITE_INTERVAL_MS=200
SETTLE_SEC=2
READY_TIMEOUT_SEC=180
OUT_DIR=./backups
CHAOS_LABEL=raft-kv-chaos=true
CLIENT_BASE_ROOT=19080

usage() {
  sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --trials) TRIALS=$2; shift 2 ;;
    --inject) INJECT_CMD=$2; shift 2 ;;
    --namespace|-n) NS=$2; shift 2 ;;
    --write-interval-ms) WRITE_INTERVAL_MS=$2; shift 2 ;;
    --settle-sec) SETTLE_SEC=$2; shift 2 ;;
    --out-dir) OUT_DIR=$2; shift 2 ;;
    --help|-h) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need kubectl
need python3
need date

[[ -n "$INJECT_CMD" ]] || {
  echo "missing --inject <command> (use --inject 'true' for a noop smoke run)" >&2
  usage 1
}

mkdir -p "$OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
TSV="${OUT_DIR}/chaos-harness-${STAMP}.tsv"
EVENTS="${OUT_DIR}/chaos-harness-${STAMP}.events.jsonl"
ACKED="${OUT_DIR}/chaos-harness-${STAMP}.acked"
RUN_TAG="chaos-${STAMP}"

echo -e "trial\tinject_start_unix\tinject_end_unix\tfirst_write_unix\tready_3_unix\tmttr_write_s\tmttr_ready_s\tleader_before\tleader_after\tleader_changed\tacked_before\tintegrity_ok\tready_at_inject_end\tnotes" >"$TSV"

declare -A CLIENT_PORT=()
PF_PIDS=""
LOAD_PID=""
LOAD_STOP=""

log() { echo "==> $*" >&2; }
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*" >&2; }

event() {
  # event KIND [json fields as key=value...]
  local kind=$1
  shift
  python3 - "$kind" "$@" <<'PY' >>"$EVENTS"
import json, sys, time
kind = sys.argv[1]
rec = {"ts_unix": time.time(), "kind": kind}
for arg in sys.argv[2:]:
    if "=" not in arg:
        continue
    k, v = arg.split("=", 1)
    try:
        if v.lower() in ("true", "false"):
            rec[k] = v.lower() == "true"
        elif v.replace(".", "", 1).isdigit():
            rec[k] = float(v) if "." in v else int(v)
        else:
            rec[k] = v
    except Exception:
        rec[k] = v
print(json.dumps(rec, separators=(",", ":")))
PY
}

now_unix() { python3 -c 'import time; print(f"{time.time():.6f}")'; }

stop_port_forwards() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    # shellcheck disable=SC2086
    kill $PF_PIDS 2>/dev/null || true
  fi
  PF_PIDS=""
}

cleanup_all() {
  stop_load
  stop_port_forwards
}

trap cleanup_all EXIT INT TERM

ready_count() {
  kubectl -n "$NS" get pods -l "$LABEL" -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' 2>/dev/null \
    | grep -c '^true$' || true
}

wait_ready() {
  local need want timeout=$READY_TIMEOUT_SEC i n=0
  want=$(kubectl -n "$NS" get sts "$RELEASE" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo 3)
  for i in $(seq 1 "$timeout"); do
    n=$(ready_count)
    if [[ "$n" -ge "$want" ]]; then
      echo "$n"
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for ${want}/3 Ready (have ${n})" >&2
  kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
  return 1
}

start_client_pf() {
  # Refresh only port-forwards — do not stop the background load writer.
  stop_port_forwards
  # Drop any stale listeners on this trial's client ports (previous hung PFs).
  local i=0 p port need=0
  for i in 0 1 2; do
    port=$((CLIENT_BASE + i))
    fuser -k "${port}/tcp" >/dev/null 2>&1 || true
  done
  i=0
  CLIENT_PORT=()
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    local phase
    phase=$(kubectl -n "$NS" get pod "$p" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Running" ]] || continue
    port=$((CLIENT_BASE + i))
    CLIENT_PORT[$p]=$port
    kubectl -n "$NS" port-forward "pod/${p}" "${port}:8080" >/tmp/chaos-harness-${p}-${port}.log 2>&1 &
    PF_PIDS+=" $!"
    need=$((need + 1))
    i=$((i + 1))
  done
  [[ "$need" -ge 1 ]] || { echo "no Running raft-kv pods for client PF" >&2; return 1; }
  local up=0 t
  for t in $(seq 1 50); do
    up=0
    for p in "${!CLIENT_PORT[@]}"; do
      port=${CLIENT_PORT[$p]}
      if python3 -c "import socket; s=socket.create_connection(('127.0.0.1',${port}),0.5); s.close()" 2>/dev/null; then
        up=$((up + 1))
      fi
    done
    [[ "$up" -ge "$need" ]] && return 0
    sleep 0.2
  done
  echo "client port-forwards failed (up=$up need=$need)" >&2
  return 1
}

kv_do_pod() {
  local pod=$1 cmd=$2 port
  port=${CLIENT_PORT[$pod]:-}
  [[ -n "$port" ]] || { echo "DOWN"; return; }
  python3 - "$port" "$cmd" <<'PY'
import socket, sys
port = int(sys.argv[1]); cmd = sys.argv[2]
try:
    s = socket.create_connection(("127.0.0.1", port), 5)
    s.sendall((cmd + "\n").encode())
    s.settimeout(10)
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

# kv_put_ok KEY VAL — try every pod; echo accepting pod on success.
kv_put_ok() {
  local key=$1 val=$2 p resp
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    [[ -n "${CLIENT_PORT[$p]:-}" ]] || continue
    resp=$(kv_do_pod "$p" "PUT $key $val")
    if [[ "$resp" == "OK" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

kv_get() {
  local key=$1 p resp
  for p in "${RELEASE}-0" "${RELEASE}-1" "${RELEASE}-2"; do
    [[ -n "${CLIENT_PORT[$p]:-}" ]] || continue
    resp=$(kv_do_pod "$p" "GET $key")
    if [[ "$resp" != NOT_LEADER* && "$resp" != ERROR* && "$resp" != DOWN && -n "$resp" ]]; then
      echo "$resp"
      return 0
    fi
  done
  echo "ERROR"
  return 1
}

find_leader_by_put() {
  local i p
  for i in $(seq 1 30); do
    if p=$(kv_put_ok "__chaos_probe_${RUN_TAG}__" "1" 2>/dev/null); then
      echo "$p"
      return 0
    fi
    if (( i % 10 == 0 )); then
      start_client_pf >/dev/null 2>&1 || true
    fi
    sleep 0.2
  done
  return 1
}

# start_load TRIAL — background unique writes; records acked KEY VAL lines.
start_load() {
  local trial=$1
  LOAD_STOP="${OUT_DIR}/chaos-harness-${STAMP}.stop.${trial}"
  rm -f "$LOAD_STOP"
  (
    seq=0
    while [[ ! -f "$LOAD_STOP" ]]; do
      seq=$((seq + 1))
      key="ch-${RUN_TAG}-t${trial}-s${seq}"
      val="v${seq}"
      if leader=$(kv_put_ok "$key" "$val" 2>/dev/null); then
        printf '%s %s %s\n' "$key" "$val" "$leader" >>"${ACKED}.${trial}"
      fi
      python3 -c "import time; time.sleep(${WRITE_INTERVAL_MS}/1000.0)"
    done
  ) &
  LOAD_PID=$!
}

stop_load() {
  if [[ -n "${LOAD_PID:-}" ]]; then
    touch "${LOAD_STOP:-/tmp/chaos-harness.stop}" 2>/dev/null || true
    kill "$LOAD_PID" 2>/dev/null || true
    wait "$LOAD_PID" 2>/dev/null || true
    LOAD_PID=""
  fi
}

# Wait until a PUT succeeds after t0; print "unix leader".
wait_first_write_after() {
  local t0=$1 deadline=$((READY_TIMEOUT_SEC * 2)) i leader ts
  for i in $(seq 1 "$deadline"); do
    if (( i == 1 || i % 5 == 0 )); then
      start_client_pf >/dev/null 2>&1 || true
    fi
    ts=$(now_unix)
    if leader=$(kv_put_ok "ch-${RUN_TAG}-post-${i}-$(date +%s)" "1" 2>/dev/null); then
      echo "$ts $leader"
      return 0
    fi
    if (( i % 25 == 0 )); then
      log "still waiting for first post-fault write (attempt $i/${deadline}, ready=$(ready_count))"
    fi
    sleep 0.25
  done
  return 1
}

# integrity_check TRIAL LIMIT — verify a sample of the first LIMIT acked keys
# (pre-inject baseline). LIMIT empty => all rows in the snapshot file.
integrity_check() {
  local trial=$1 limit=${2:-}
  local snap="${ACKED}.${trial}.pre" key val got miss=0
  [[ -f "$snap" ]] || { echo 1; return 0; }
  mapfile -t sample < <(python3 - "$snap" "${limit:-0}" <<'PY'
import sys
path, lim = sys.argv[1], int(sys.argv[2])
rows = open(path).read().splitlines()
if lim > 0:
    rows = rows[:lim]
if len(rows) <= 50:
    out = rows
else:
    out = rows[:25] + rows[-25:]
for r in out:
    print(r)
PY
)
  [[ ${#sample[@]} -gt 0 ]] || { echo 1; return 0; }
  for line in "${sample[@]}"; do
    key=${line%% *}
    rest=${line#* }
    val=${rest%% *}
    got=$(kv_get "$key" || echo ERROR)
    if [[ "$got" != "$val" ]]; then
      miss=$((miss + 1))
      event integrity_miss trial="$trial" key="$key" want="$val" got="$got"
    fi
  done
  if [[ "$miss" -eq 0 ]]; then
    echo 1
  else
    echo 0
  fi
}

# ADR-013 cleanup checklist — failing cleanup fails the run.
assert_cleanup() {
  local leftovers
  # Best-effort: kinds may be absent if Chaos Mesh CRDs are not installed yet.
  leftovers=$(kubectl get podchaos,networkchaos,timechaos,iochaos,httpchaos,stresschaos \
    -A -l "$CHAOS_LABEL" --no-headers 2>/dev/null | wc -l | tr -d ' ') || leftovers=0

  if [[ "${leftovers:-0}" -gt 0 ]]; then
    echo "cleanup: deleting leftover Chaos CRs with label $CHAOS_LABEL" >&2
    kubectl delete podchaos,networkchaos,timechaos,iochaos,httpchaos,stresschaos \
      -A -l "$CHAOS_LABEL" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  fi

  leftovers=$(kubectl get podchaos,networkchaos,timechaos,iochaos,httpchaos,stresschaos \
    -A -l "$CHAOS_LABEL" --no-headers 2>/dev/null | wc -l | tr -d ' ') || leftovers=0
  [[ "${leftovers:-0}" -eq 0 ]] || fail "ADR-013 cleanup: Chaos CRs still present with $CHAOS_LABEL"

  local n
  n=$(wait_ready) || fail "ADR-013 cleanup: cluster not 3/3 Ready"
  stop_load
  event cleanup_ok ready="$n"
  ok "cleanup: no labeled Chaos CRs; Ready ${n}/3; harness load stopped"
}

run_trial() {
  local trial=$1
  local inject_start inject_end first_write ready_3 leader_before leader_after
  local mttr_write mttr_ready leader_changed integrity ready_at_end notes="" acked_before
  local first_line fw_ts fw_leader

  log "trial $trial/$TRIALS: settle + baseline writes"
  start_client_pf || fail "port-forward setup failed"
  : >"${ACKED}.${trial}"
  start_load "$trial"
  sleep "$SETTLE_SEC"

  leader_before=$(find_leader_by_put || echo unknown)
  # Snapshot acked keys before inject — integrity only checks these.
  cp "${ACKED}.${trial}" "${ACKED}.${trial}.pre"
  acked_before=$(wc -l <"${ACKED}.${trial}.pre" | tr -d ' ')
  event trial_baseline trial="$trial" leader="$leader_before" acked="$acked_before" ready="$(ready_count)"

  inject_start=$(now_unix)
  event inject_start trial="$trial" unix="$inject_start"
  log "trial $trial: inject start ($inject_start) — $INJECT_CMD"
  set +e
  # Export helpers inject scripts may use (namespace + chaos resource label).
  export CHAOS_HARNESS_NS="$NS" CHAOS_HARNESS_LABEL="$CHAOS_LABEL" CHAOS_HARNESS_TRIAL="$trial"
  bash -c "$INJECT_CMD"
  local inj_rc=$?
  set -e
  inject_end=$(now_unix)
  event inject_end trial="$trial" unix="$inject_end" rc="$inj_rc"

  # Scrub leftovers immediately — failed injects used to leave NetworkChaos up.
  leftovers=$(kubectl get podchaos,networkchaos,timechaos,iochaos,httpchaos,stresschaos \
    -A -l "$CHAOS_LABEL" --no-headers 2>/dev/null | wc -l | tr -d ' ') || leftovers=0
  if [[ "${leftovers:-0}" -gt 0 ]]; then
    log "trial $trial: deleting ${leftovers} leftover Chaos CR(s) after inject"
    kubectl delete podchaos,networkchaos,timechaos,iochaos,httpchaos,stresschaos \
      -A -l "$CHAOS_LABEL" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  fi

  if [[ "$inj_rc" -ne 0 ]]; then
    wait_ready >/dev/null 2>&1 || true
    fail "trial $trial: inject exited $inj_rc"
  fi

  ready_at_end=$(ready_count)
  event ready_sample trial="$trial" when=inject_end ready="$ready_at_end"

  # New client ports after inject — avoids colliding with inject-script PFs / stale FDs.
  # Restart load so the writer subshell picks up updated CLIENT_PORT (bash fork copy).
  stop_load
  CLIENT_BASE=$((CLIENT_BASE_ROOT + (trial * 10) + (RANDOM % 40)))
  start_client_pf || fail "trial $trial: client port-forward refresh failed after inject"
  start_load "$trial"

  log "trial $trial: wait first post-fault committed write (client_base=$CLIENT_BASE)"
  first_line=$(wait_first_write_after "$inject_start") \
    || fail "trial $trial: no committed write after inject within timeout"
  fw_ts=${first_line%% *}
  fw_leader=${first_line#* }
  first_write=$fw_ts
  event first_write trial="$trial" unix="$first_write" leader="$fw_leader"

  log "trial $trial: wait 3/3 Ready"
  wait_ready >/dev/null || fail "trial $trial: Ready wait failed"
  ready_3=$(now_unix)
  # Refresh PFs after heal (pods may have restarted).
  start_client_pf || true
  leader_after=$(find_leader_by_put || echo unknown)

  mttr_write=$(python3 -c "print(f'{float('$first_write')-float('$inject_start'):.6f}')")
  mttr_ready=$(python3 -c "print(f'{float('$ready_3')-float('$inject_start'):.6f}')")
  if [[ "$leader_before" != "$leader_after" ]]; then
    leader_changed=1
  else
    leader_changed=0
  fi

  stop_load
  sleep 0.5
  integrity=$(integrity_check "$trial" "$acked_before")
  [[ "$integrity" == "1" ]] || notes="${notes:+$notes;}integrity_fail"

  event trial_done trial="$trial" mttr_write_s="$mttr_write" mttr_ready_s="$mttr_ready" \
    leader_before="$leader_before" leader_after="$leader_after" integrity_ok="$integrity"

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$trial" "$inject_start" "$inject_end" "$first_write" "$ready_3" \
    "$mttr_write" "$mttr_ready" "$leader_before" "$leader_after" "$leader_changed" \
    "$acked_before" "$integrity" "$ready_at_end" "$notes" >>"$TSV"

  ok "trial $trial: mttr_write=${mttr_write}s mttr_ready=${mttr_ready}s leader ${leader_before}->${leader_after} integrity=$integrity"

  assert_cleanup
  # Brief quiet between trials so Ready/load state is stable.
  sleep "$SETTLE_SEC"
}

# main
log "chaos harness: trials=$TRIALS ns=$NS inject=[$INJECT_CMD] tag=$RUN_TAG"
kubectl cluster-info >/dev/null 2>&1 || fail "kubectl cannot reach a cluster"
[[ "$(ready_count)" -ge 3 ]] || fail "expected 3 Ready raft-kv pods before run"
event run_start trials="$TRIALS" namespace="$NS" stamp="$STAMP"

for t in $(seq 1 "$TRIALS"); do
  run_trial "$t"
done

stop_load
assert_cleanup

python3 - "$TSV" <<'PY'
import sys
path = sys.argv[1]
rows = []
with open(path) as f:
    header = f.readline().rstrip("\n").split("\t")
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) < len(header):
            continue
        rows.append(dict(zip(header, parts)))

def pct(vals, p):
    if not vals:
        return float("nan")
    vals = sorted(vals)
    if len(vals) == 1:
        return vals[0]
    k = (len(vals) - 1) * (p / 100.0)
    f = int(k)
    c = min(f + 1, len(vals) - 1)
    if f == c:
        return vals[f]
    return vals[f] + (vals[c] - vals[f]) * (k - f)

writes = [float(r["mttr_write_s"]) for r in rows]
readys = [float(r["mttr_ready_s"]) for r in rows]
integ = sum(1 for r in rows if r.get("integrity_ok") == "1")
n = len(rows)
print()
print(f"chaos-harness summary  trials={n}  integrity_ok={integ}/{n}")
print(f"  mttr_write_s  p50={pct(writes,50):.3f}  p95={pct(writes,95):.3f}  max={max(writes):.3f}" if writes else "  mttr_write_s  (none)")
print(f"  mttr_ready_s  p50={pct(readys,50):.3f}  p95={pct(readys,95):.3f}  max={max(readys):.3f}" if readys else "  mttr_ready_s  (none)")
print(f"  tsv={path}")
if integ != n:
    sys.exit(2)
PY

ok "chaos harness complete — see $TSV"
event run_end stamp="$STAMP"
