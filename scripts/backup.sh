#!/usr/bin/env bash
# backup.sh — quiesced follower backup of raft-kv /data for kind / Kubernetes.
#
# WHY NOT A LIVE COPY
# -------------------
# The WAL (raft.log) is append-only and fsync'd per entry, but a concurrent
# tar/cp can still capture a torn last record (length prefix written, payload
# incomplete). NewRaft → Replay panics on that corrupt file (ADR-004). So we
# never tar a live writer's /data.
#
# QUIESCE STRATEGY (distroless has no shell → no kubectl exec kill -STOP)
# ----------------------------------------------------------------------
# 1. Pick a Ready *follower* (never the leader — don't pause the write path).
# 2. Delete that pod so the process stops and releases the RWO PVC.
# 3. Mount the PVC on a short-lived busybox helper and tar the data files.
#    The StatefulSet will recreate the pod, but it stays Pending while the
#    helper holds the volume (RWO) — that is intentional quiesce.
# 4. Delete the helper; the StatefulSet pod binds the PVC and becomes Ready.
#
# Copy order does not matter once the process is stopped; we still pack a
# fixed file list (raft.log, raft.state, raft.snapshot if present).
#
# Usage:
#   ./scripts/backup.sh
#   ./scripts/backup.sh --namespace default --out ./backups
#   ./scripts/backup.sh --pod raft-kv-1          # must be a follower
#
# Requires: kubectl, curl, tar on PATH.
set -euo pipefail

cd "$(dirname "$0")/.."

NS=default
OUT_DIR=./backups
LABEL=app=raft-kv
FORCE_POD=""
METRICS_PORT=2112
HELPER_IMAGE=busybox:1.36

usage() {
  sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) NS=$2; shift 2 ;;
    --out|-o) OUT_DIR=$2; shift 2 ;;
    --pod) FORCE_POD=$2; shift 2 ;;
    --help|-h) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }
need kubectl
need curl
need tar

mkdir -p "$OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)

PF_PIDS=""
HELPER=""
cleanup() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    # shellcheck disable=SC2086
    kill $PF_PIDS 2>/dev/null || true
  fi
  PF_PIDS=""
}
trap cleanup EXIT INT TERM

is_leader() {
  local local_port=$1
  local metrics
  metrics=$(curl -sf --max-time 2 "http://127.0.0.1:${local_port}/metrics" 2>/dev/null || true)
  echo "$metrics" | grep -q '^raft_is_leader 1$'
}

pick_follower() {
  local pods pod i local_port leader="" followers=() ready
  # Only Ready pods: metrics/port-forward need a serving process, and we must
  # leave a majority up when we delete the backup target.
  mapfile -t pods < <(
    kubectl -n "$NS" get pods -l "$LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.containerStatuses[0].ready}{"\n"}{end}' \
      | awk -F'\t' '$2=="true"{print $1}'
  )
  if [[ ${#pods[@]} -lt 2 ]]; then
    echo "need ≥2 Ready pods to backup a follower while keeping quorum (have ${#pods[@]})" >&2
    exit 1
  fi

  cleanup
  i=0
  for pod in "${pods[@]}"; do
    local_port=$((32000 + i))
    kubectl -n "$NS" port-forward "pod/${pod}" "${local_port}:${METRICS_PORT}" >/tmp/raft-kv-backup-pf-${pod}.log 2>&1 &
    PF_PIDS+=" $!"
    i=$((i + 1))
  done
  sleep 2

  i=0
  for pod in "${pods[@]}"; do
    local_port=$((32000 + i))
    if is_leader "$local_port"; then
      leader=$pod
    else
      followers+=("$pod")
    fi
    i=$((i + 1))
  done
  cleanup

  if [[ -n "$FORCE_POD" ]]; then
    if [[ -n "$leader" && "$FORCE_POD" == "$leader" ]]; then
      echo "refusing to backup leader $FORCE_POD — pick a follower (leader=$leader)" >&2
      exit 1
    fi
    local found=0
    for pod in "${pods[@]}"; do
      [[ "$pod" == "$FORCE_POD" ]] && found=1
    done
    if [[ $found -eq 0 ]]; then
      echo "pod $FORCE_POD not found among Ready raft-kv pods" >&2
      exit 1
    fi
    echo "$FORCE_POD"
    return
  fi

  if [[ ${#followers[@]} -eq 0 ]]; then
    echo "no follower found (leader=${leader:-unknown}); need a Ready follower to quiesce" >&2
    exit 1
  fi
  if [[ -z "$leader" ]]; then
    echo "warning: could not identify leader via raft_is_leader; using Ready non-self heuristic" >&2
  fi
  echo "${followers[0]}"
}

start_helper() {
  local name=$1 pvc=$2
  kubectl -n "$NS" delete pod "$name" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  cat <<EOF | kubectl -n "$NS" apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  labels:
    app: raft-kv-backup
spec:
  restartPolicy: Never
  containers:
    - name: backup
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
}

TARGET=$(pick_follower)
PVC="data-${TARGET}"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
ARCHIVE="${OUT_DIR}/raft-kv-${TARGET}-${STAMP}.tar.gz"
# Pod names must be lowercase RFC 1123 (no uppercase T from ISO stamp).
HELPER=$(echo "bk-${TARGET}-${STAMP}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9.-' '-' | sed 's/--*/-/g; s/^-//; s/-$//')
HELPER=${HELPER:0:63}

echo "==> quiesced follower backup"
echo "    namespace: $NS"
echo "    follower: $TARGET"
echo "    pvc:      $PVC"
echo "    archive:  $ARCHIVE"
echo
echo "    consistency: process stopped (pod deleted) before tar — not a live copy."
echo "    files: raft.log, raft.state, raft.snapshot (if present)."
echo

if ! kubectl -n "$NS" get pvc "$PVC" >/dev/null 2>&1; then
  echo "PVC $PVC not found" >&2
  exit 1
fi

echo "==> deleting pod $TARGET (stop process / release RWO PVC)"
kubectl -n "$NS" delete pod "$TARGET" --wait=true --timeout=120s

# Race: StatefulSet recreates TARGET quickly. Helper must win the PVC attach;
# the recreated pod then stays Pending until we delete the helper.
echo "==> starting helper $HELPER (holds PVC so STS pod stays Pending)"
for attempt in 1 2 3 4 5; do
  if kubectl -n "$NS" get pod "$TARGET" >/dev/null 2>&1; then
    phase=$(kubectl -n "$NS" get pod "$TARGET" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$phase" == "Running" || "$phase" == "Pending" ]]; then
      echo "    STS recreated $TARGET ($phase) — deleting so helper can bind PVC (attempt $attempt)"
      kubectl -n "$NS" delete pod "$TARGET" --wait=true --timeout=60s || true
    fi
  fi
  start_helper "$HELPER" "$PVC"
  if kubectl -n "$NS" wait --for=condition=Ready "pod/${HELPER}" --timeout=60s 2>/dev/null; then
    break
  fi
  echo "    helper not Ready yet (attempt $attempt); retrying"
  kubectl -n "$NS" delete pod "$HELPER" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  if [[ $attempt -eq 5 ]]; then
    echo "helper $HELPER failed to become Ready" >&2
    kubectl -n "$NS" describe pod "$HELPER" >&2 || true
    exit 1
  fi
done

echo "==> creating archive from quiesced /data"
kubectl -n "$NS" exec "$HELPER" -- sh -c '
  set -e
  cd /data
  ls -la
  files=""
  for f in raft.log raft.state raft.snapshot; do
    if [ -e "$f" ]; then files="$files $f"; fi
  done
  if [ -z "$files" ]; then
    echo "no raft data files under /data" >&2
    exit 1
  fi
  # shellcheck disable=SC2086
  tar -czf /tmp/backup.tar.gz $files
  ls -la /tmp/backup.tar.gz
'

kubectl -n "$NS" cp "${NS}/${HELPER}:/tmp/backup.tar.gz" "$ARCHIVE"
ls -la "$ARCHIVE"

# Sanity: archive must contain raft.log
if ! tar -tzf "$ARCHIVE" | grep -qx 'raft.log'; then
  echo "archive missing raft.log — aborting" >&2
  exit 1
fi

echo "==> removing helper (releases PVC for StatefulSet)"
kubectl -n "$NS" delete pod "$HELPER" --wait=true --timeout=120s
HELPER=""

echo "==> waiting for $TARGET to return Ready"
for i in $(seq 1 90); do
  ready=$(kubectl -n "$NS" get pod "$TARGET" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)
  if [[ "$ready" == "true" ]]; then
    echo "    $TARGET Ready after ~$((i * 2))s"
    break
  fi
  if [[ $i -eq 90 ]]; then
    echo "timed out waiting for $TARGET Ready" >&2
    kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
    exit 1
  fi
  sleep 2
done
kubectl -n "$NS" get pods -l "$LABEL" -o wide

echo
echo "backup complete: $ARCHIVE"
echo "restore rule reminder: if any quorum survives, prefer wipe-rejoin over restore-from-backup."
