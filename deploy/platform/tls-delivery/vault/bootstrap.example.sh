#!/usr/bin/env bash
# bootstrap.example.sh — one-time Vault PKI + Kubernetes-auth setup for raft-kv ESO.
#
# EXAMPLE ONLY. Run from an operator workstation with vault CLI + cluster-admin.
# Substitute placeholders; never commit tokens, unseal keys, or PEM material.
#
# Prerequisites:
#   - Vault reachable (dev server or HA install)
#   - Kubernetes cluster with raft-kv namespace
#   - External Secrets Operator installed (separate platform step)
#   - ServiceAccount raft-kv-eso in raft-kv (Helm tls.eso.createServiceAccount=true)
#
# Usage:
#   export VAULT_ADDR=https://vault.example:8200
#   export VAULT_TOKEN=<bootstrap-admin-token>   # not stored in git
#   export PKI_MOUNT=pki
#   export K8S_AUTH_MOUNT=kubernetes
#   export RAFT_KV_NS=raft-kv
#   export RAFT_KV_FULLNAME=raft-kv
#   export REPLICAS=3
#   ./deploy/platform/tls-delivery/vault/bootstrap.example.sh
set -euo pipefail

: "${VAULT_ADDR:?set VAULT_ADDR}"
: "${VAULT_TOKEN:?set VAULT_TOKEN for bootstrap only — rotate after}"
PKI_MOUNT=${PKI_MOUNT:-pki}
K8S_AUTH_MOUNT=${K8S_AUTH_MOUNT:-kubernetes}
RAFT_KV_NS=${RAFT_KV_NS:-raft-kv}
RAFT_KV_FULLNAME=${RAFT_KV_FULLNAME:-raft-kv}
REPLICAS=${REPLICAS:-3}
LEAF_TTL=${LEAF_TTL:-168h}
VAULT_ROLE=${VAULT_ROLE:-raft-kv-eso}
K8S_SA=${K8S_SA:-raft-kv-eso}

echo "==> enable PKI mount (skip if already enabled)"
vault secrets enable -path="$PKI_MOUNT" pki 2>/dev/null || true
vault secrets tune -max-lease-ttl=87600h "$PKI_MOUNT"

echo "==> generate internal root (lab/demo). Production: use an offline root + intermediate."
vault write -field=certificate "$PKI_MOUNT/root/generate/internal" \
  common_name="raft-kv-lab-root" ttl=87600h >/dev/null

echo "==> configure PKI URLs (adjust host to your Vault ingress)"
vault write "$PKI_MOUNT/config/urls" \
  issuing_certificates="$VAULT_ADDR/v1/$PKI_MOUNT/ca" \
  crl_distribution_points="$VAULT_ADDR/v1/$PKI_MOUNT/crl"

echo "==> build allowed_domains for ADR-009 SANs (short + cluster DNS)"
DOMAINS=()
for ((i = 0; i < REPLICAS; i++)); do
  short="${RAFT_KV_FULLNAME}-${i}.${RAFT_KV_FULLNAME}"
  DOMAINS+=("$short" "${short}.${RAFT_KV_NS}.svc" "${short}.${RAFT_KV_NS}.svc.cluster.local")
done
ALLOWED=$(IFS=,; echo "${DOMAINS[*]}")

echo "==> create scoped PKI role (no allow_any_name)"
vault write "$PKI_MOUNT/roles/raft-kv" \
  allowed_domains="$ALLOWED" \
  allow_subdomains=false \
  allow_any_name=false \
  allow_bare_domains=true \
  allow_ip_sans=false \
  server_flag=true \
  client_flag=true \
  key_type=rsa \
  key_bits=2048 \
  max_ttl="$LEAF_TTL" \
  ttl="$LEAF_TTL"

echo "==> install narrow policy"
POLICY_FILE="$(dirname "$0")/policy-raft-kv-eso.hcl"
sed "s/PKI_MOUNT/$PKI_MOUNT/g" "$POLICY_FILE" | vault policy write raft-kv-eso -

echo "==> bind Kubernetes auth to SA $K8S_SA in ns $RAFT_KV_NS"
vault write "auth/${K8S_AUTH_MOUNT}/role/${VAULT_ROLE}" \
  bound_service_account_names="$K8S_SA" \
  bound_service_account_namespaces="$RAFT_KV_NS" \
  policies=raft-kv-eso \
  ttl=1h

echo "==> done. Helm values (no secrets):"
cat <<EOF
  tls.eso.enabled: true
  tls.eso.pki.path: ${PKI_MOUNT}/issue/raft-kv
  tls.eso.vault.server: ${VAULT_ADDR}
  tls.eso.vault.auth.mountPath: ${K8S_AUTH_MOUNT}
  tls.eso.vault.auth.role: ${VAULT_ROLE}
  tls.eso.vault.auth.serviceAccountName: ${K8S_SA}
EOF
