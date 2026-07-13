#!/usr/bin/env bash
# verify-kyverno-posture.sh — Phase D #16–#17 Kyverno posture checks (static + live).
#
# Usage:
#   ./scripts/verify-kyverno-posture.sh
#   ./scripts/verify-kyverno-posture.sh --live --namespace default
#
# Requires: kubectl. --live expects Kyverno (#15) and applied policies.
set -euo pipefail

cd "$(dirname "$0")/.."

POLICIES=deploy/platform/kyverno/policies
RAFT_NS=default
LIVE=

while [[ $# -gt 0 ]]; do
  case "$1" in
    --live) LIVE=1; shift ;;
    --namespace|-n) RAFT_NS=$2; shift 2 ;;
    -h|--help)
      sed -n '2,10p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

command -v kubectl >/dev/null || { echo "need kubectl" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }

echo "==> policy manifest parses (server dry-run)"
kubectl apply --dry-run=server -f "$POLICIES" >/dev/null
ok "raft-kv-posture ClusterPolicy accepted by API server"

if [[ -n "$LIVE" ]]; then
  echo "==> live: Kyverno controller running"
  kubectl get pods -n kyverno -l app.kubernetes.io/component=admission-controller \
    --no-headers 2>/dev/null | grep -q Running || fail "Kyverno admission controller not Running"

  echo "==> live: ClusterPolicy raft-kv-posture installed"
  kubectl get clusterpolicy raft-kv-posture >/dev/null 2>&1 || fail "apply policies first: ./scripts/apply-kyverno-policies.sh"

  MODE=$(kubectl get clusterpolicy raft-kv-posture -o jsonpath='{.spec.rules[0].validate.failureAction}')
  [[ "$MODE" == "Enforce" ]] || fail "expected Enforce mode in #17 (got $MODE)"
  ok "policy mode is Enforce"

  echo "==> live: wait for background scan"
  sleep 8

  echo "==> live: raft-kv pods should pass posture rules"
  FAILS=0
  for i in 0 1 2; do
    POD="raft-kv-$i"
    if ! kubectl get pod -n "$RAFT_NS" "$POD" >/dev/null 2>&1; then
      fail "pod $POD not found in $RAFT_NS"
    fi
    # PolicyReport name is generated; grep reports for this pod.
    if kubectl get policyreport -n "$RAFT_NS" -o yaml 2>/dev/null | grep -A20 "name: $POD" | grep -q 'status: fail'; then
      echo "FAIL: $POD has failing policy report entries" >&2
      FAILS=1
    fi
  done
  [[ "$FAILS" -eq 0 ]] || fail "raft-kv pods failed posture policy reports"
  ok "raft-kv pods have no failing PolicyReport entries"

  echo "==> live: compliant pod recreate succeeds under Enforce"
  kubectl delete pod -n "$RAFT_NS" raft-kv-0 --wait=true
  kubectl wait --for=condition=Ready "pod/raft-kv-0" -n "$RAFT_NS" --timeout=120s >/dev/null
  ok "raft-kv-0 recreated successfully"

  echo "==> live: deliberate bad pod denied at admission"
  BAD=np-posture-bad-$$
  BAD_MANIFEST=$(mktemp)
  trap 'rm -f "$BAD_MANIFEST"' EXIT
  cat > "$BAD_MANIFEST" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${BAD}
  namespace: ${RAFT_NS}
  labels:
    app: raft-kv
spec:
  containers:
    - name: raft-kv
      image: busybox:1.36
      command: ["sleep", "60"]
      securityContext:
        runAsUser: 0
EOF
  set +e
  OUT=$(kubectl apply --dry-run=server -f "$BAD_MANIFEST" 2>&1)
  RC=$?
  set -e
  rm -f "$BAD_MANIFEST"
  trap - EXIT
  [[ "$RC" -ne 0 ]] || fail "bad pod was admitted (expected Enforce denial)"
  if echo "$OUT" | grep -Eiq 'denied|violation|failure'; then
    ok "bad pod denied by Kyverno admission webhook"
  else
    echo "$OUT" >&2
    fail "expected admission denial message for bad pod"
  fi
fi

echo "==> all kyverno posture checks passed"
