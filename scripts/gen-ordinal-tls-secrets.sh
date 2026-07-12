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
#   ./scripts/gen-ordinal-tls-secrets.sh --ordinal 1 --ca-dir data/lab-ca --name raft-kv
#
# --ca-dir persists the lab CA between runs (required for leaf rotation drill #9).
# Requires: openssl, kubectl.
set -euo pipefail

NAME=raft-kv
NAMESPACE=default
REPLICAS=3
ORDINAL=
CA_DIR=
TTL_DAYS=7

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --namespace|-n) NAMESPACE=$2; shift 2 ;;
    --replicas) REPLICAS=$2; shift 2 ;;
    --ordinal) ORDINAL=$2; shift 2 ;;
    --ca-dir) CA_DIR=$2; shift 2 ;;
    -h|--help)
      sed -n '2,20p' "$0"
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

if [[ -n "$ORDINAL" ]]; then
  if [[ "$ORDINAL" -lt 0 ]] || [[ "$ORDINAL" -ge "$REPLICAS" ]]; then
    echo "--ordinal $ORDINAL out of range for replicas=$REPLICAS" >&2
    exit 1
  fi
fi

if [[ -z "$CA_DIR" ]]; then
  WORKDIR=$(mktemp -d)
  trap 'rm -rf "$WORKDIR"' EXIT
  CA_DIR="$WORKDIR"
else
  mkdir -p "$CA_DIR"
fi

CA_KEY="$CA_DIR/ca.key"
CA_CRT="$CA_DIR/ca.crt"

if [[ -f "$CA_KEY" && -f "$CA_CRT" ]]; then
  echo "==> reusing lab CA from $CA_DIR"
else
  echo "==> generating lab CA in $CA_DIR (not for production)"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "$CA_KEY" -out "$CA_CRT" -days 3650 \
    -subj "/CN=raft-kv-lab-ca" >/dev/null 2>&1
fi

kubectl get ns "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"

issue_ordinal() {
  local i=$1
  local POD="${NAME}-${i}"
  local SHORT="${POD}.${NAME}"
  local SECRET="${POD}-tls"
  local KEY="$CA_DIR/${POD}.key"
  local CSR="$CA_DIR/${POD}.csr"
  local CRT="$CA_DIR/${POD}.crt"
  local EXT="$CA_DIR/${POD}.ext"

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
}

if [[ -n "$ORDINAL" ]]; then
  issue_ordinal "$ORDINAL"
else
  for ((i = 0; i < REPLICAS; i++)); do
    issue_ordinal "$i"
  done
fi

echo "==> done. Install/upgrade the chart with tls.enabled=true (default)."
