# ADR-009 — Peer mTLS certificate and identity contract

**Status:** Accepted · **Date:** 2026-07-11

## Context

Peer RPC is plaintext JSON-over-TCP
([ADR-003](ADR-003-json-tcp-vs-grpc.md)): `callRPC` dials and the Raft
listener accepts any TCP client
([internal/raft/raft.go](../../internal/raft/raft.go)). Threat-model T1 / T3 /
T6 / T8 are open until peers authenticate each other.

The cluster already has stable member identity. Helm generates the same
`--peers` string for every pod
([deploy/helm/raft-kv/templates/_helpers.tpl](../../deploy/helm/raft-kv/templates/_helpers.tpl)):

```
<id>@<raftHost>:<raftPort>@<clientHost>:<clientPort>
```

where `<id>` is the StatefulSet pod name (`raft-kv-0`, …) and `<raftHost>` is
the headless short DNS (`raft-kv-0.raft-kv`). Certificates must bind TLS
identity to that member — not to “any pod in the release.”

M8 Phase A implements the transport; Phase B delivers material via Vault +
External Secrets Operator (ESO). This ADR locks the **identity and material
contract** before either phase writes code. Rollout / fail-closed behaviour
is [ADR-010](ADR-010-mtls-rollout.md) (rollout and failure mode); this document
only defines what a correct peer certificate *is*.

## Decision

### Issuer and delivery

| Role | Owner | Responsibility |
|------|--------|----------------|
| Issue leaf certs + CA | **Vault PKI** | Sign per-ordinal leaves; hold the CA private key |
| Deliver into the pod | **External Secrets Operator** | Sync Vault-issued material into a Kubernetes TLS Secret; mount into the matching StatefulSet ordinal |
| Consume at runtime | **raft-kv process** | Read CA + leaf cert/key from configured filesystem paths; never talk to Vault |

The application ServiceAccount must not need Vault credentials. ESO authenticates
to Vault with its own workload identity and a policy scoped to this app’s PKI
paths only (Phase B details; no credentials in this repo).

### Per-ordinal identity (no shared key)

Each StatefulSet ordinal gets its **own** leaf private key and certificate.
A shared or wildcard certificate for `*.raft-kv` (or one Secret mounted by every
pod) is **rejected**: it would authenticate “a cluster member” but not “this
ordinal,” so a compromised pod could impersonate any peer.

**Raft member ID** (`--id`, e.g. `raft-kv-0`) is the authoritative logical
identity. The certificate’s DNS SANs must include the DNS names derived from
that id so dial and accept can verify the same string the peer list already
uses.

### Subject Alternative Names

For release name / headless Service `raft-kv` in namespace `<ns>`, ordinal `N`:

| SAN | Purpose |
|-----|---------|
| `raft-kv-N.raft-kv` | Matches the short host in `--peers` (primary dial `ServerName`) |
| `raft-kv-N.raft-kv.<ns>.svc` | Cluster-local short FQDN |
| `raft-kv-N.raft-kv.<ns>.svc.cluster.local` | Full cluster DNS |

CN may repeat the short form for readability; verification is by **DNS SAN**,
not CN alone. URI SANs are not required for M8.

When verifying an inbound connection, the server checks the client certificate
SAN set against the **configured peer id / DNS** for that connection (Phase A
#3). A CA-valid cert for `raft-kv-1` must not be accepted as `raft-kv-0`.

### Secret object and mount contract

One Kubernetes Secret per ordinal (ESO-created; not committed):

| Convention | Value |
|------------|--------|
| Secret name | `{{ fullname }}-N-tls` (e.g. `raft-kv-0-tls`) |
| Keys | `tls.crt`, `tls.key`, `ca.crt` (CA bundle; may be a single CA or a PEM chain) |
| Mount | Read-only projected/volume under e.g. `/var/run/raft-kv/tls/` |
| Paths (chart → flags) | `--raft-tls-cert`, `--raft-tls-key`, `--raft-tls-ca` (exact flag names fixed in Phase A #1) |

Secrets and Vault paths stay out of Git. The chart commits only ExternalSecret
templates / value references. TLS material must never appear as env vars,
command-line arguments, Helm `--set` values, logs, or docs fixtures.

### CA bundle

- Trust store for both dial and accept is the mounted `ca.crt` from the same
  Secret (or an identical CA ConfigMap/Secret if Phase B splits it — still one
  trust root per cluster).
- Peers trust only this CA for Raft RPC. System roots are not used for peer
  verification.
- CA compromise implies full peer-impersonation risk; rotation and dual-trust
  windows are owned by [ADR-010](ADR-010-mtls-rollout.md) / Phase B rotation
  drill (#9), not claimed here as already supported.

### TTL and renewal

| Item | Contract |
|------|----------|
| Leaf TTL | Short-lived relative to PVC lifetime (target **≤ 7 days**; exact Vault role TTL set at bootstrap) |
| Renewal | Vault / ESO refresh before expiry; raft-kv does **not** implement in-process hot reload in M8 unless Phase B’s rotation drill proves otherwise |
| After renew | Restart the affected member (StatefulSet pod) so it loads new files; document as required in the rotation runbook |
| Revocation | Document procedure (Vault revoke + restart / replace Secret); incident path must not require pasting PEMs into git |

### Local / development mode

| Environment | Mode |
|-------------|------|
| Unit / integration tests | May use plaintext when TLS config is **unset** ([ADR-010](ADR-010-mtls-rollout.md)); never the Helm production default |
| Local kind / `cluster-up.sh` | Prefer a **scripted self-signed CA** that issues the same per-ordinal SAN set as Vault would; mount files via the same path contract so the binary path matches production |
| Helm chart (Kubernetes) | mTLS **on by default**; missing CA/cert/key must fail closed ([ADR-010](ADR-010-mtls-rollout.md)) — no silent downgrade to plaintext |

### Binding summary

```
StatefulSet ordinal N
  → Raft --id = <fullname>-N
  → peer DNS = <fullname>-N.<fullname>[.<ns>.svc[.cluster.local]]
  → Secret <fullname>-N-tls {tls.crt, tls.key, ca.crt}
  → leaf cert SANs include those DNS names
  → dial ServerName = short peer host from --peers
  → accept: client cert SAN must match expected peer identity
```

## Rejected alternatives

**One wildcard (or one shared) certificate for all members.**
Simplest ops story and weakest auth: any holder of the key can forge RPCs as
any peer. Rejected for T1.

**App pods authenticating directly to Vault for certs.**
Puts Vault credentials or tokens in the Raft workload; expands blast radius.
ESO owns delivery; the app only reads files.

**SPIFFE/SPIRE as the identity plane for M8.**
Stronger long-term model, but new control plane and different verification
API. Vault PKI + DNS SANs match the existing StatefulSet DNS and peer list
without a second identity system. Revisit only if Vault/ESO proves unfit.

**Client (KV port) certificates in this ADR.**
Out of M8 scope (threat-model / tracker fence). This contract is peer RPC
only.

## Consequences

- Phase A (#1–#6) implements TLS dial/listen and SAN↔peer checks against this
  contract; production chart enables mTLS by default under
  [ADR-010](ADR-010-mtls-rollout.md)’s fail-closed rule.
- Phase B (#7–#10) adds Vault PKI + ESO templates that produce
  `<fullname>-N-tls` Secrets with the three keys above; rotation drill states
  restart-required until proven otherwise.
- Threat-model T1 / T3 / T8 are **Mitigated** (T6 peer half **Partial**)
  after Phase A verification; residuals (CA compromise, restart-required
  leaf reload, plaintext when TLS unset) remain in
  [threat-model.md](../threat-model.md).
- Connection-per-RPC ([ADR-003](ADR-003-json-tcp-vs-grpc.md)) becomes more
  expensive under TLS; acceptable for M8 demo scale; connection pooling is
  not part of this decision.
- Operators must provision Vault PKI + ESO before a secure cluster comes up;
  local demos use the self-signed script path with the same SAN rules.
