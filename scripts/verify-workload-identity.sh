#!/usr/bin/env bash
# verify-workload-identity.sh — Phase C #13 ServiceAccount hygiene (static + live).
#
# Ensures raft-kv pods use a dedicated ServiceAccount with no projected API token
# and no workload RBAC grants.
#
# Usage:
#   ./scripts/verify-workload-identity.sh
#   ./scripts/verify-workload-identity.sh --live --namespace default
#
# Requires: helm. --live also needs kubectl + a reachable cluster.
set -euo pipefail

cd "$(dirname "$0")/.."

CHART=deploy/helm/raft-kv
RELEASE=raft-kv
NAMESPACE=default
LIVE=

while [[ $# -gt 0 ]]; do
  case "$1" in
    --live) LIVE=1; shift ;;
    --namespace|-n) NAMESPACE=$2; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

command -v helm >/dev/null || { echo "need helm" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }

# PUT/GET via labeled client pod; follows NOT_LEADER hints to the current leader.
kv_via_client() {
  local client_ns=$1 client_pod=$2 host=$3 cmd=$4
  local tries=0 resp leader_host h
  while [[ $tries -lt 6 ]]; do
    resp=$(kubectl exec -n "$client_ns" "$client_pod" -- sh -c \
      "printf '${cmd}\n' | nc -w 5 '${host}' 8080" 2>/dev/null || true)
    if [[ "$resp" == OK* ]] || [[ "$resp" == *persisted* ]] || [[ "$resp" == NOT_FOUND ]]; then
      echo "$resp"
      return 0
    fi
    if [[ "$resp" == NOT_LEADER* ]]; then
      h=$(echo "$resp" | awk '{print $2}' | cut -d: -f1)
      if [[ "$h" == *".svc."* ]]; then
        host=$h
      else
        # Hint is <pod>.<service> (e.g. raft-kv-1.raft-kv) — append namespace + cluster DNS suffix.
        host="${h}.${NAMESPACE}.svc.cluster.local"
      fi
      tries=$((tries + 1))
      continue
    fi
    echo "$resp"
    return 1
  done
  echo "$resp"
  return 1
}

render() {
  helm template "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" \
    --set replicaCount=3 \
    --set tls.enabled=true
}

echo "==> render chart (workload SA enabled)"
MANIFEST=$(render)

echo "==> dedicated ServiceAccount rendered"
if ! echo "$MANIFEST" | grep -A20 'kind: ServiceAccount' | grep -q 'automountServiceAccountToken: false'; then
  fail "workload ServiceAccount missing automountServiceAccountToken: false"
fi
if ! echo "$MANIFEST" | grep -A5 'kind: ServiceAccount' | grep -q 'name: raft-kv'; then
  fail "expected workload ServiceAccount named raft-kv"
fi
ok "ServiceAccount raft-kv with automountServiceAccountToken=false"

echo "==> StatefulSet uses dedicated SA and disables token automount"
STS=$(echo "$MANIFEST" | sed -n '/kind: StatefulSet/,/^---/p')
if ! echo "$STS" | grep -q 'serviceAccountName: raft-kv'; then
  fail "StatefulSet missing serviceAccountName: raft-kv"
fi
if ! echo "$STS" | grep -q 'automountServiceAccountToken: false'; then
  fail "StatefulSet pod spec missing automountServiceAccountToken: false"
fi
ok "StatefulSet wired to raft-kv SA, automount off"

echo "==> no Role/RoleBinding grants workload SA API access"
if echo "$MANIFEST" | grep -A30 'kind: RoleBinding' | grep -B5 -A5 'name: raft-kv' | grep -q 'kind: ServiceAccount'; then
  fail "RoleBinding binds workload SA raft-kv — app needs no API RBAC"
fi
ok "no RoleBinding subjects for workload SA"

if [[ -n "$LIVE" ]]; then
  command -v kubectl >/dev/null || fail "--live needs kubectl"
  SA=raft-kv
  TARGET_POD="${RELEASE}-0"
  TARGET_FQDN="${TARGET_POD}.${RELEASE}.${NAMESPACE}.svc.cluster.local"
  CLIENT_NS=raft-kv-clients
  CLIENT_POD=wi-verify-client-$$

  echo "==> live: pod uses $SA and has no projected API token"
  LIVE_SA=$(kubectl get pod -n "$NAMESPACE" "$TARGET_POD" -o jsonpath='{.spec.serviceAccountName}')
  [[ "$LIVE_SA" == "$SA" ]] || fail "pod serviceAccountName=$LIVE_SA want $SA"
  LIVE_AUTO=$(kubectl get pod -n "$NAMESPACE" "$TARGET_POD" -o jsonpath='{.spec.automountServiceAccountToken}')
  [[ "$LIVE_AUTO" == "false" ]] || fail "pod automountServiceAccountToken=$LIVE_AUTO want false"
  if kubectl get pod -n "$NAMESPACE" "$TARGET_POD" -o json \
    | grep -q 'serviceAccountToken'; then
    fail "pod still has projected serviceAccountToken volume"
  fi
  ok "live pod identity: $SA, no API token volume"

  echo "==> live: workload SA has no secret/pod RBAC"
  for verb in get list create delete; do
    for res in secrets pods; do
      if kubectl auth can-i "$verb" "$res" \
        --as="system:serviceaccount:${NAMESPACE}:${SA}" \
        -n "$NAMESPACE" 2>/dev/null | grep -qx yes; then
        fail "workload SA can $verb $res"
      fi
    done
  done
  ok "workload SA denied secrets/pods API verbs"

  echo "==> live: PVC survives pod restart (PUT → delete pod → GET)"
  kubectl create namespace "$CLIENT_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl run "$CLIENT_POD" -n "$CLIENT_NS" --restart=Never --image=busybox:1.36 \
    -l raft-kv.client=true --command -- sleep 300 >/dev/null
  kubectl wait --for=condition=Ready "pod/$CLIENT_POD" -n "$CLIENT_NS" --timeout=90s >/dev/null
  KEY="wi-pvc-$(date +%s)"
  RESP=$(kv_via_client "$CLIENT_NS" "$CLIENT_POD" "$TARGET_FQDN" "PUT ${KEY} persisted") \
    || fail "PUT before restart failed (got: ${RESP:-empty})"
  echo "$RESP" | grep -q OK || fail "PUT before restart failed (got: ${RESP:-empty})"
  kubectl delete pod -n "$NAMESPACE" "$TARGET_POD" --wait=true
  kubectl wait --for=condition=Ready "pod/$TARGET_POD" -n "$NAMESPACE" --timeout=120s >/dev/null
  GET=$(kv_via_client "$CLIENT_NS" "$CLIENT_POD" "$TARGET_FQDN" "GET ${KEY}") \
    || fail "GET after restart failed"
  echo "$GET" | grep -q persisted || fail "GET after restart failed (got: ${GET:-empty})"
  kubectl delete pod "$CLIENT_POD" -n "$CLIENT_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  ok "PVC data survived pod restart"
fi

echo "==> all workload identity checks passed"
