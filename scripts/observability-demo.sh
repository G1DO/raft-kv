#!/usr/bin/env bash
# observability-demo.sh — the M6 "done when" demo, scripted for repeatability.
#
# Against the kind cluster (raft-kv + the observability stack deployed):
#   1. finds the current leader by asking PROMETHEUS (max(raft_is_leader) by pod)
#   2. drives a steady PUT/GET load through the cluster
#   3. force-deletes the leader pod mid-load
#   4. waits for Prometheus to see the new leader and the election counter tick
#   5. prints the Grafana/Loki/Tempo views where the same incident is visible,
#      correlated: latency spike (RED) ↔ leader flip (Raft internals) ↔
#      election_won log line (Loki) ↔ slow-request trace (Tempo, by trace_id)
#
# Requires: kubectl context on the kind cluster; raft-kv release in $RAFT_NS with
# metrics + tracing enabled; observability release in $OBS_NS. Load/leader-kill
# survive the demo — committed data outlives the failover, as in chaos-demo.sh.
set -euo pipefail

RAFT_NS=${RAFT_NS:-default}
OBS_NS=${OBS_NS:-observability}
PROM_LOCAL=19090
PODS=(raft-kv-0 raft-kv-1 raft-kv-2)
declare -A CLIENT=([raft-kv-0]=18080 [raft-kv-1]=18081 [raft-kv-2]=18082)

PF_PIDS=()
cleanup() { for p in "${PF_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT INT TERM

echo "==> port-forwarding Prometheus and the raft-kv pods"
kubectl -n "$OBS_NS" port-forward svc/kps-prometheus "$PROM_LOCAL:9090" >/dev/null 2>&1 &
PF_PIDS+=($!)
for pod in "${PODS[@]}"; do
  kubectl -n "$RAFT_NS" port-forward "pod/$pod" "${CLIENT[$pod]}:8080" >/dev/null 2>&1 &
  PF_PIDS+=($!)
done
sleep 2

# promq QUERY — instant-query Prometheus, print "<pod> <value>" per result row.
promq() {
  curl -sf "http://localhost:$PROM_LOCAL/api/v1/query" --data-urlencode "query=$1" |
    python3 -c 'import sys,json
for r in json.load(sys.stdin)["data"]["result"]:
    print(r["metric"].get("pod","-"), r["value"][1])'
}

# send POD COMMAND — one text-protocol round trip via the pod's port-forward.
send() {
  local pod=$1 cmd=$2 line
  { exec 3<>"/dev/tcp/localhost/${CLIENT[$pod]}"; } 2>/dev/null || { echo DOWN; return; }
  printf '%s\n' "$cmd" >&3
  IFS= read -r line <&3 || line=""
  exec 3>&- 3<&-
  echo "${line%$'\r'}"
}

# write_leader COMMAND — try every pod, return which one accepted (the leader).
write_leader() {
  local cmd=$1
  for _ in $(seq 1 60); do
    for pod in "${PODS[@]}"; do
      [[ "$(send "$pod" "$cmd")" == OK ]] && { echo "$pod"; return 0; }
    done
    sleep 0.5
  done
  return 1
}

echo "==> leader according to Prometheus:"
promq 'max by (pod) (raft_is_leader) == 1' || true
elections_before=$(promq 'sum(raft_elections_total)' | awk '{print $2}')
echo "    elections_total so far: ${elections_before:-0}"

echo "==> driving load (60 writes)"
leader=""
for i in $(seq 1 60); do
  l=$(write_leader "PUT demo-$i value-$i") || { echo "no leader accepting writes"; exit 1; }
  leader=$l
done
echo "    leader (accepting writes): $leader"

echo
echo ">>> force-deleting the leader pod: $leader <<<"
kubectl -n "$RAFT_NS" delete pod "$leader" --grace-period=0 --force >/dev/null 2>&1

echo "==> writing through the failover (watch the latency spike on the RED dashboard)"
start=$(date +%s)
newleader=$(write_leader "PUT demo-failover survived") || { echo "cluster never recovered"; exit 1; }
echo "    new leader: $newleader (recovered in $(( $(date +%s) - start ))s)"
echo "    GET demo-failover -> $(send "$newleader" 'GET demo-failover')"

echo "==> waiting for Prometheus to scrape the flip (interval 15s)..."
for _ in $(seq 1 12); do
  now=$(promq 'max by (pod) (raft_is_leader) == 1' | awk '{print $1}' | head -1)
  [[ -n "$now" && "$now" != "$leader" ]] && break
  sleep 5
done
echo "    leader per Prometheus now:"
promq 'max by (pod) (raft_is_leader) == 1' || true
elections_after=$(promq 'sum(raft_elections_total)' | awk '{print $2}')
echo "    elections_total: ${elections_before:-0} -> ${elections_after:-?}"

cat <<EOF

============================= where to look =============================
Grafana (kubectl -n $OBS_NS port-forward svc/grafana 3000:80):
  d/raftkv-raft-internals  leader timeline flips $leader -> $newleader;
                           elections rate ticks; commit p99 spikes
  d/raftkv-red             PUT latency spike + timeout/not_leader blip
  d/raftkv-slo             5m burn-rate spike, 1h line barely moves

Loki (Grafana > Explore, datasource Loki):
  {namespace="$RAFT_NS", pod=~"raft-kv-.*"} | json | msg =~ "election_won|stepping_down|rpc_failed"
  {namespace="$RAFT_NS", pod=~"raft-kv-.*"} | json | audit="true"
  Any request line carries trace_id — click the derived-field link to jump to
  its Tempo trace.

Tempo (Grafana > Explore, datasource Tempo):
  Search service raft-kv, sort by duration: the slowest raftkv.request spans
  during the failover show the time inside raft.replicate_commit_apply.
=========================================================================
EOF
