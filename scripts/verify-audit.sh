#!/usr/bin/env bash
# verify-audit.sh — Phase E #21 app security audit verification (live cluster).
#
# Proves ADR-012 audit events reach Loki with the agreed schema and without
# leaking keys/values:
#   - labeled client PUT on leader → audit.client.mutate outcome=allow
#   - client PUT on follower → audit.client.mutate outcome=deny
#   - plaintext to peer :9090 (when mTLS on) → audit.peer.tls_fail
#   - NetworkPolicy block (when enforced) → not observable in Loki (documented)
#
# Usage:
#   ./scripts/verify-audit.sh
#   ./scripts/verify-audit.sh --namespace default --obs-namespace observability
#
# Requires: kubectl, curl, python3; raft-kv + observability releases on cluster.
# Rebuild/redeploy raft-kv after Phase E #19 emit code before expecting audit lines.
set -euo pipefail

cd "$(dirname "$0")/.."

RELEASE=raft-kv
RAFT_NS=default
OBS_NS=observability
CLIENT_NS=raft-kv-clients
LOKI_LOCAL=13100
RAFT_TLS_PF=19090

CLIENT_POD=audit-client-$$
DENY_POD=audit-np-deny-$$

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) RAFT_NS=$2; shift 2 ;;
    --obs-namespace) OBS_NS=$2; shift 2 ;;
    -h|--help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

command -v kubectl >/dev/null || { echo "need kubectl" >&2; exit 1; }
command -v curl >/dev/null || { echo "need curl" >&2; exit 1; }
command -v python3 >/dev/null || { echo "need python3" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }
info() { echo "==> $*"; }

RAFT_SVC=raft-kv
MARKER="audit-$$"
SECRET="secret-$$"
KEY="key-${MARKER}"

PF_PIDS=()
cleanup() {
  for p in "${PF_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  kubectl delete pod -n "$CLIENT_NS" -l audit-verify=client --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete pod -n "$RAFT_NS" -l audit-verify=deny --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

now_ns() { python3 -c 'import time; print(int(time.time()*1e9))'; }

cni_enforces_policy() {
  kubectl get pods -A -l k8s-app=calico-node --no-headers 2>/dev/null | grep -q Running && return 0
  kubectl get pods -n kube-system -l k8s-app=cilium --no-headers 2>/dev/null | grep -q Running && return 0
  return 1
}

peer_tls_enabled() {
  kubectl get statefulset -n "$RAFT_NS" "$RELEASE" -o yaml 2>/dev/null | grep -q 'raft-tls-cert'
}

wait_pod() {
  kubectl wait --for=condition=Ready "pod/$2" -n "$1" --timeout=90s >/dev/null
}

tcp_probe() {
  local ns=$1 pod=$2 host=$3 port=$4
  kubectl exec -n "$ns" "$pod" -- sh -c "nc -zv -w 3 '$host' '$port'" >/dev/null 2>&1
}

kv_cmd() {
  local ns=$1 pod=$2 host=$3 cmd=$4
  kubectl exec -n "$ns" "$pod" -- sh -c \
    "printf '%s\n' '$cmd' | nc -w 5 '$host' 8080" 2>/dev/null | tr -d '\r' | tail -1
}

loki_query() {
  local query=$1 start_ns=$2
  local end_ns
  end_ns=$(now_ns)
  curl -sf -G "http://127.0.0.1:${LOKI_LOCAL}/loki/api/v1/query_range" \
    --data-urlencode "query=${query}" \
    --data-urlencode "start=${start_ns}" \
    --data-urlencode "end=${end_ns}" \
    --data-urlencode "limit=100" \
    --data-urlencode "direction=forward"
}

# wait_loki_audit QUERY START_NS EVENT OUTCOME ACTION [forbidden substring...]
wait_loki_audit() {
  local query=$1 start_ns=$2 event=$3 outcome=$4 action=$5
  shift 5
  local forbidden=("$@")
  local i raw
  for i in $(seq 1 15); do
    raw=$(loki_query "$query" "$start_ns" 2>/dev/null) || raw=""
    if [[ -n "$raw" ]]; then
      if printf '%s' "$raw" | python3 - "$event" "$outcome" "$action" "${forbidden[@]}" <<'PY'
import json, sys

event_want = sys.argv[1]
outcome_want = sys.argv[2]
action_want = sys.argv[3]
forbidden = sys.argv[4:]

raw = sys.stdin.read()
if not raw.strip():
    sys.exit(1)

try:
    data = json.loads(raw)
except json.JSONDecodeError:
    sys.exit(1)

lines = []
for stream in data.get("data", {}).get("result", []):
    for _ts, line in stream.get("values", []):
        lines.append(line)

if not lines:
    sys.exit(1)

for line in lines:
    for bad in forbidden:
        if bad and bad in line:
            print(f"forbidden substring in log: {bad}", file=sys.stderr)
            sys.exit(3)
    try:
        rec = json.loads(line)
    except json.JSONDecodeError:
        continue
    if rec.get("msg") != "security_audit":
        continue
    if rec.get("audit") not in (True, "true"):
        continue
    if rec.get("event") != event_want:
        continue
    if outcome_want and rec.get("outcome") != outcome_want:
        continue
    if action_want and rec.get("action") != action_want:
        continue
    for field in ("event", "outcome", "node", "remote", "action"):
        if field not in rec or rec[field] in ("", None):
            print(f"missing field {field}", file=sys.stderr)
            sys.exit(4)
    if event_want.startswith("audit.client.") and rec.get("actor") != "unauthenticated":
        print(f"bad actor: {rec.get('actor')}", file=sys.stderr)
        sys.exit(5)
    sys.exit(0)

sys.exit(2)
PY
      then
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}

info "preflight: cluster, raft-kv, and Loki"
kubectl cluster-info >/dev/null 2>&1 || fail "kubectl cannot reach a cluster"

READY=$(kubectl get pods -n "$RAFT_NS" -l app=raft-kv --no-headers 2>/dev/null | awk '$2=="1/1"{n++} END{print n+0}')
[[ "$READY" -ge 3 ]] || fail "expected 3 Ready raft-kv pods in $RAFT_NS (got $READY)"

kubectl get svc -n "$OBS_NS" loki >/dev/null 2>&1 || fail "Loki service not found in $OBS_NS (deploy observability stack)"

kubectl -n "$OBS_NS" port-forward svc/loki "$LOKI_LOCAL:3100" >/dev/null 2>&1 &
PF_PIDS+=($!)
sleep 2
curl -sf "http://127.0.0.1:${LOKI_LOCAL}/ready" >/dev/null || fail "Loki not ready on port-forward"
ok "Loki reachable"

kubectl create namespace "$CLIENT_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl run "$CLIENT_POD" -n "$CLIENT_NS" --restart=Never --image=busybox:1.36 \
  -l raft-kv.client=true,audit-verify=client --command -- sleep 600 >/dev/null
wait_pod "$CLIENT_NS" "$CLIENT_POD"
ok "labeled client pod ready"

info "case 1: allowed client PUT (leader)"
START_ALLOW=$(now_ns)
LEADER_POD=""
LEADER_HOST=""
FOLLOWER_HOST=""
for i in 0 1 2; do
  host="${RELEASE}-${i}.${RAFT_SVC}.${RAFT_NS}.svc.cluster.local"
  resp=$(kv_cmd "$CLIENT_NS" "$CLIENT_POD" "$host" "PUT ${KEY} ${SECRET}")
  if [[ "$resp" == OK ]]; then
    LEADER_POD="${RELEASE}-${i}"
    LEADER_HOST="$host"
  elif [[ "$resp" == NOT_LEADER* ]]; then
    FOLLOWER_HOST="$host"
  fi
done
[[ -n "$LEADER_POD" ]] || fail "no leader accepted PUT (cluster unhealthy?)"
ok "leader ${LEADER_POD} accepted PUT"

QUERY="{namespace=\"${RAFT_NS}\", pod=~\"${RELEASE}-.*\"} | json | msg=\"security_audit\""
wait_loki_audit "$QUERY" "$START_ALLOW" "audit.client.mutate" "allow" "PUT" "$SECRET" "$KEY" \
  || fail "no allow mutate audit in Loki after leader PUT"
ok "Loki: audit.client.mutate outcome=allow (no key/value leak)"

info "case 2: denied client PUT (follower)"
[[ -n "$FOLLOWER_HOST" ]] || fail "could not find a follower (all pods leader?)"
START_DENY=$(now_ns)
resp=$(kv_cmd "$CLIENT_NS" "$CLIENT_POD" "$FOLLOWER_HOST" "PUT ${KEY}-deny ${SECRET}-deny")
[[ "$resp" == NOT_LEADER* ]] || fail "expected NOT_LEADER from follower, got: ${resp:-empty}"

QUERY="{namespace=\"${RAFT_NS}\", pod=~\"${RELEASE}-.*\"} | json | msg=\"security_audit\""
wait_loki_audit "$QUERY" "$START_DENY" "audit.client.mutate" "deny" "PUT" "${SECRET}-deny" \
  || fail "no deny mutate audit in Loki after follower PUT"
ok "Loki: audit.client.mutate outcome=deny"

info "case 3: peer TLS handshake failure (when mTLS enabled)"
if peer_tls_enabled; then
  TARGET="${RELEASE}-0.${RAFT_SVC}.${RAFT_NS}.svc.cluster.local"
  kubectl -n "$RAFT_NS" port-forward "pod/${RELEASE}-0" "$RAFT_TLS_PF:9090" >/dev/null 2>&1 &
  PF_PIDS+=($!)
  sleep 1
  START_TLS=$(now_ns)
  { exec 3<>/dev/tcp/127.0.0.1/$RAFT_TLS_PF; printf '{"Type":"RequestVote"}\n' >&3; } 2>/dev/null || true
  exec 3<&- 3>&- 2>/dev/null || true
  sleep 1

  QUERY="{namespace=\"${RAFT_NS}\", pod=\"${RELEASE}-0\"} | json | msg=\"security_audit\""
  wait_loki_audit "$QUERY" "$START_TLS" "audit.peer.tls_fail" "deny" "handshake" \
    || fail "no peer TLS audit in Loki after plaintext :9090 probe"
  ok "Loki: audit.peer.tls_fail action=handshake on ${RELEASE}-0"
else
  ok "peer mTLS not enabled — skipped TLS audit case (set tls.enabled=true for full coverage)"
fi

info "case 4: NetworkPolicy deny not observable from Loki"
if cni_enforces_policy; then
  kubectl run "$DENY_POD" -n "$RAFT_NS" --restart=Never --image=busybox:1.36 \
    -l audit-verify=deny --command -- sleep 300 >/dev/null
  wait_pod "$RAFT_NS" "$DENY_POD"
  if tcp_probe "$RAFT_NS" "$DENY_POD" "${RELEASE}-0.${RAFT_SVC}" 8080; then
    fail "unlabeled pod reached :8080 — NP not enforced; cannot test non-observable case"
  fi
  START_NP=$(now_ns)
  # No app log expected — verify no fresh client mutate audit tied to this window.
  sleep 3
  raw=$(loki_query "{namespace=\"${RAFT_NS}\", pod=~\"${RELEASE}-.*\"} | json | msg=\"security_audit\"" "$START_NP" 2>/dev/null || true)
  if printf '%s' "$raw" | python3 -c '
import json, sys
data = json.load(sys.stdin)
for stream in data.get("data", {}).get("result", []):
    for _ts, line in stream.get("values", []):
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if rec.get("audit") in (True, "true") and rec.get("event") == "audit.client.mutate":
            sys.exit(1)
sys.exit(0)
' 2>/dev/null; then
    ok "NP-blocked connect produced no client mutate audit (not observable from Loki)"
  else
    fail "unexpected client mutate audit after NP block — check probe isolation"
  fi
else
  ok "no enforcing CNI — NP non-observable case skipped (install Calico/Cilium for full coverage)"
fi

echo
ok "audit verification complete"
