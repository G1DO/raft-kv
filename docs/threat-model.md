# raft-kv — Threat model (STRIDE-lite)

This is a learning prototype, not a production system. The threats below
are real — listed so reviewers don't have to find them by reading the
code, and so the future mTLS / NetworkPolicy work has a clear scope.

## Scope

In scope:

- A 3-node cluster on a single LAN (kind / k3d / docker compose / bare
  hosts). All nodes mutually trust the network.
- Clients connecting to the line protocol on the node's client port.
- Peer-to-peer RPC on the node's raft port.

Out of scope:

- Multi-tenant operation, secret management, key rotation.
- Confidentiality of data at rest (the WAL and snapshot files are
  plaintext on disk).

## Assets

| Asset | Where | Why it matters |
|---|---|---|
| Committed key-value state | WAL + snapshot on each node's data dir | The thing clients write and read |
| Raft term / vote state | `<data>/raft.state` | Tampering risks safety: an attacker could "vote" as us in a past term and change the outcome of an election |
| Leader identity at time T | In-memory, advertised via `NOT_LEADER <addr>` responses | A malicious leader can reject reads or commit malicious writes |

## Trust boundaries

![trust boundary for a raft-kv node](diagrams/rendered/trust-boundary.svg)

Anything that crosses an arrow is hostile. Anything inside the box is
trusted code we wrote.

## Threats (STRIDE-lite)

| # | STRIDE | Threat | Vector | Status |
|---|---|---|---|---|
| T1 | **S**poofing | A host that can reach the raft port can pose as a peer | Plaintext, unauthenticated peer RPC at [internal/raft/raft.go:861-900](../internal/raft/raft.go#L861-L900). No identity check on `RPCMessage.Type` dispatch. | **Open.** Fix in M8 (mTLS). |
| T2 | **S**poofing | A client can pose as any other client | The line protocol carries no auth and no `ClientId` (see [design.md "Known correctness gaps"](design.md)). | **Open.** Fix when the protocol gains `ClientId`/`RequestId` (parked). |
| T3 | **T**ampering | Peer RPC traffic can be modified in flight | Plaintext TCP, no MAC, no TLS. An on-path attacker can rewrite `AppendEntries` payloads. | **Open.** Fix in M8 (mTLS gives confidentiality + integrity). |
| T4 | **T**ampering | An operator (or attacker with disk access) can edit the WAL / state / snapshot files | Files are plaintext, no checksums beyond what record framing provides. | **Partial.** [ADR-004](decisions/ADR-004-panic-on-corrupt-file.md) — the node panics on parse failure, but a *valid-looking* tampered record will be accepted. Out of scope for this project. |
| T5 | **R**epudiation | No audit log: we cannot prove who did what | No audit / structured log today. | **Open.** Track B (M6 observability) addresses this for operability; security audit is M8. |
| T6 | **I**nformation disclosure | Cluster traffic is readable by anyone on the LAN | Plaintext for both client and peer RPC. Includes keys, values, and committed log entries. | **Open.** Fix in M8 (mTLS) for peer traffic; client-side TLS is a separate, follow-on item. |
| T7 | **D**enial of service | Connection-per-RPC ([ADR-003](decisions/ADR-003-json-tcp-vs-grpc.md)) makes the raft port cheap to exhaust | Any reachable attacker can open connections faster than `callRPC` retires them. There is no rate limit, no connection cap, no per-peer accounting. | **Open.** Mitigated in M8 by a default-deny NetworkPolicy (only peers and clients can reach the ports). Not fixed at the application layer. |
| T8 | **D**enial of service | A malicious peer can send a `RequestVote` with a higher term and force every node to step down repeatedly | The protocol requires us to step down on a higher term — that's correctness. Without peer auth (T1) we cannot tell a real higher-term peer from a forged one. | **Open.** Same fix as T1: mTLS. |
| T9 | **E**levation of privilege | A client can perform membership changes via the line protocol | `ADD_SERVER` / `REMOVE_SERVER` verbs are exposed on the client port at [internal/server/raft_server.go:141-157](../internal/server/raft_server.go#L141-L157) with no authorisation check. Any client that can connect can grow or shrink the cluster. | **Open.** Fix is a client-side auth story (not scoped to M8); for now, rely on network policy to keep the client port inaccessible to untrusted callers. |

## What is *not* a threat (and why)

- **A crashed node leaking data.** Files are plaintext but the data is
  the workload itself — there is no "secret" hidden inside, just
  whatever the client put there.
- **Replay of a stale RPC.** Raft's term and log-index checks already
  reject stale RPCs at the protocol level. The risk is forgery (T1, T3),
  not replay.

## What M8 will fix

In rough order:

1. **mTLS for peer RPC.** Closes T1, T3, T6, T8.
2. **Default-deny NetworkPolicy.** Closes T7 in the Kubernetes deployment
   and reduces blast radius for everything else.
3. **Least-privilege ServiceAccount + RBAC** on the workload pod itself.
4. **Audit logs to Loki.** Closes T5 to the extent observability gives a
   security signal.

Client-side authentication, client TLS, and an authorisation model for
membership changes (T2, T9) are *not* in M8 and are noted here so they
are not silently forgotten.
