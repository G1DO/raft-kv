#!/usr/bin/env bash
# gen-ordinal-tls-secrets.sh — lab/kind self-signed Secrets matching ADR-009.
#
# Creates one Kubernetes Secret per StatefulSet ordinal:
#   {{name}}-N-tls  keys: tls.crt, tls.key, ca.crt
# with DNS SANs: <pod>.<name>, <pod>.<name>.<ns>.svc, …cluster.local
#
# Does NOT talk to Vault. For production use Helm tls.eso.enabled + ESO (Phase B #7).
# Never commit the generated PEMs.
#
# Usage:
#   ./scripts/gen-ordinal-tls-secrets.sh
#   ./scripts/gen-ordinal-tls-secrets.sh --namespace raft-kv --name raft-kv --replicas 3
#
# Requires: openssl, kubectl. Writes a throwaway CA under a temp dir (deleted on exit
# unless --keep-dir is set).
set -euo pipefail

NAME=raft-kv
NAMESPACE=default
REPLICAS=3
KEEP_DIR=
TTL_DAYS=7

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --namespace|-n) NAMESPACE=$2; shift 2 ;;
    --replicas) REPLICAS=$2; shift 2 ;;
    --keep-dir) KEEP_DIR=$2; shift 2 ;;
    -h|--help)
      sed -n '2,18p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

if ! command -v openssl >/dev/null || ! command -v kubectl >/dev/null; then
  echo "need openssl and kubectl on PATH" >&2
  exit 1
fi

WORKDIR=${KEEP_DIR:-$(mktemp -d)}
cleanup() {
  if [[ -z "${KEEP_DIR}" ]]; then
    rm -rf "$WORKDIR"
  fi
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
CA_KEY="$WORKDIR/ca.key"
CA_CRT="$WORKDIR/ca.crt"

openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$CA_KEY" -out "$CA_CRT" -days 3650 \
  -subj "/CN=raft-kv-lab-ca" >/dev/null 2>&1

echo "==> lab CA in $WORKDIR (not for production)"
kubectl get ns "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"

for ((i = 0; i < REPLICAS; i++)); do
  POD="${NAME}-${i}"
  SHORT="${POD}.${NAME}"
  SECRET="${POD}-tls"
  KEY="$WORKDIR/${POD}.key"
  CSR="$WORKDIR/${POD}.csr"
  CRT="$WORKDIR/${POD}.crt"
  EXT="$WORKDIR/${POD}.ext"

  cat >"$EXT" <<EOF
subjectAltName=DNS:${SHORT},DNS:${SHORT}.${NAMESPACE}.svc,DNS:${SHORT}.${NAMESPACE}.svc.cluster.local
extendedKeyUsage=serverAuth,clientAuth
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
EOF

  openssl req -newkey rsa:2048 -nodes \
    -keyout "$KEY" -out "$CSR" \
    -subj "/CN=${SHORT}" >/dev/null 2>&1
  openssl x509 -req -in "$CSR" -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial \
    -out "$CRT" -days "$TTL_DAYS" -extfile "$EXT" >/dev/null 2>&1

  kubectl -n "$NAMESPACE" create secret generic "$SECRET" \
    --from-file=tls.crt="$CRT" \
    --from-file=tls.key="$KEY" \
    --from-file=ca.crt="$CA_CRT" \
    --dry-run=client -o yaml | kubectl apply -f -

  echo "    Secret/${SECRET} (SANs: ${SHORT}, …)"
done

echo "==> done. Install/upgrade the chart with tls.enabled=true (default)."
