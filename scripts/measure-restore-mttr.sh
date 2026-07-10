#!/usr/bin/env bash
# measure-restore-mttr.sh — wall-clock MTTR for restore paths (a)/(b)/(c) on kind.
#
# Endpoint (me/todos.md #11):
#   start  = damage begins (wipe / disaster wipe / full restore start)
#   end    = target Ready (D1 /readyz) AND recovery proof:
#            PUT a sentinel via the leader, then recovered pod's
#            raft_applied_index ≥ leader's raft_applied_index after that PUT.
#
# Usage:
#   ./scripts/measure-restore-mttr.sh              # all paths, 5 trials each
#   ./scripts/measure-restore-mttr.sh --path wipe --trials 5
#   ./scripts/measure-restore-mttr.sh --path restore --trials 5
#   ./scripts/measure-restore-mttr.sh --path full --trials 5
#   ./scripts/measure-restore-mttr.sh --load 500   # PUTs before measuring
#
# Writes a TSV summary to backups/mttr-restore-<stamp>.tsv (gitignored).
set -euo pipefail

cd "$(dirname "$0")/.."

NS=default
LABEL=app=raft-kv
HELPER_IMAGE=busybox:1.36
# Per-pod client port-forwards (Service PF cannot follow in-cluster NOT_LEADER hints).
CLIENT_BASE=18080
METRICS_BASE=37000
TRIALS=5
PATHS="wipe restore full"
LOAD_PUTS=500
OUT_DIR=./backups
ARCHIVE=""
declare -A CLIENT_PORT=()

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --trials) TRIALS=$2; shift 2 ;;
    --path) PATHS=$2; shift 2 ;;
    --load) LOAD_PUTS=$2; shift 2 ;;
    --archive) ARCHIVE=$2; shift 2 ;;
    --namespace|-n) NS=$2; shift 2 ;;
    --help|-h) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing: $1" >&2; exit 1; }; }
need kubectl
need curl
need python3
need tar
need date

mkdir -p "$OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
TSV="${OUT_DIR}/mttr-restore-${STAMP}.tsv"
echo -e "path\ttrial\tseconds\tready_seconds\tproof_seconds\ttarget\tnotes" >"$TSV"

PF_PIDS=""
cleanup_pf() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    # shellcheck disable=SC2086
    kill $PF_PIDS 2>/dev/null || true
  fi
  PF_PIDS=""
}
trap cleanup_pf EXIT INT TERM

log() { echo "==> $*" >&2; }

wait_all_ready() {
  local i ready n
  n=$(kubectl -n "$NS" get sts -l "$LABEL" -o jsonpath='{.items[0].spec.replicas}' 2>/dev/null || echo 3)
  for i in $(seq 1 120); do
    ready=$(kubectl -n "$NS" get pods -l "$LABEL" -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' \
      | grep -c '^true$' || true)
    if [[ "$ready" -ge "$n" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for $n Ready pods (have ${ready:-0})" >&2
  kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
  return 1
}

start_client_pf() {
  cleanup_pf
  CLIENT_PORT=()
  local i=0 p port need=0
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    local phase
    phase=$(kubectl -n "$NS" get pod "$p" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Running" ]] || continue
    port=$((CLIENT_BASE + i))
    CLIENT_PORT[$p]=$port
    kubectl -n "$NS" port-forward "pod/${p}" "${port}:8080" >/tmp/mttr-client-${p}.log 2>&1 &
    PF_PIDS+=" $!"
    need=$((need + 1))
    i=$((i + 1))
  done
  if [[ "$need" -lt 1 ]]; then
    echo "no Running raft-kv pods for client PF" >&2
    return 1
  fi
  local up=0
  for i in $(seq 1 50); do
    up=0
    for p in "${!CLIENT_PORT[@]}"; do
      port=${CLIENT_PORT[$p]}
      if python3 -c "import socket; s=socket.create_connection(('127.0.0.1',${port}),0.5); s.close()" 2>/dev/null; then
        up=$((up + 1))
      fi
    done
    if [[ "$up" -ge "$need" ]]; then
      return 0
    fi
    sleep 0.2
  done
  echo "client port-forwards failed (up=$up need=$need)" >&2
  return 1
}

# kv_do_pod POD CMD — one round trip to a specific pod's local PF.
kv_do_pod() {
  local pod=$1 cmd=$2 port
  port=${CLIENT_PORT[$pod]:-}
  if [[ -z "$port" ]]; then
    echo "DOWN"
    return
  fi
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

# kv_put_ok KEY VAL — try every pod until a leader accepts.
kv_put_ok() {
  local key=$1 val=$2 p resp
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    [[ -n "${CLIENT_PORT[$p]:-}" ]] || continue
    resp=$(kv_do_pod "$p" "PUT $key $val")
    if [[ "$resp" == "OK" ]]; then
      return 0
    fi
  done
  return 1
}

kv_get() {
  local key=$1 p resp
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    resp=$(kv_do_pod "$p" "GET $key")
    if [[ "$resp" != NOT_LEADER* && "$resp" != ERROR* && "$resp" != DOWN ]]; then
      echo "$resp"
      return 0
    fi
  done
  echo "ERROR"
  return 1
}

load_dataset() {
  local n=$1 i ok t
  log "loading dataset: $n PUTs"
  start_client_pf
  # Wait until some leader accepts writes (fresh cluster / post-heal).
  ok=0
  for t in $(seq 1 60); do
    if kv_put_ok "__mttr_probe__" "1"; then
      ok=1
      break
    fi
    if (( t % 10 == 0 )); then
      start_client_pf || true
    fi
    sleep 0.5
  done
  if [[ "$ok" -ne 1 ]]; then
    echo "cluster not accepting writes before load" >&2
    kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
    exit 1
  fi
  for i in $(seq 1 "$n"); do
    ok=0
    for t in $(seq 1 20); do
      if kv_put_ok "load-$i" "v$i"; then
        ok=1
        break
      fi
      if (( t % 5 == 0 )); then
        start_client_pf || true
      fi
      sleep 0.2
    done
    if [[ "$ok" -ne 1 ]]; then
      echo "load failed at PUT load-$i" >&2
      exit 1
    fi
    if (( i % 100 == 0 )); then
      echo "    loaded $i/$n" >&2
    fi
  done
  log "load complete ($n keys)"
}

metric_gauge() {
  local pod=$1 name=$2 port local_port
  local_port=$((METRICS_BASE + ${pod##*-}))
  kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:2112" >/tmp/mttr-m-${pod}.log 2>&1 &
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

find_leader_pod() {
  local p is
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    is=$(metric_gauge "$p" raft_is_leader || echo 0)
    if [[ "$is" == "1" || "$is" == "1.0" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

pick_follower() {
  local leader p
  leader=$(find_leader_pod) || { echo "no leader" >&2; return 1; }
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    if [[ "$p" != "$leader" ]]; then
      local ready
      ready=$(kubectl -n "$NS" get pod "$p" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)
      if [[ "$ready" == "true" ]]; then
        echo "$p"
        return 0
      fi
    fi
  done
  echo "no Ready follower" >&2
  return 1
}

# recovery_proof TARGET — PUT sentinel; require target applied_index ≥ leader applied_index
# after that PUT, and GET sentinel == 1. Refreshes client PFs (pods churn under wipe).
# Echoes proof_seconds.
recovery_proof() {
  local target=$1
  local t0 key leader applied_leader applied_target i put_ok=0 got
  t0=$(date +%s.%N)
  key="mttr-sentinel-$(date -u +%Y%m%dT%H%M%S)-$RANDOM"

  for i in $(seq 1 90); do
    # Port-forwards die when pods are recreated — refresh periodically.
    if (( i == 1 || i % 10 == 0 )); then
      start_client_pf || true
    fi
    if kv_put_ok "$key" "1"; then
      put_ok=1
      break
    fi
    sleep 0.5
  done
  if [[ "$put_ok" -ne 1 ]]; then
    echo "sentinel PUT failed after retries (key=$key)" >&2
    kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
    return 1
  fi

  leader=$(find_leader_pod) || leader=""
  if [[ -z "$leader" ]]; then
    echo "no leader after sentinel PUT" >&2
    return 1
  fi
  applied_leader=$(metric_gauge "$leader" raft_applied_index)
  if [[ -z "$applied_leader" ]]; then
    echo "could not read leader applied_index" >&2
    return 1
  fi
  applied_leader=${applied_leader%%.*}

  for i in $(seq 1 120); do
    applied_target=$(metric_gauge "$target" raft_applied_index || echo 0)
    applied_target=${applied_target%%.*}
    if [[ -n "$applied_target" && "$applied_target" -ge "$applied_leader" ]]; then
      start_client_pf || true
      got=$(kv_get "$key" || true)
      if [[ "$got" == "1" ]]; then
        python3 - "$t0" <<'PY'
import sys, time
t0 = float(sys.argv[1])
print(f"{time.time() - t0:.3f}")
PY
        return 0
      fi
      echo "    indices ok (target=$applied_target leader=$applied_leader) but GET $key -> [$got]; retrying" >&2
    fi
    sleep 1
  done
  echo "proof timeout: target=$target applied=${applied_target:-?} leader_applied=$applied_leader key=$key" >&2
  return 1
}

ensure_backup() {
  if [[ -n "$ARCHIVE" && -f "$ARCHIVE" ]]; then
    ARCHIVE=$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE")
    log "using existing archive $ARCHIVE"
    return 0
  fi
  log "taking quiesced follower backup"
  ./scripts/backup.sh --namespace "$NS" --out "$OUT_DIR"
  ARCHIVE=$(ls -1t "$OUT_DIR"/raft-kv-*.tar.gz | head -1)
  if [[ -z "$ARCHIVE" || ! -f "$ARCHIVE" ]]; then
    echo "backup produced no archive" >&2
    exit 1
  fi
  log "archive: $ARCHIVE"
}

# Low-level empty PVC (disaster prep). Bypasses wipe quorum guard.
empty_pvc() {
  local target=$1
  local pvc="data-${target}"
  local helper stamp
  stamp=$(date -u +%Y%m%dt%H%M%Sz | tr '[:upper:]' '[:lower:]')
  helper=$(echo "mt-${target}-${stamp}" | tr -c 'a-z0-9.-' '-' | sed 's/--*/-/g; s/^-//; s/-$//')
  helper=${helper:0:63}
  # Truncation can reintroduce a trailing '-'; strip again.
  helper=${helper%-}
  log "disaster-empty $target ($pvc) via $helper"
  kubectl -n "$NS" delete pod "$target" --ignore-not-found --wait=true --timeout=120s || true
  local attempt phase
  for attempt in 1 2 3 4 5; do
    if kubectl -n "$NS" get pod "$target" >/dev/null 2>&1; then
      phase=$(kubectl -n "$NS" get pod "$target" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      if [[ "$phase" == "Running" || "$phase" == "Pending" ]]; then
        kubectl -n "$NS" delete pod "$target" --wait=true --timeout=60s || true
      fi
    fi
    kubectl -n "$NS" delete pod "$helper" --ignore-not-found --wait=true >/dev/null 2>&1 || true
    cat <<EOF | kubectl -n "$NS" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${helper}
  labels:
    app: raft-kv-mttr
spec:
  restartPolicy: Never
  containers:
    - name: wipe
      image: ${HELPER_IMAGE}
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${pvc}
EOF
    if kubectl -n "$NS" wait --for=condition=Ready "pod/${helper}" --timeout=60s >/dev/null 2>&1; then
      kubectl -n "$NS" exec "$helper" -- sh -c 'rm -rf /data/* /data/.[!.]* 2>/dev/null; true'
      kubectl -n "$NS" delete pod "$helper" --wait=true --timeout=120s >/dev/null
      return 0
    fi
    kubectl -n "$NS" delete pod "$helper" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  done
  echo "failed to empty $pvc" >&2
  return 1
}

destroy_cluster_data() {
  local p
  log "destroying all raft-kv PVC data (disaster)"
  for p in raft-kv-0 raft-kv-1 raft-kv-2; do
    empty_pvc "$p"
  done
  # Let STS recreate empty pods; they will not form a useful quorum with empty WALs
  # until we restore. Wait briefly for pods to exist (may be NotReady).
  sleep 3
  kubectl -n "$NS" delete pod -l app=raft-kv-mttr --ignore-not-found --wait=true >/dev/null 2>&1 || true
}

now_s() { date +%s.%N; }

elapsed() {
  python3 - "$1" <<'PY'
import sys, time
print(f"{time.time() - float(sys.argv[1]):.3f}")
PY
}

record() {
  local path=$1 trial=$2 total=$3 ready=$4 proof=$5 target=$6 notes=$7
  echo -e "${path}\t${trial}\t${total}\t${ready}\t${proof}\t${target}\t${notes}" | tee -a "$TSV" >/dev/null
  echo "    RESULT path=$path trial=$trial total=${total}s ready=${ready}s proof=${proof}s target=$target" >&2
}

run_wipe_trial() {
  local trial=$1 target t0 t_ready ready_s proof_s total_s
  wait_all_ready
  cleanup_pf
  target=$(pick_follower)
  log "wipe trial $trial target=$target"
  t0=$(now_s)
  ./scripts/restore.sh wipe --namespace "$NS" --pod "$target"
  ready_s=$(elapsed "$t0")
  t_ready=$(now_s)
  wait_all_ready
  recovery_proof "$target" >/dev/null
  proof_s=$(elapsed "$t_ready")
  total_s=$(elapsed "$t0")
  record wipe "$trial" "$total_s" "$ready_s" "$proof_s" "$target" "quorum-alive"
  cleanup_pf
  wait_all_ready
}

run_restore_trial() {
  # Path (b): disaster → restore archive onto one seed only. Clock covers restore
  # until seed Ready + proof. Empty peers rejoin via normal AE (not timed as wipe).
  local trial=$1 target=raft-kv-0 t0 t_ready ready_s proof_s total_s
  ensure_backup
  destroy_cluster_data
  log "restore trial $trial seed=$target archive=$ARCHIVE"
  t0=$(now_s)
  ./scripts/restore.sh restore --namespace "$NS" --pod "$target" --archive "$ARCHIVE" --force-disaster
  ready_s=$(elapsed "$t0")
  t_ready=$(now_s)
  recovery_proof "$target" >/dev/null
  proof_s=$(elapsed "$t_ready")
  total_s=$(elapsed "$t0")
  record restore "$trial" "$total_s" "$ready_s" "$proof_s" "$target" "force-disaster"
  # Heal peers for the next trial (not part of measured window).
  local p
  for p in raft-kv-1 raft-kv-2; do
    ./scripts/restore.sh wipe --namespace "$NS" --pod "$p" 2>/dev/null || empty_pvc "$p" || true
  done
  wait_all_ready || true
}

run_full_trial() {
  local trial=$1 target=raft-kv-0 t0 t_ready ready_s proof_s total_s
  ensure_backup
  destroy_cluster_data
  log "full trial $trial seed=$target archive=$ARCHIVE"
  t0=$(now_s)
  ./scripts/restore.sh full --namespace "$NS" --pod "$target" --archive "$ARCHIVE"
  ready_s=$(elapsed "$t0")
  t_ready=$(now_s)
  recovery_proof "$target" >/dev/null
  proof_s=$(elapsed "$t_ready")
  total_s=$(elapsed "$t0")
  record full "$trial" "$total_s" "$ready_s" "$proof_s" "$target" "disaster-full"
  wait_all_ready
}

summarize() {
  python3 - "$TSV" <<'PY'
import sys
from collections import defaultdict
path = sys.argv[1]
rows = defaultdict(list)
with open(path) as f:
    next(f)
    for line in f:
        p, trial, total, ready, proof, target, notes = line.rstrip("\n").split("\t")
        rows[p].append(float(total))
print()
print("MTTR summary (total seconds: Ready + proof)")
print(f"{'path':<10} {'n':>3} {'min':>8} {'p50':>8} {'mean':>8} {'max':>8}")
for p in ("wipe", "restore", "full"):
    xs = sorted(rows.get(p, []))
    if not xs:
        continue
    n = len(xs)
    mean = sum(xs) / n
    p50 = xs[(n - 1) // 2]
    print(f"{p:<10} {n:>3} {xs[0]:8.3f} {p50:8.3f} {mean:8.3f} {xs[-1]:8.3f}")
print(f"\nraw: {path}")
PY
}

# --- main ------------------------------------------------------------------
log "MTTR measure: paths=[$PATHS] trials=$TRIALS load=$LOAD_PUTS"
# Wipe needs a healthy quorum. Disaster paths destroy PVCs first — don't block
# on Ready when we're about to wipe everything (and load=0).
need_ready=0
if [[ "$LOAD_PUTS" -gt 0 ]]; then
  need_ready=1
fi
for path in $PATHS; do
  if [[ "$path" == "wipe" ]]; then
    need_ready=1
  fi
done
if [[ "$need_ready" -eq 1 ]]; then
  wait_all_ready
fi
if [[ "$LOAD_PUTS" -gt 0 ]]; then
  load_dataset "$LOAD_PUTS"
fi
ensure_backup

for path in $PATHS; do
  case "$path" in
    wipe)
      for t in $(seq 1 "$TRIALS"); do run_wipe_trial "$t"; done
      ;;
    restore)
      for t in $(seq 1 "$TRIALS"); do run_restore_trial "$t"; done
      ;;
    full)
      for t in $(seq 1 "$TRIALS"); do run_full_trial "$t"; done
      ;;
    *)
      echo "unknown path: $path (wipe|restore|full)" >&2
      exit 1
      ;;
  esac
done

summarize
log "done → $TSV"
