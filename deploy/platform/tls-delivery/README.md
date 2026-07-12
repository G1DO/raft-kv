# Platform TLS delivery — Vault PKI + External Secrets Operator

This is the **platform half** of peer mTLS certificate delivery. The app chart
(`deploy/helm/raft-kv`, `tls.eso`) declares *what* Secrets to sync; this directory
defines *who may ask Vault for certs* and how operators bootstrap it.

- Chart templates: [`externalsecret.yaml`](../../helm/raft-kv/templates/externalsecret.yaml)
- Identity contract: [ADR-009](../../../docs/decisions/ADR-009-mtls-peer-identity.md)
- Operator lifecycle (renew / revoke): [docs/runbooks/tls-certificates.md](../../../docs/runbooks/tls-certificates.md)

## Roles (who talks to whom)

| Actor | Talks to | Purpose |
|-------|----------|---------|
| **Vault PKI** | — | Holds CA key; issues leaf certs when policy allows |
| **ESO controller** | Vault + Kubernetes API | Uses SA `raft-kv-eso` token for Vault login; writes `raft-kv-N-tls` Secrets |
| **raft-kv pods** | Files on disk only | Read mounted `tls.crt` / `tls.key` / `ca.crt`; **never** call Vault |
| **Operator** | Vault admin API (bootstrap) | One-time PKI + policy + K8s-auth role setup |

This is **not** GitHub, OAuth, or app-user login. ESO presents a Kubernetes
ServiceAccount token to Vault; Vault checks a narrow policy and returns cert material.

## Files

| Path | Purpose |
|------|---------|
| [`vault/policy-raft-kv-eso.hcl`](vault/policy-raft-kv-eso.hcl) | Least-privilege policy: issue one PKI role + read CA only |
| [`vault/bootstrap.example.sh`](vault/bootstrap.example.sh) | Example `vault write` sequence (placeholders; no secrets in git) |
| [`k8s/eso-raft-kv-rbac.example.yaml`](k8s/eso-raft-kv-rbac.example.yaml) | Namespace Role so ESO can sync Secrets in `raft-kv` |

When `tls.eso.enabled=true`, the Helm chart also renders SA `raft-kv-eso` and
the same RBAC (parameterize ESO controller SA via `tls.eso.controller.*`).

## Bootstrap order

1. **Install External Secrets Operator** (cluster platform; not part of this chart).
2. **Create `raft-kv` namespace** (Argo `CreateNamespace=true` or manually).
3. **Run Vault bootstrap** (example script) or equivalent HCL/Terraform:
   - Enable PKI mount
   - Create `raft-kv` PKI role with ADR-009 `allowed_domains` for your ordinals
   - Write policy from `policy-raft-kv-eso.hcl`
   - Bind `auth/kubernetes/role/raft-kv-eso` → SA `raft-kv-eso` in `raft-kv`
4. **Apply RBAC** so ESO can create Secrets and mint SA tokens in `raft-kv`.
5. **Deploy raft-kv chart** with `tls.eso.enabled=true` and vault refs only.

Lab / kind without Vault: use
[`scripts/gen-ordinal-tls-secrets.sh`](../../../scripts/gen-ordinal-tls-secrets.sh)
(same Secret names; no ESO).

## What the Vault policy deliberately cannot do

- `list` or `read` arbitrary secret engines
- Issue certs from any PKI role other than `PKI_MOUNT/issue/raft-kv`
- Read or rotate the root key (`pki/root/*`)
- Admin paths (`sys/*`, `auth/*` other than self)

If ESO is compromised, blast radius is **minting raft-kv peer leaf certs** under the
scoped PKI role — not reading unrelated Vault data.

## Verify least privilege (smoke)

After bootstrap, with a token for the `raft-kv-eso` policy only:

```bash
# should succeed
vault write pki/issue/raft-kv common_name=raft-kv-0.raft-kv \
  alt_names=raft-kv-0.raft-kv,raft-kv-0.raft-kv.raft-kv.svc

# should fail (policy denied)
vault kv get secret/some-other-app
vault write pki/issue/other-role common_name=evil.example
```

Kubernetes: unrelated SA must not read TLS Secrets (checked in Phase B #10).
