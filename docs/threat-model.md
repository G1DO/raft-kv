# raft-kv — Threat model (STRIDE-lite)

This is a learning prototype, not a production system. The threats below
are real — listed so reviewers don't have to find them by reading the
code, and so remaining M8 work (chaos) has a
clear scope. Phase C network boundary and workload identity are implemented
([ADR-011](decisions/ADR-011-networkpolicy-boundary.md),
[ADR-014](decisions/ADR-014-networkpolicy-egress.md);
[runbook](../runbooks/networkpolicy.md)).

## Scope

In scope:

- A 3-node cluster on a single LAN (kind / k3d / docker compose / bare
  hosts). With peer mTLS configured, the network is **not** trusted for
  Raft RPC authenticity or confidentiality; without TLS config (local
  demos / tests), peer RPC is still plaintext.
- Clients connecting to the line protocol on the node's client port.
- Peer-to-peer RPC on the node's raft port.

Out of scope:

- Multi-tenant operation.
- Full certificate lifecycle automation (Vault/ESO delivery is Phase B;
  dual-trust CA rotation is not claimed until drilled — see residuals).
- Confidentiality of data at rest (the WAL and snapshot files are
  plaintext on disk).
- Client-port TLS / authentication (T2, T9).

## Assets

| Asset | Where | Why it matters |
|---|---|---|
| Committed key-value state | WAL + snapshot on each node's data dir | The thing clients write and read |
| Raft term / vote state | `<data>/raft.state` | Tampering risks safety: an attacker could "vote" as us in a past term and change the outcome of an election |
| Leader identity at time T | In-memory, advertised via `NOT_LEADER <addr>` responses | A malicious leader can reject reads or commit malicious writes |
| Peer TLS private keys + CA | Mounted files / Secrets (when mTLS enabled) | CA or leaf compromise lets an attacker impersonate a voter |

## Trust boundaries

![trust boundary for a raft-kv node](diagrams/rendered/trust-boundary.svg)

Anything that crosses an arrow is hostile. Anything inside the box is
trusted code we wrote. Peer RPC is authenticated only when mTLS is
configured ([ADR-009](decisions/ADR-009-mtls-peer-identity.md),
[ADR-010](decisions/ADR-010-mtls-rollout.md)).

## Threats (STRIDE-lite)

| # | STRIDE | Threat | Vector | Status |
|---|---|---|---|---|
| T1 | **S**poofing | A host that can reach the raft port can pose as a peer | Without TLS: plaintext RPC at [internal/raft/raft.go](../internal/raft/raft.go) (`callRPC` / `handleRPC`). With mTLS: listener requires client certs; inbound RPC must present a leaf whose SAN matches claimed `CandidateID`/`LeaderID` before handlers run ([tls_identity.go](../internal/raft/tls_identity.go)). | **Mitigated** when peer TLS is configured (Phase A verified). Residuals below. Reopens if TLS unset or CA/leaf compromised. |
| T2 | **S**poofing | A client can pose as any other client | The line protocol carries no auth and no `ClientId` (see [design.md "Known correctness gaps"](design.md)). | **Open.** Fix when the protocol gains `ClientId`/`RequestId` (parked). |
| T3 | **T**ampering | Peer RPC traffic can be modified in flight | Without TLS: plaintext TCP. With mTLS: TLS integrity on the peer path; no TLS→plaintext fallback ([ADR-010](decisions/ADR-010-mtls-rollout.md)). | **Mitigated** for peer traffic when mTLS is on. Client port still plaintext. |
| T4 | **T**ampering | An operator (or attacker with disk access) can edit the WAL / state / snapshot files | Files are plaintext, no checksums beyond what record framing provides. | **Partial.** [ADR-004](decisions/ADR-004-panic-on-corrupt-file.md) — the node panics on parse failure, but a *valid-looking* tampered record will be accepted. Out of scope for this project. |
| T5 | **R**epudiation | No audit log: we cannot prove who did what | App security audit to Loki ([ADR-012](decisions/ADR-012-security-audit-events.md), [runbook](../runbooks/audit.md), `./scripts/verify-audit.sh`). | **Partially mitigated.** Network-source audit only (`actor=unauthenticated`, `remote` = `host:port`). Lab retention (~24h). NP drops invisible to the app. User attribution needs client auth (out of M8). |
| T6 | **I**nformation disclosure | Cluster traffic is readable by anyone on the LAN | Peer path: mTLS encrypts Raft RPC when configured. Client path: still plaintext line protocol (keys/values on the wire). | **Partial.** Peer mitigated under mTLS; client TLS is follow-on. |
| T7 | **D**enial of service | Connection-per-RPC ([ADR-003](decisions/ADR-003-json-tcp-vs-grpc.md)) makes the raft port cheap to exhaust | Any **reachable** attacker can open connections faster than `callRPC` retires them. There is no rate limit, no connection cap, no per-peer accounting. mTLS raises handshake cost but does not rate-limit. | **Mitigated (K8s boundary)** when default-deny NetworkPolicy is **enforced** by a capable CNI (Calico/Cilium — [ADR-011](decisions/ADR-011-networkpolicy-boundary.md), [runbook](../runbooks/networkpolicy.md), verified 2026-07-13). Only peer pods may reach :9090. **Not fixed** at the application layer; a compromised peer or labeled client can still connect. |
| T8 | **D**enial of service | A malicious peer can send a `RequestVote` with a higher term and force every node to step down repeatedly | The protocol requires step-down on a higher term. Forged voters need a CA-trusted cert whose SAN matches a cluster member id (T1). A **compromised real member** can still thrash elections. | **Mitigated** against network forgers when mTLS is on (same residuals as T1). |
| T9 | **E**levation of privilege | A client can perform membership changes via the line protocol | `ADD_SERVER` / `REMOVE_SERVER` verbs are exposed on the client port at [internal/server/raft_server.go](../internal/server/raft_server.go) with no authorisation check. Any client that can connect can grow or shrink the cluster. | **Open (app layer).** **Reduced (K8s boundary)** when NetworkPolicy is enforced: only pods labeled `raft-kv.client=true` in namespace `raft-kv-clients` may reach :8080 ([ADR-011](decisions/ADR-011-networkpolicy-boundary.md)). That is **network-authorized**, not authenticated — no proof of who runs `ADD_SERVER`. Client auth is out of M8 scope. |

## Residuals after Phase A (peer mTLS)

Operators should treat these as still true even when the chart has
`tls.enabled: true`:

| Residual | Why it remains | Ops implication |
|---|---|---|
| **CA compromise** | Any leaf signed by the trust-store CA can impersonate a voter if SAN crafting matches a member id | Protect Vault PKI / local CA as a cluster root of trust; rotate CA only with a tested dual-trust window ([ADR-010](decisions/ADR-010-mtls-rollout.md) — **not claimed in M8 yet**) |
| **Leaf private-key theft** | Mounted `tls.key` in the pod (or stolen Secret) is enough to speak as that ordinal | Limit Secret RBAC; Phase B keeps paths out of Git; treat node compromise as voter compromise |
| **Restart-required reload** | raft-kv loads cert/key/CA at startup; no in-process hot reload in M8 | After ESO/Vault renews files, restart the affected StatefulSet pod ([ADR-009](decisions/ADR-009-mtls-peer-identity.md)) |
| **Plaintext when TLS unset** | Fail-closed applies only when paths are set but mounts missing; unset TLS is intentional plaintext (tests, `k8s-up.sh --set tls.enabled=false`) | Never call an unset-TLS deployment “secure”; Helm default is mTLS on |
| **Client port** | Still unauthenticated plaintext (T2, T6 partial, T9 app layer) | NetworkPolicy limits :8080 to labeled clients in `raft-kv-clients` when enforced; not exposing the client Service publicly; `kubectl port-forward` is operator break-glass only |
| **Connection-per-RPC + TLS cost** | Each RPC does a fresh TLS handshake ([ADR-003](decisions/ADR-003-json-tcp-vs-grpc.md)) | Acceptable at demo scale; can amplify election-timer pressure under extreme load (seen in race tests) |
| **NetworkPolicy not enforced** | kindnet and other CNIs ignore NetworkPolicy | Install Calico/Cilium before claiming T7/T9 K8s mitigations ([ADR-013](decisions/ADR-013-chaos-lab-environment.md), [runbook](../runbooks/networkpolicy.md)) |
| **Compromised labeled client** | A pod with `raft-kv.client=true` can use the full line protocol including membership verbs | Treat client namespace/label as a trust boundary; app auth still required for real multi-tenant use |

## Residuals after Phase C (network + workload identity)

| Residual | Why it remains | Ops implication |
|---|---|---|
| **Workload Kubernetes API** | App does not call the API | Dedicated SA `raft-kv`, `automountServiceAccountToken: false`, no workload Role/RoleBinding (`./scripts/verify-workload-identity.sh`) |
| **Egress beyond DNS/peers/OTLP** | Default-deny egress | No arbitrary HTTPS or API egress ([ADR-014](decisions/ADR-014-networkpolicy-egress.md)); OTLP only when tracing enabled |

## Residuals after Phase E (app security audit)

| Residual | Why it remains | Ops implication |
|---|---|---|
| **No client identity** | Line protocol has no auth; audit rows use `actor=unauthenticated` | You see *which IP:port* tried `PUT`, not *which user* — not repudiation-grade attribution |
| **Lab retention** | Loki demo stack (~24h / 2Gi PVC) | Not WORM or compliance storage; extend retention separately if needed |
| **NP deny invisible** | Packets dropped before the process accepts | Do not expect `audit.*` lines for pure NetworkPolicy blocks ([runbook](../runbooks/audit.md)) |
| **Operational log hygiene** | M6 lines are separate from audit | Query with `audit="true"`; audit emit path never records values ([ADR-012](decisions/ADR-012-security-audit-events.md)) |

## What is *not* a threat (and why)

- **A crashed node leaking data.** Files are plaintext but the data is
  the workload itself — there is no "secret" hidden inside, just
  whatever the client put there. (Peer TLS keys *are* secrets; see residuals.)
- **Replay of a stale RPC.** Raft's term and log-index checks already
  reject stale RPCs at the protocol level. The risk is forgery (T1, T3),
  not replay.

## What M8 will fix (remaining)

Already done:

- **Phase A — peer mTLS** mitigates T1, T3, T8 and the peer half of T6 when TLS is configured.
- **Phase B — Vault PKI + ESO delivery** (operational cert lifecycle; rotation drill documents restart-required).
- **Phase C — default-deny NetworkPolicy** reduces T7 and T9 blast radius at the Kubernetes boundary when a capable CNI enforces policies ([ADR-011](decisions/ADR-011-networkpolicy-boundary.md), [ADR-014](decisions/ADR-014-networkpolicy-egress.md), [runbook](../runbooks/networkpolicy.md), `./scripts/verify-networkpolicy.sh`).
- **Phase C — least-privilege workload ServiceAccount** (dedicated `raft-kv` SA, no API token, no workload RBAC; `./scripts/verify-workload-identity.sh`).
- **Phase E — app security audit to Loki** partially mitigates T5 ([ADR-012](decisions/ADR-012-security-audit-events.md), [runbook](../runbooks/audit.md), `./scripts/verify-audit.sh`).

Still in rough order:

1. **Chaos Mesh experiments + postmortems** (Phase F) — measurement harness (#22),
   pod-kill (#23), and network-partition M8 gate (#24) are scripted
   ([runbook](../runbooks/chaos.md)); packet-loss, clock-skew, and incident writeups remain.

Client-side authentication, client TLS, and an authorisation model for
membership changes (T2, T9) are *not* in M8 and are noted here so they
are not silently forgotten.
