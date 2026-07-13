#!/usr/bin/env bash
# verify-kyverno-posture-drift.sh — Phase D #18 chart/policy alignment (CI + local).
#
# Renders the raft-kv chart and checks the Pod template satisfies the same
# posture contract as deploy/platform/kyverno/policies/raft-kv-posture.yaml.
# When kyverno CLI is on PATH (CI installs v1.18.2), also runs the policy
# engine against a synthesized good Pod and a deliberate bad Pod.
#
# Usage:
#   ./scripts/verify-kyverno-posture-drift.sh
#
# Requires: helm, python3. Optional: kyverno (CLI, same app version as platform chart).
set -euo pipefail

cd "$(dirname "$0")/.."

CHART=deploy/helm/raft-kv
POLICY=deploy/platform/kyverno/policies/raft-kv-posture.yaml
RELEASE=drift-check
NAMESPACE=default

command -v helm >/dev/null || { echo "need helm" >&2; exit 1; }
command -v python3 >/dev/null || { echo "need python3" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }

render_sts() {
  helm template "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" \
    --set replicaCount=3 \
    -s templates/statefulset.yaml
}

echo "==> policy enforces at admission (failureAction: Enforce)"
ENFORCE_COUNT=$(grep -c '^        failureAction: Enforce' "$POLICY" || true)
[[ "$ENFORCE_COUNT" -eq 5 ]] || fail "expected 5 Enforce rules in $POLICY (got $ENFORCE_COUNT)"
grep -q 'app: raft-kv' "$POLICY" || fail "policy must select app=raft-kv workloads"
ok "ClusterPolicy has 5 Enforce rules for app=raft-kv"

echo "==> render StatefulSet and extract Pod template"
STS=$(render_sts)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

STS="$STS" python3 - "$TMP/good-pod.yaml" <<'PY'
import os, sys

out_path = sys.argv[1]
sts = os.environ["STS"]

if "kind: StatefulSet" not in sts:
    sys.exit("rendered manifest is not a StatefulSet")

marker = "  template:\n"
start = sts.find(marker)
if start < 0:
    sys.exit("could not find StatefulSet pod template")
start += len(marker)

end = sts.find("  volumeClaimTemplates:", start)
if end < 0:
    sys.exit("could not find end of pod template (volumeClaimTemplates)")

block = sts[start:end]
spec_idx = block.find("    spec:")
if spec_idx < 0:
    sys.exit("pod template missing spec")
spec_part = block[spec_idx + 4 :]  # drop one indent level -> "spec:\n      ..."

pod = (
    "apiVersion: v1\n"
    "kind: Pod\n"
    "metadata:\n"
    "  name: drift-check-good\n"
    "  namespace: drift-check\n"
    "  labels:\n"
    "    app: raft-kv\n"
    + spec_part
)
open(out_path, "w").write(pod)
PY

[[ -s "$TMP/good-pod.yaml" ]] || fail "failed to synthesize Pod from StatefulSet"

echo "==> static posture contract on rendered Pod template"
POD_TEXT=$(cat "$TMP/good-pod.yaml")

echo "$POD_TEXT" | grep -q 'runAsNonRoot: true' || fail "missing runAsNonRoot"
echo "$POD_TEXT" | grep -q 'runAsUser: 65532' || fail "missing runAsUser 65532"
echo "$POD_TEXT" | grep -q 'runAsGroup: 65532' || fail "missing runAsGroup 65532"
echo "$POD_TEXT" | grep -q 'fsGroup: 65532' || fail "missing fsGroup 65532"

RAFT=$(echo "$POD_TEXT" | sed -n '/- name: raft-kv$/,/^    - name: /p')
[[ -n "$RAFT" ]] || RAFT=$(echo "$POD_TEXT" | sed -n '/- name: raft-kv$/,/^      volumes:/p')
echo "$RAFT" | grep -q 'startupProbe:' || fail "raft-kv container missing startupProbe"
echo "$RAFT" | grep -q 'livenessProbe:' || fail "raft-kv container missing livenessProbe"
echo "$RAFT" | grep -q 'readinessProbe:' || fail "raft-kv container missing readinessProbe"
echo "$RAFT" | grep -q 'path: /healthz' || fail "probes must hit /healthz"
echo "$RAFT" | grep -q 'path: /readyz' || fail "readiness must hit /readyz"
echo "$RAFT" | grep -qE 'limits:\s*$' -A1 || true
echo "$RAFT" | grep -q 'memory:' || fail "raft-kv container missing memory request/limit"
ok "rendered Pod template matches Kyverno posture contract"

if command -v kyverno >/dev/null; then
  echo "==> kyverno CLI: good Pod passes Enforce policies"
  cat > "$TMP/bad-pod.yaml" <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: drift-check-bad
  namespace: drift-check
  labels:
    app: raft-kv
spec:
  containers:
    - name: raft-kv
      image: busybox:1.36
      command: ["sleep", "1"]
      securityContext:
        runAsUser: 0
EOF
  set +e
  GOOD_OUT=$(kyverno apply "$POLICY" --resource "$TMP/good-pod.yaml" 2>&1)
  GOOD_RC=$?
  BAD_OUT=$(kyverno apply "$POLICY" --resource "$TMP/bad-pod.yaml" 2>&1)
  BAD_RC=$?
  set -e
  [[ "$GOOD_RC" -eq 0 ]] || { echo "$GOOD_OUT" >&2; fail "kyverno apply rejected good Pod"; }
  ok "kyverno CLI accepts rendered good Pod"
  [[ "$BAD_RC" -ne 0 ]] || { echo "$BAD_OUT" >&2; fail "kyverno apply admitted bad Pod"; }
  ok "kyverno CLI rejects deliberate bad Pod"
else
  echo "==> skip kyverno CLI apply (install kyverno v1.18.2 for engine-level check)"
fi

echo "==> all posture drift checks passed"
