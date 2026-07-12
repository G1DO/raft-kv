# Runbook — Peer TLS certificates (Vault / ESO)

Operator guide for **production** peer mTLS material on Kubernetes. The app only
reads files from mounts ([ADR-009](../decisions/ADR-009-mtls-peer-identity.md));
this runbook covers delivery, renewal, and revocation.

**Lab / kind without Vault:** [`scripts/gen-ordinal-tls-secrets.sh`](../../scripts/gen-ordinal-tls-secrets.sh)
then `helm install` with default `tls.eso.enabled=false`. Leaf rotation drill:
[`scripts/tls-rotation-drill.sh`](../../scripts/tls-rotation-drill.sh).

**Bootstrap (one-time):** [`deploy/platform/tls-delivery/README.md`](../../deploy/platform/tls-delivery/README.md)

---

## Prerequisites checklist

- [ ] External Secrets Operator installed (cluster platform)
- [ ] Vault PKI mount + `raft-kv` role with ADR-009 DNS SANs for your ordinals
- [ ] Vault policy `raft-kv-eso` (issue + read CA only) — see
      [`policy-raft-kv-eso.hcl`](../../deploy/platform/tls-delivery/vault/policy-raft-kv-eso.hcl)
- [ ] Vault Kubernetes-auth role bound to SA `raft-kv-eso` in namespace `raft-kv`
- [ ] ESO RBAC in `raft-kv` (chart `tls.eso.rbac.create` or example manifest)
- [ ] Helm: `tls.eso.enabled=true` with server / path / role refs (no PEMs in values)

---

## Rotation drill (Phase B #9)

**Verified:** leaf renewal under the **same CA** works with a **pod restart**; there is
**no in-process TLS reload** (`SetTLSConfig` / `loadMTLS` run at startup only —
see `TestTLS_NoInProcessReload` in `internal/raft/tls_config_test.go`).

**CA dual-trust rotation:** not exercised; not claimed in M8 ([ADR-010](decisions/ADR-010-mtls-rollout.md)).

### Kind / lab (scripted)

After `./scripts/k8s-up.sh` (persists lab CA under `data/lab-ca/`):

```bash
./scripts/tls-rotation-drill.sh
# optional: --ordinal 2 --namespace default --name raft-kv
```

The drill:

1. Records the follower cert serial in Secret `raft-kv-N-tls`
2. Re-issues that ordinal with the **same** lab CA (`--ca-dir data/lab-ca`)
3. Restarts **only** pod `raft-kv-N`
4. `PUT`/`GET` through `raft-kv-0` to prove quorum + mTLS still work

### Production (Vault / ESO)

1. Let ESO refresh the Secret (or force-sync — see [Forced renewal](#forced-renewal-operator))
2. `kubectl delete pod -n raft-kv raft-kv-N` — **restart required**
3. Wait `/readyz`; repeat one ordinal at a time
4. Confirm new cert serial in Secret and healthy peer RPC

---

## Normal renewal (leaf, same CA)

ADR-009 / ADR-010: ESO refreshes the Secret; **raft-kv does not hot-reload** certs.

1. ESO `refreshInterval` (default 24h) must stay **below** leaf TTL (`tls.eso.pki.ttl`, target ≤ 7d).
2. When Vault re-issues, ESO updates `raft-kv-N-tls` in place.
3. **Restart one pod at a time** so it reads the new files:
   ```bash
   kubectl delete pod -n raft-kv raft-kv-1
   kubectl rollout status statefulset/raft-kv -n raft-kv
   ```
4. Wait `/readyz` on the restarted member before the next ordinal.
5. Confirm mTLS still works (peer logs show no `rpc_identity_rejected` / TLS handshake errors).

Do **not** roll all pods at once unless you accept brief quorum risk.

---

## Forced renewal (operator)

If you need a cert rotated before ESO refresh:

```bash
# Force ESO to reconcile (annotation pattern; exact label depends on ESO version)
kubectl annotate externalsecret -n raft-kv raft-kv-1-tls \
  force-sync=$(date +%s) --overwrite

kubectl delete pod -n raft-kv raft-kv-1
```

Or temporarily lower `tls.eso.refreshInterval` via Helm, sync, then restore.

---

## Revocation / incident (compromised leaf key)

Assume **one ordinal's private key** may be exposed (not CA compromise).

1. **Revoke in Vault** (serial from cert or Vault audit):
   ```bash
   vault write pki/revoke serial_number=<hex-serial>
   ```
2. **Force ESO sync** for that ordinal's ExternalSecret (see above).
3. **Restart that pod** only; verify it rejoins with a new serial.
4. **Review** who had `get secret` RBAC on `raft-kv-N-tls` (#10 hygiene).

Do not paste PEMs or private keys into tickets, git, or chat.

---

## CA compromise (suspected)

**Dual-trust CA rotation is not claimed in M8** ([ADR-010](decisions/ADR-010-mtls-rollout.md)).
Treat as disaster:

1. Stop new writes at the client layer if possible.
2. Rotate Vault PKI root/intermediate per your org procedure (out of scope here).
3. Re-bootstrap policy + K8s-auth role; re-issue all ordinals.
4. Rolling restart **every** member with quorum-safe one-at-a-time order.
5. Document gap: until a dual-trust drill exists, plan maintenance window.

---

## Failure modes

| Symptom | Likely cause | Action |
|---------|--------------|--------|
| Init `missing TLS files` | Secret not created yet | Check ExternalSecret status; Vault policy / K8s-auth binding |
| ExternalSecret `SecretSyncedError` | Vault deny / ESO RBAC | `kubectl describe externalsecret`; Vault audit log |
| Pod runs but peer TLS fails | SAN mismatch vs ADR-009 | Compare cert SANs to `alt_names` in generator; fix PKI role |
| Stale cert after renew | No pod restart | Delete pod after Secret update |

---

## What raft-kv pods must never have

- Vault tokens or `VAULT_*` env vars
- RBAC `get/list` on TLS Secrets (mount-only access via kubelet)
- Shared wildcard cert across ordinals (ADR-009 rejected)

ESO holds the Vault session; the workload only sees files under
`/var/run/raft-kv/tls/`.

---

## Secret hygiene (Phase B #10)

Static verification (CI + local):

```bash
./scripts/verify-tls-secret-hygiene.sh
```

Checks:

| Check | Expectation |
|-------|-------------|
| Helm render | No `BEGIN CERTIFICATE` / `BEGIN PRIVATE KEY` in output |
| Pod mounts | `readOnly: true` on workload TLS volume; init ordinal Secret mounts read-only |
| Env / args | No `secretKeyRef`; `--raft-tls-*` are **paths** only |
| Git | No committed PEM fixtures |
| RBAC (rendered) | TLS Secret sync RoleBinding → ESO controller only, not `default` SA |

With a live cluster:

```bash
./scripts/verify-tls-secret-hygiene.sh --live --namespace default
```

Confirms `system:serviceaccount:<ns>:default` **cannot** `get` `*-tls` Secrets.
Raft pods use the namespace default SA for API access but receive TLS material
via kubelet volume mounts only — not via `kubectl get secret`.
