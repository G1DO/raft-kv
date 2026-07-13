#!/usr/bin/env bash
# install-kyverno.sh — idempotent Kyverno platform install for lab/kind (Phase D #15).
#
#   ./scripts/install-kyverno.sh
#   kubectl get pods -n kyverno
#
# Image signatures remain on sigstore policy-controller (deploy/policy/).
set -euo pipefail

cd "$(dirname "$0")/.."

RELEASE=kyverno
CHART=deploy/platform/kyverno
NS=kyverno

echo "==> building chart dependencies ($CHART)"
helm dependency build "$CHART"

echo "==> installing Kyverno ($RELEASE) into namespace $NS"
helm upgrade --install "$RELEASE" "$CHART" \
  --namespace "$NS" \
  --create-namespace \
  --wait \
  --timeout 300s

echo
kubectl get pods -n "$NS"
echo
kubectl get crd clusterpolicies.kyverno.io policies.kyverno.io 2>/dev/null || true
echo
echo "Kyverno platform install complete. Posture policies: deploy/platform/kyverno/policies/ (#16)."
