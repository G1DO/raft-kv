#!/usr/bin/env bash
# cluster-up.sh — launch a local 3-node raft-kv cluster.
#
#   node1  client localhost:8081  raft localhost:9001  metrics localhost:2112  data data/node1
#   node2  client localhost:8082  raft localhost:9002  metrics localhost:2113  data data/node2
#   node3  client localhost:8083  raft localhost:9003  metrics localhost:2114  data data/node3
#
# Each node is told about the other two via --peers (id@raftAddr@clientAddr).
# Ctrl-C tears the whole cluster down.
set -euo pipefail

cd "$(dirname "$0")/.."

go build -o raft-kv ./cmd/server

PIDS=()
cleanup() {
  echo
  echo "stopping cluster..."
  for pid in "${PIDS[@]}"; do
    kill -INT "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

start_node() {
  local id=$1 client=$2 raft=$3 metrics=$4 peers=$5
  ./raft-kv --id "$id" --addr "$client" --raft-addr "$raft" \
            --metrics-addr "$metrics" --peers "$peers" --data "data/$id" &
  PIDS+=("$!")
  echo "started $id  (client $client, raft $raft, metrics http://$metrics/metrics, pid ${PIDS[-1]})"
}

start_node node1 localhost:8081 localhost:9001 localhost:2112 "node2@localhost:9002@localhost:8082,node3@localhost:9003@localhost:8083"
start_node node2 localhost:8082 localhost:9002 localhost:2113 "node1@localhost:9001@localhost:8081,node3@localhost:9003@localhost:8083"
start_node node3 localhost:8083 localhost:9003 localhost:2114 "node1@localhost:9001@localhost:8081,node2@localhost:9002@localhost:8082"

echo
echo "3-node cluster up. Try:  printf 'PUT foo bar\\n' | nc localhost 8081"
echo "(a non-leader replies NOT_LEADER <addr>; retry against that address.)"
echo "Press Ctrl-C to stop."
wait
