#!/usr/bin/env bash
# restore.sh — recover a raft-kv node (or the whole cluster) on Kubernetes/kind.
#
# DECISION RULE (read this before any flag)
# ----------------------------------------
#   Quorum still alive?  →  ALWAYS wipe-rejoin (mode: wipe). Never restore a
#                           backup into a live cluster.
#   Full cluster loss?   →  restore-from-backup is allowed (mode: restore or
#                           full). Disaster only.
#
# Why wipe when quorum survives
# -----------------------------
# Emptying the PVC makes NewRaft treat the node as fresh; it catches up via
# AppendEntries / InstallSnapshot. Wipe also discards term/vote — that is only
# safe while a majority of *other* voters is intact (otherwise a wiped node
# that had voted this term could double-vote if you later restore its state).
#
# Why restore-from-backup is dangerous when quorum survives
# --------------------------------------------------------
# A backup embeds a possibly-stale membership config. Restoring it into a
# cluster that later did AddServer/RemoveServer can resurrect a removed peer
# or split-brain if that node wins an election on the stale config.
#
# Modes
# -----
#   wipe     (a) Empty one pod's PVC; STS recreates; node rejoins from peers.
#            Requires --pod. Safe only if enough *other* Ready pods remain for
#            majority (for N=3, need ≥2 Ready among the others before wipe).
#
#   restore  (b) Unpack a backup.tar.gz onto one pod's PVC. Disaster only.
#            Requires --pod and --archive. Refuses if ≥ majority Ready pods
#            already exist (use wipe instead), unless --force-disaster.
#
#   full     (c) Disaster: restore archive onto --pod (seed), wipe the other
#            raft-kv PVCs so they rejoin the seed. Requires --archive and
#            --pod (seed). Implies --force-disaster.
#
# Usage:
#   ./scripts/restore.sh wipe --pod raft-kv-2
#   ./scripts/restore.sh restore --pod raft-kv-0 --archive backups/….tar.gz --force-disaster
#   ./scripts/restore.sh full --pod raft-kv-0 --archive backups/….tar.gz
#
# Requires: kubectl, tar on PATH.
set -euo pipefail

cd "$(dirname "$0")/.."

NS=default
LABEL=app=raft-kv
HELPER_IMAGE=busybox:1.36
MODE=""
POD=""
ARCHIVE=""
FORCE_DISASTER=0
REPLICA_COUNT=""

usage() {
  sed -n '2,48p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

if [[ $# -lt 1 ]]; then usage 1; fi
MODE=$1
shift

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) NS=$2; shift 2 ;;
    --pod) POD=$2; shift 2 ;;
    --archive) ARCHIVE=$2; shift 2 ;;
    --force-disaster) FORCE_DISASTER=1; shift ;;
    --replicas) REPLICA_COUNT=$2; shift 2 ;;
    --help|-h) usage 0 ;;
    *) echo "unknown arg: $1" >&2; usage 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }
need kubectl
need tar

case "$MODE" in
  wipe|restore|full) ;;
  *) echo "mode must be wipe|restore|full (got: $MODE)" >&2; usage 1 ;;
esac

if [[ -z "$POD" ]]; then
  echo "--pod is required" >&2
  exit 1
fi
if [[ "$MODE" == "restore" || "$MODE" == "full" ]]; then
  if [[ -z "$ARCHIVE" ]]; then
    echo "--archive is required for mode=$MODE" >&2
    exit 1
  fi
  if [[ ! -f "$ARCHIVE" ]]; then
    echo "archive not found: $ARCHIVE" >&2
    exit 1
  fi
  ARCHIVE=$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE")
fi
if [[ "$MODE" == "full" ]]; then
  FORCE_DISASTER=1
fi

pvc_for() { echo "data-$1"; }

ready_pods() {
  kubectl -n "$NS" get pods -l "$LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.containerStatuses[0].ready}{"\n"}{end}' \
    | awk -F'\t' '$2=="true"{print $1}'
}

count_ready() {
  ready_pods | wc -l | tr -d ' '
}

all_member_pods() {
  # Prefer StatefulSet ordinals when we know replica count; else discover PVCs.
  local n i
  if [[ -n "$REPLICA_COUNT" ]]; then
    n=$REPLICA_COUNT
  else
    n=$(kubectl -n "$NS" get sts -l "$LABEL" -o jsonpath='{.items[0].spec.replicas}' 2>/dev/null || true)
    if [[ -z "$n" ]]; then
      n=$(kubectl -n "$NS" get pvc -o name 2>/dev/null | grep -c 'data-raft-kv-' || true)
    fi
  fi
  if [[ -z "$n" || "$n" -lt 1 ]]; then
    echo "could not determine replica count; pass --replicas N" >&2
    exit 1
  fi
  # Derive name prefix from --pod (raft-kv-2 → raft-kv)
  local prefix=${POD%-*}
  for i in $(seq 0 $((n - 1))); do
    echo "${prefix}-${i}"
  done
}

majority_of() {
  local n=$1
  echo $(( (n / 2) + 1 ))
}

helper_name() {
  local suffix=$1
  local stamp
  stamp=$(date -u +%Y%m%dt%H%M%Sz | tr '[:upper:]' '[:lower:]')
  local name
  name=$(echo "rs-${suffix}-${stamp}" | tr -c 'a-z0-9.-' '-' | sed 's/--*/-/g; s/^-//; s/-$//')
  echo "${name:0:63}"
}

# hold_pvc_with_helper POD — delete pod, start helper mounting its PVC, wait Ready.
# Prints helper name on stdout only; all progress goes to stderr (callers capture
# the name with helper=$(hold_pvc_with_helper …)).
hold_pvc_with_helper() {
  local target=$1
  local pvc helper attempt phase
  pvc=$(pvc_for "$target")
  helper=$(helper_name "$target")

  if ! kubectl -n "$NS" get pvc "$pvc" >/dev/null 2>&1; then
    echo "PVC $pvc not found" >&2
    exit 1
  fi

  echo "==> deleting pod $target (release RWO PVC $pvc)" >&2
  kubectl -n "$NS" delete pod "$target" --ignore-not-found --wait=true --timeout=120s >&2 || true

  for attempt in 1 2 3 4 5; do
    if kubectl -n "$NS" get pod "$target" >/dev/null 2>&1; then
      phase=$(kubectl -n "$NS" get pod "$target" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      if [[ "$phase" == "Running" || "$phase" == "Pending" ]]; then
        echo "    STS recreated $target ($phase) — deleting so helper can bind (attempt $attempt)" >&2
        kubectl -n "$NS" delete pod "$target" --wait=true --timeout=60s >&2 || true
      fi
    fi
    kubectl -n "$NS" delete pod "$helper" --ignore-not-found --wait=true >/dev/null 2>&1 || true
    cat <<EOF | kubectl -n "$NS" apply -f - >&2
apiVersion: v1
kind: Pod
metadata:
  name: ${helper}
  labels:
    app: raft-kv-restore
spec:
  restartPolicy: Never
  containers:
    - name: restore
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
    if kubectl -n "$NS" wait --for=condition=Ready "pod/${helper}" --timeout=60s >&2; then
      echo "$helper"
      return 0
    fi
    echo "    helper not Ready (attempt $attempt); retrying" >&2
    kubectl -n "$NS" delete pod "$helper" --ignore-not-found --wait=true >/dev/null 2>&1 || true
  done
  echo "helper failed to become Ready for $pvc" >&2
  exit 1
}

wipe_pvc_via_helper() {
  local target=$1
  local helper
  helper=$(hold_pvc_with_helper "$target")
  # Defensive: capture must be a single DNS-1123 label (no newlines/spaces).
  if [[ ! "$helper" =~ ^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$ ]]; then
    echo "internal error: bad helper name from hold_pvc_with_helper: [$helper]" >&2
    exit 1
  fi
  echo "==> wiping /data on $target (via $helper)"
  kubectl -n "$NS" exec "$helper" -- sh -c 'rm -rf /data/* /data/.[!.]* 2>/dev/null; ls -la /data'
  echo "==> releasing helper $helper"
  kubectl -n "$NS" delete pod "$helper" --wait=true --timeout=120s
}

restore_archive_via_helper() {
  local target=$1
  local archive=$2
  local helper remote
  if ! tar -tzf "$archive" | grep -qx 'raft.log'; then
    echo "archive missing raft.log: $archive" >&2
    exit 1
  fi
  helper=$(hold_pvc_with_helper "$target")
  if [[ ! "$helper" =~ ^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$ ]]; then
    echo "internal error: bad helper name from hold_pvc_with_helper: [$helper]" >&2
    exit 1
  fi
  remote="/tmp/restore-$(basename "$archive")"
  echo "==> copying archive into $helper"
  kubectl -n "$NS" cp "$archive" "${NS}/${helper}:${remote}"
  echo "==> wiping then extracting onto /data"
  kubectl -n "$NS" exec "$helper" -- sh -c "
    set -e
    rm -rf /data/* /data/.[!.]* 2>/dev/null || true
    tar -xzf '${remote}' -C /data
    ls -la /data
  "
  echo "==> releasing helper $helper"
  kubectl -n "$NS" delete pod "$helper" --wait=true --timeout=120s
}

wait_pod_ready() {
  local target=$1
  local i ready
  echo "==> waiting for $target Ready"
  # Disaster restore can sit NotReady through election + no-op commit + DNS
  # settle; budget ~6 minutes (180 × 2s).
  for i in $(seq 1 180); do
    ready=$(kubectl -n "$NS" get pod "$target" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)
    if [[ "$ready" == "true" ]]; then
      echo "    $target Ready after ~$((i * 2))s"
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for $target Ready" >&2
  kubectl -n "$NS" get pods -l "$LABEL" -o wide >&2 || true
  exit 1
}

print_rule_banner() {
  echo "============================================================"
  echo " DECISION RULE"
  echo "   quorum survives  →  wipe-rejoin only"
  echo "   full cluster loss → restore / full (disaster)"
  echo "============================================================"
  echo
}

# --- mode implementations ---------------------------------------------------

do_wipe() {
  local ready others maj n
  print_rule_banner
  mapfile -t members < <(all_member_pods)
  n=${#members[@]}
  maj=$(majority_of "$n")
  ready=$(count_ready)

  # Count Ready pods among *others* (excluding target). After wipe, those must
  # still form a majority for the cluster to keep serving while target catches up.
  others=0
  while read -r p; do
    [[ "$p" == "$POD" ]] && continue
    others=$((others + 1))
  done < <(ready_pods)

  echo "==> mode=wipe (rebuild-from-peers)"
  echo "    target:     $POD"
  echo "    members:    $n  majority: $maj"
  echo "    Ready now:  $ready  (Ready others excluding target: $others)"
  echo
  echo "    Wipe discards term/vote on $POD. Safe only while a quorum of others"
  echo "    remains intact so this node rejoins as brand-new."
  echo

  if [[ "$others" -lt "$maj" ]]; then
    echo "refusing wipe: Ready others ($others) < majority ($maj)." >&2
    echo "If the whole cluster is dead, use: restore --force-disaster or full" >&2
    exit 1
  fi

  wipe_pvc_via_helper "$POD"
  wait_pod_ready "$POD"
  kubectl -n "$NS" get pods -l "$LABEL" -o wide
  echo
  echo "wipe complete: $POD should catch up from peers (AppendEntries/InstallSnapshot)."
}

do_restore() {
  local ready n maj
  print_rule_banner
  mapfile -t members < <(all_member_pods)
  n=${#members[@]}
  maj=$(majority_of "$n")
  ready=$(count_ready)

  echo "==> mode=restore (from backup) — DISASTER PATH"
  echo "    target:  $POD"
  echo "    archive: $ARCHIVE"
  echo "    Ready:   $ready / $n (majority=$maj)"
  echo
  echo "    WARNING: restored data may embed a stale membership config."
  echo "    If any quorum survives, use wipe instead."
  echo

  if [[ "$ready" -ge "$maj" && "$FORCE_DISASTER" -ne 1 ]]; then
    echo "refusing restore: $ready Ready pods ≥ majority ($maj)." >&2
    echo "Quorum appears alive — use: ./scripts/restore.sh wipe --pod $POD" >&2
    echo "Only if you are sure this is full-cluster loss: add --force-disaster" >&2
    exit 1
  fi
  if [[ "$FORCE_DISASTER" -eq 1 ]]; then
    echo "    --force-disaster set: proceeding despite Ready count."
  fi

  restore_archive_via_helper "$POD" "$ARCHIVE"
  wait_pod_ready "$POD"
  kubectl -n "$NS" get pods -l "$LABEL" -o wide
  echo
  echo "restore complete on $POD. Prefer wipe-rejoin for any other damaged members."
}

do_full() {
  local m seed_done=0
  print_rule_banner
  mapfile -t members < <(all_member_pods)

  echo "==> mode=full (disaster: seed from backup, wipe-rejoin others)"
  echo "    seed:    $POD"
  echo "    archive: $ARCHIVE"
  echo "    members: ${members[*]}"
  echo
  echo "    WARNING: full-cluster restore. Stale membership in the backup can"
  echo "    resurrect removed peers if this seed wins an election on old config."
  echo

  # Seed first
  restore_archive_via_helper "$POD" "$ARCHIVE"
  wait_pod_ready "$POD"
  seed_done=1

  for m in "${members[@]}"; do
    [[ "$m" == "$POD" ]] && continue
    echo "==> wipe-rejoin other member $m"
    wipe_pvc_via_helper "$m"
    wait_pod_ready "$m"
  done

  kubectl -n "$NS" get pods -l "$LABEL" -o wide
  echo
  echo "full restore complete: seed=$POD from archive; others wiped and rejoined."
}

case "$MODE" in
  wipe) do_wipe ;;
  restore) do_restore ;;
  full) do_full ;;
esac
