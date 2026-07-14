#!/usr/bin/env bash
# chaos-phase-f-28.sh — run ≥5 clean trials per Phase F fault class and print summaries.
#
# Usage:
#   ./scripts/chaos-phase-f-28.sh
#   ./scripts/chaos-phase-f-28.sh --trials 5 --namespace default
#
# Writes under backups/phase-f-28-<stamp>/ (gitignored via backups/).
set -euo pipefail

cd "$(dirname "$0")/.."

TRIALS=5
NS=default
SETTLE=5

while [[ $# -gt 0 ]]; do
  case "$1" in
    --trials) TRIALS=$2; shift 2 ;;
    --namespace|-n) NS=$2; shift 2 ;;
    --settle-sec) SETTLE=$2; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="./backups/phase-f-28-${STAMP}"
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

log() { echo "==> $*" >&2; }
fail() { echo "FAIL: $*" >&2; exit 1; }

[[ "$(kubectl -n "$NS" get pods -l app=raft-kv -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' | grep -c '^true$' || true)" -ge 3 ]] \
  || fail "need 3/3 Ready raft-kv in $NS"
kubectl get crd networkchaos.chaos-mesh.org timechaos.chaos-mesh.org >/dev/null \
  || fail "Chaos Mesh CRDs missing — ./scripts/chaos-mesh-up.sh"
kubectl delete networkchaos,podchaos,timechaos -A -l raft-kv-chaos=true --ignore-not-found >/dev/null 2>&1 || true

# kind inotify (helps Chaos Mesh stay healthy across long runs)
for node in $(kubectl get nodes -o name 2>/dev/null | sed 's|node/||'); do
  docker exec "$node" sh -c \
    'sysctl -w fs.inotify.max_user_watches=524288 fs.inotify.max_user_instances=8192' \
    >/dev/null 2>&1 || true
done

SUMMARY="${OUT}/summary.txt"
: >"$SUMMARY"

run_class() {
  local name=$1
  shift
  local class_dir="${OUT}/${name}"
  mkdir -p "$class_dir"
  log "==== ${name}: ${TRIALS} trials ===="
  set +e
  "$@" --trials "$TRIALS" --namespace "$NS" --settle-sec "$SETTLE" --out-dir "$class_dir"
  local rc=$?
  set -e
  # Locate the TSV produced in class_dir
  local tsv
  tsv=$(ls -1t "$class_dir"/chaos-harness-*.tsv 2>/dev/null | head -1 || true)
  if [[ -z "$tsv" ]]; then
    echo "${name}: FAIL (no TSV, rc=$rc)" | tee -a "$SUMMARY"
    return "$rc"
  fi
  python3 - "$name" "$tsv" "$rc" <<'PY' | tee -a "$SUMMARY"
import sys
name, path, rc = sys.argv[1], sys.argv[2], int(sys.argv[3])
rows = []
with open(path) as f:
    hdr = f.readline().rstrip("\n").split("\t")
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) >= len(hdr):
            rows.append(dict(zip(hdr, parts)))

def pct(vals, p):
    if not vals:
        return float("nan")
    vals = sorted(vals)
    if len(vals) == 1:
        return vals[0]
    k = (len(vals) - 1) * (p / 100.0)
    f = int(k)
    c = min(f + 1, len(vals) - 1)
    return vals[f] if f == c else vals[f] + (vals[c] - vals[f]) * (k - f)

writes = [float(r["mttr_write_s"]) for r in rows]
readys = [float(r["mttr_ready_s"]) for r in rows]
integ = sum(1 for r in rows if r.get("integrity_ok") == "1")
changed = sum(1 for r in rows if r.get("leader_changed") == "1")
n = len(rows)
status = "OK" if rc == 0 and integ == n and n > 0 else "FAIL"
print(f"{name}: {status}  trials={n}  integrity={integ}/{n}  leader_changed={changed}/{n}")
if writes:
    print(f"  mttr_write_s  p50={pct(writes,50):.3f}  p95={pct(writes,95):.3f}  max={max(writes):.3f}")
if readys:
    print(f"  mttr_ready_s  p50={pct(readys,50):.3f}  p95={pct(readys,95):.3f}  max={max(readys):.3f}")
print(f"  tsv={path}")
raise SystemExit(0 if status == "OK" else 1)
PY
}

FAILED=0

wait_cluster_ready() {
  local i n
  kubectl delete networkchaos,podchaos,timechaos -A -l raft-kv-chaos=true --ignore-not-found >/dev/null 2>&1 || true
  for i in $(seq 1 90); do
    n=$(kubectl -n "$NS" get pods -l app=raft-kv -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' \
      | grep -c '^true$' || true)
    if [[ "$n" -ge 3 ]]; then
      return 0
    fi
    sleep 2
  done
  fail "cluster not 3/3 Ready before next class (have ${n:-0})"
}

run_class pod-kill \
  ./scripts/chaos-harness.sh --inject './scripts/chaos-inject-pod-kill.sh' \
  || FAILED=1

wait_cluster_ready
sleep 5

PARTITION_DURATION=20s run_class network-partition \
  ./scripts/chaos-harness.sh --inject './scripts/chaos-inject-network-partition.sh' \
  || FAILED=1

wait_cluster_ready
sleep 5

PACKET_LOSS_PERCENT=25 PACKET_LOSS_DURATION=15s run_class packet-loss \
  ./scripts/chaos-harness.sh --inject './scripts/chaos-inject-packet-loss.sh' \
  || FAILED=1

wait_cluster_ready
sleep 5

CLOCK_SKEW_OFFSET=+5m CLOCK_SKEW_DURATION=15s run_class clock-skew \
  ./scripts/chaos-harness.sh --inject './scripts/chaos-inject-clock-skew.sh' \
  || FAILED=1

echo | tee -a "$SUMMARY"
echo "Phase F #28 raw output: $OUT" | tee -a "$SUMMARY"
[[ "$FAILED" -eq 0 ]] || fail "one or more fault classes failed — see $SUMMARY"
log "all four fault classes: ${TRIALS} clean trials each"
cat "$SUMMARY"
