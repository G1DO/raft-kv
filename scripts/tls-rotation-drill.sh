#!/usr/bin/env bash
# tls-rotation-drill.sh — Phase B #9 leaf renewal drill (kind / lab path).
#
# Renews one follower's TLS Secret (same CA), restarts only that pod, and proves
# the cluster still serves writes. Confirms restart is required (no in-process reload).
#
# Prerequisites: cluster up with mTLS (./scripts/k8s-up.sh uses data/lab-ca).
#
# Usage:
#   ./scripts/tls-rotation-drill.sh
#   ./scripts/tls-rotation-drill.sh --ordinal 2 --namespace default --name raft-kv
#
# Production (Vault/ESO): renew via ESO/Vault, then delete the pod — see
# docs/runbooks/tls-certificates.md. CA dual-trust rotation is not claimed (ADR-010).
set -euo pipefail

cd "$(dirname "$0")/.."

NAME=raft-kv
NAMESPACE=default
ORDINAL=1
CA_DIR=data/lab-ca
CLIENT_PORT=8080
TIMEOUT=120

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --namespace|-n) NAMESPACE=$2; shift 2 ;;
    --ordinal) ORDINAL=$2; shift 2 ;;
    --ca-dir) CA_DIR=$2; shift 2 ;;
    --timeout) TIMEOUT=$2; shift 2 ;;
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

POD="${NAME}-${ORDINAL}"
SECRET="${POD}-tls"

need() {
  command -v "$1" >/dev/null || { echo "need $1 on PATH" >&2; exit 1; }
}
need kubectl
need openssl
need nc

secret_serial() {
  kubectl -n "$NAMESPACE" get secret "$SECRET" -o jsonpath='{.data.tls\.crt}' \
    | base64 -d | openssl x509 -noout -serial 2>/dev/null
}

echo "==> preflight: pods Ready"
kubectl -n "$NAMESPACE" wait --for=condition=ready "pod/${NAME}-0" --timeout="${TIMEOUT}s"
kubectl -n "$NAMESPACE" wait --for=condition=ready "pod/${NAME}-1" --timeout="${TIMEOUT}s"
kubectl -n "$NAMESPACE" wait --for=condition=ready "pod/${NAME}-2" --timeout="${TIMEOUT}s"

OLD_SERIAL=$(secret_serial)
if [[ -z "$OLD_SERIAL" ]]; then
  echo "Secret ${SECRET} missing or invalid — run k8s-up / gen-ordinal-tls-secrets first" >&2
  exit 1
fi
echo "    ${POD} cert before renew: ${OLD_SERIAL}"

echo "==> renew leaf Secret (same CA from ${CA_DIR})"
./scripts/gen-ordinal-tls-secrets.sh \
  --name "$NAME" --namespace "$NAMESPACE" --replicas 3 \
  --ordinal "$ORDINAL" --ca-dir "$CA_DIR"

NEW_SERIAL_IN_SECRET=$(secret_serial)
if [[ "$NEW_SERIAL_IN_SECRET" == "$OLD_SERIAL" ]]; then
  echo "Secret did not change serial after renew" >&2
  exit 1
fi
echo "    Secret updated: ${NEW_SERIAL_IN_SECRET} (restart required — process does not reload)"

echo "==> restart only ${POD}"
kubectl -n "$NAMESPACE" delete pod "$POD" --wait=true
kubectl -n "$NAMESPACE" wait --for=condition=ready "pod/${POD}" --timeout="${TIMEOUT}s"

echo "==> verify write/read through ${NAME}-0 (client port)"
KEY="drill-$(date +%s)"
VAL="rotated-${ORDINAL}"
PF_PID=
cleanup() {
  if [[ -n "${PF_PID:-}" ]]; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

kubectl -n "$NAMESPACE" port-forward "pod/${NAME}-0" "${CLIENT_PORT}:${CLIENT_PORT}" >/dev/null 2>&1 &
PF_PID=$!
for _ in $(seq 1 30); do
  if printf 'GET probe\n' | nc -w 1 localhost "$CLIENT_PORT" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

OUT=$(printf 'PUT %s %s\nGET %s\n' "$KEY" "$VAL" "$KEY" | nc -w 3 localhost "$CLIENT_PORT")
echo "$OUT" | grep -q "OK" || { echo "PUT failed: $OUT" >&2; exit 1; }
echo "$OUT" | grep -q "$VAL" || { echo "GET failed: $OUT" >&2; exit 1; }

echo "==> drill passed"
echo "    leaf renewed on ${POD}; quorum intact; restart required (no in-process TLS reload)"
echo "    CA dual-trust rotation: not exercised (ADR-010 — not claimed in M8)"
