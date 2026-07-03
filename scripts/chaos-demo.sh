#!/usr/bin/env bash
# chaos-demo.sh — induce a leader failure and watch Raft re-elect and keep serving.
#
# Brings up a local 3-node cluster, writes a value through the leader, SIGKILLs the
# leader, and shows a new leader elected and accepting writes within a couple of
# seconds — with the committed data surviving the failover. This is the
# demonstrable core of the project in ~15 seconds.
#
#   node1  client 8081  raft 9001        node2  client 8082  raft 9002
#   node3  client 8083  raft 9003        (data under data/chaos-demo/)
#
# To capture an asciinema cast for the README:
#   asciinema rec demo.cast -c ./scripts/chaos-demo.sh
# then upload it and paste the URL into README.md's Benchmarks section.
set -euo pipefail

cd "$(dirname "$0")/.."

PORTS=(8081 8082 8083)
declare -A RAFT=([8081]=9001 [8082]=9002 [8083]=9003)
declare -A ID=([8081]=node1 [8082]=node2 [8083]=node3)
DATA=data/chaos-demo

rm -rf "$DATA"
go build -o raft-kv ./cmd/server

declare -A PID
cleanup() {
  echo
  echo "stopping cluster..."
  for p in "${PID[@]:-}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# peers_for CLIENT_PORT — the --peers value (every other node) for that node.
peers_for() {
  local self=$1 out=()
  for p in "${PORTS[@]}"; do
    [[ $p == "$self" ]] && continue
    out+=("${ID[$p]}@localhost:${RAFT[$p]}@localhost:$p")
  done
  local IFS=,
  echo "${out[*]}"
}

# start_node CLIENT_PORT — launch one node, logging to data/chaos-demo/<id>.log.
start_node() {
  local port=$1
  ./raft-kv --id "${ID[$port]}" --addr "localhost:$port" \
    --raft-addr "localhost:${RAFT[$port]}" \
    --metrics-addr "localhost:$((port + 12000))" \
    --peers "$(peers_for "$port")" --data "$DATA/${ID[$port]}" \
    >"$DATA/${ID[$port]}.log" 2>&1 &
  PID[$port]=$!
  disown   # keep bash from printing an async "Killed" line when we SIGKILL it
}

# send CLIENT_PORT COMMAND — one line of the text protocol; echoes the reply.
send() {
  local port=$1 cmd=$2 line
  # The { } 2>/dev/null wrapper suppresses bash's "Connection refused" noise while
  # nodes are still binding their ports during startup (redirections apply L→R, so
  # a trailing 2>/dev/null on the bare exec would print before taking effect).
  { exec 3<>"/dev/tcp/localhost/$port"; } 2>/dev/null || { echo "DOWN"; return; }
  printf '%s\n' "$cmd" >&3
  IFS= read -r line <&3 || line=""
  exec 3>&- 3<&-
  echo "${line%$'\r'}"
}

# find_leader — echo the client port whose node accepts a write directly (the
# leader; followers reply NOT_LEADER). Polls until one appears.
find_leader() {
  for _ in $(seq 1 100); do
    for p in "${PORTS[@]}"; do
      [[ -n "${PID[$p]:-}" ]] || continue
      [[ "$(send "$p" 'PUT __demo_probe__ 1')" == "OK" ]] && { echo "$p"; return 0; }
    done
    sleep 0.1
  done
  return 1
}

mkdir -p "$DATA"
echo "starting 3-node cluster..."
for p in "${PORTS[@]}"; do start_node "$p"; done

echo -n "waiting for a leader... "
leader=$(find_leader) || { echo "no leader elected"; exit 1; }
echo "${ID[$leader]} (port $leader)"

echo "write through the leader:  PUT demo hello  ->  $(send "$leader" 'PUT demo hello')"

echo
echo ">>> kill -9 the leader (${ID[$leader]}) <<<"
kill -9 "${PID[$leader]}"
unset 'PID[$leader]'

echo -n "re-electing... "
start=$(date +%s%3N)
newleader=$(find_leader) || { echo "no new leader elected"; exit 1; }
elapsed=$(( $(date +%s%3N) - start ))
echo "${ID[$newleader]} (port $newleader) in ${elapsed} ms"

echo "committed data survived the failover:"
echo "  GET demo        ->  $(send "$newleader" 'GET demo')"
echo "the new leader accepts writes:"
echo "  PUT demo world  ->  $(send "$newleader" 'PUT demo world')"
echo "  GET demo        ->  $(send "$newleader" 'GET demo')"
echo
echo "done — re-elected and serving after losing the leader."
