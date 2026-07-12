#!/usr/bin/env bash
# verify-tls-secret-hygiene.sh — Phase B #10 static (+ optional live) checks.
#
# Ensures TLS material is mount-only: no PEMs in Helm output, read-only workload
# mounts, file paths (not key bytes) in args, no secretKeyRef env wiring.
#
# Usage:
#   ./scripts/verify-tls-secret-hygiene.sh
#   ./scripts/verify-tls-secret-hygiene.sh --live --namespace default
#
# Requires: helm, git, grep. --live also needs kubectl + a reachable cluster.
set -euo pipefail

cd "$(dirname "$0")/.."

CHART=deploy/helm/raft-kv
RELEASE=hygiene-check
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
command -v git >/dev/null || { echo "need git" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }

render() {
  helm template "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" \
    --set replicaCount=3 \
    --set tls.enabled=true \
    --set tls.eso.enabled=true \
    --set tls.eso.pki.path=pki/issue/raft-kv \
    --set tls.eso.vault.server=https://vault.example.svc:8200
}

echo "==> render chart (TLS + ESO)"
MANIFEST=$(render)

echo "==> no PEM material in Helm output"
if echo "$MANIFEST" | grep -qE 'BEGIN (CERTIFICATE|RSA PRIVATE KEY|PRIVATE KEY)'; then
  fail "rendered manifests contain PEM blocks"
fi
ok "no PEM in helm template output"

echo "==> workload TLS volumeMount is readOnly"
RAFT_KV_MOUNTS=$(echo "$MANIFEST" | sed -n '/- name: raft-kv$/,/^      volumes:/p')
if ! echo "$RAFT_KV_MOUNTS" | grep -A3 'name: tls' | grep -q 'readOnly: true'; then
  fail "raft-kv container tls volumeMount missing readOnly: true"
fi
ok "main container tls mount readOnly"

echo "==> init ordinal Secret mounts are readOnly"
INIT_MOUNTS=$(echo "$MANIFEST" | sed -n '/- name: select-tls$/,/^      containers:/p')
if ! echo "$INIT_MOUNTS" | grep -A2 'name: tls-[0-9]' | grep -q 'readOnly: true'; then
  fail "init tls-N volumeMounts should be readOnly"
fi
ok "init ordinal secret mounts readOnly"

echo "==> no secretKeyRef in pod env (keys stay file-mounted)"
if echo "$MANIFEST" | grep -A30 'kind: StatefulSet' | grep -q 'secretKeyRef'; then
  fail "StatefulSet uses secretKeyRef in env — keys must not be env-injected"
fi
ok "no secretKeyRef in StatefulSet env"

echo "==> TLS flags are filesystem paths only"
if ! echo "$MANIFEST" | grep -qF 'raft-tls-key=/var/run/raft-kv/tls/tls.key'; then
  fail "expected --raft-tls-key file path arg"
fi
if echo "$MANIFEST" | grep -F 'raft-tls-key=' | grep -qE 'BEGIN |MII'; then
  fail "TLS key material must not appear in command args"
fi
ok "args reference paths only"

echo "==> ESO RoleBinding does not grant default ServiceAccount"
RB_SUBJECTS=$(echo "$MANIFEST" | sed -n '/kind: RoleBinding/,/^---/p' | sed -n '/subjects:/,$p')
if echo "$RB_SUBJECTS" | grep -qE '^[[:space:]]+name: default$'; then
  fail "RoleBinding subjects include default SA for TLS sync"
fi
if ! echo "$RB_SUBJECTS" | grep -q 'name: external-secrets'; then
  fail "ESO controller should be the RoleBinding subject for secret sync"
fi
ok "TLS Secret RBAC bound to ESO controller only (rendered)"

echo "==> git tree has no committed PEM blocks"
if git grep -qE '^-----BEGIN (CERTIFICATE|RSA PRIVATE KEY|PRIVATE KEY)' -- . 2>/dev/null; then
  fail "tracked files contain PEM blocks"
fi
ok "no PEM in git tree"

if [[ -n "$LIVE" ]]; then
  command -v kubectl >/dev/null || fail "--live needs kubectl"
  echo "==> live: unrelated ServiceAccount cannot read TLS Secrets"
  for i in 0 1 2; do
    if kubectl auth can-i get "secret/${RELEASE}-${i}-tls" \
      --as="system:serviceaccount:${NAMESPACE}:default" \
      -n "$NAMESPACE" 2>/dev/null | grep -qx yes; then
      fail "default SA can get secret/${RELEASE}-${i}-tls"
    fi
  done
  ok "default ServiceAccount denied get on TLS Secrets"
fi

echo "==> all hygiene checks passed"
