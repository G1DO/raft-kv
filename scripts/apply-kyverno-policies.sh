#!/usr/bin/env bash
# apply-kyverno-policies.sh — apply Phase D #16 posture ClusterPolicies.
set -euo pipefail

cd "$(dirname "$0")/.."

POLICIES=deploy/platform/kyverno/policies

command -v kubectl >/dev/null || { echo "need kubectl" >&2; exit 1; }

echo "==> applying Kyverno posture policies from $POLICIES"
kubectl apply -f "$POLICIES"

echo
kubectl get clusterpolicies -l app=raft-kv
echo
echo "Policies enforce at admission (failureAction: Enforce). Verify: ./scripts/verify-kyverno-posture.sh --live"
