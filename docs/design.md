# raft-kv — Design

A small, runnable distributed key-value store built on the Raft consensus
algorithm. Hand-rolled in Go on the standard library, zero third-party
dependencies. This document explains what it is, what it deliberately is not,
how it fails, and the trade-offs that shaped it.

For the rationale behind specific decisions, see the ADRs in
[decisions/](decisions/). For the gaps an attacker would exploit, see the
[threat model](threat-model.md).

## Problem

A learning project that goes far enough to be *real*: not "Raft in tests" but
a cluster you can launch, write to, kill a node of, and watch recover. Concretely:

- A 3-node cluster that elects a leader and replicates writes.
- A simple text protocol so a human can drive it with `nc`.
- A WAL + snapshots, so state survives restart and the log doesn't grow forever.
- Dynamic membership so the cluster can grow or shrink without downtime.
- Linearizable reads (not stale-from-any-follower).

## Goals

- **Correctness of the consensus protocol** under the failures Raft covers:
  leader crash, follower crash, network partition, slow follower.
- **Recoverable persistence**: WAL replay + snapshot install bring a node back
  to the latest committed state.
- **Demonstrable end-to-end**: `scripts/cluster-up.sh` boots a working 3-node
  cluster; `kill -9` the leader and writes still succeed.
- **Defensible design choices** with named, rejected alternatives (see ADRs).

## Non-goals

The point of being explicit about these is that "we didn't build X" is a
deliberate choice, not a missing feature.

- **Multi-region / geo-replication.** Single LAN, low-latency RPC assumed.
- **Multi-key transactions.** State machine is a flat key→value map; each
  command is independent.
- **SQL or secondary indexes.** Get/Put/Delete only.
- **Encryption or authentication of any kind.** Peer RPC is plaintext JSON
  over TCP and unauthenticated. See [threat model](threat-model.md).
- **Joint-consensus membership changes.** Single-server-at-a-time only
  (ADR-001).
- **High-throughput optimisations.** No batching, no pipelining of
  `AppendEntries`, no flush-coalescing of the WAL. The implementation is
  deliberately one-RPC-per-entry-per-peer until a benchmark proves something
  needs to change.
- **Production-grade operability.** No metrics, no structured logs, no health
  endpoints — these are out of scope here.

## Architecture

![raft-kv per-node architecture](diagrams/rendered/architecture.svg)

Concretely:

- `cmd/server/main.go` parses flags and either starts the legacy single-node
  `Server` (no flags, listens on `:8080`) or the cluster-mode `RaftServer`.
- [internal/server/raft_server.go](../internal/server/raft_server.go) speaks
  the line protocol to clients and routes writes through `AppendCommand`,
  reads through `ReadIndex`, and membership through `AddServer`/`RemoveServer`.
- [internal/raft/raft.go](../internal/raft/raft.go) implements the protocol.
  Peer RPCs are dispatched by a string switch on `RPCMessage.Type` (one of
  `RequestVote` / `AppendEntries` / `InstallSnapshot`).
- [internal/log/log.go](../internal/log/log.go) is the WAL: length-prefixed
  binary records, replayed on startup.
- [internal/kv/store.go](../internal/kv/store.go) is the state machine, with
  client-request dedup keyed on `(ClientId, RequestId)`.

## Consistency

Under partition, the design favours **consistency over availability** (CP):

- Writes go through the leader and only commit when a majority acknowledges.
  A minority partition cannot make progress.
- Reads use the [ReadIndex](decisions/ADR-002-readindex-vs-leases.md)
  path: the leader sends a fresh heartbeat round to confirm it is still
  leader before serving the read. A partitioned leader whose heartbeats no
  longer reach a majority returns an error rather than stale data —
  verified at [internal/raft/raft.go:1175-1214](../internal/raft/raft.go#L1175-L1214).
- **Read-from-follower is not supported.** It would be faster but stale.

## Failure modes

| Failure | Detection | Recovery | Client impact |
|---|---|---|---|
| Leader crash | Heartbeats stop reaching followers; election timer fires | Followers elect a new leader within one randomised election timeout (150-300 ms) | Brief write unavailability; client retries against a new node when it sees `NOT_LEADER` |
| Follower crash | Leader's `AppendEntries` to that peer fail | None required for liveness (majority still works); follower catches up via log replication or `InstallSnapshot` on restart | None — writes still commit if majority is alive |
| Network partition | Heartbeats fail; `ReadIndex` cannot confirm leadership | Majority side keeps committing; minority side rejects reads and cannot commit writes | Minority-side clients see errors; majority-side clients keep working |
| Slow follower | Leader's `nextIndex[peer]` lags | Leader keeps retrying `AppendEntries`; falls back to `InstallSnapshot` if the follower is behind the snapshot boundary | None — does not block commit |
| Disk corruption (WAL / state / snapshot) | `NewRaft` fails to parse on startup | **Process panics.** Operator must restore from another node. See [ADR-004](decisions/ADR-004-panic-on-corrupt-file.md). | Node down until manual recovery |
| Process kill during write | WAL flush may be partial | Replay drops the trailing partial record; uncommitted entries are re-replicated by the next leader | At-least-once retry semantics for the client (see below) |

## Deployment (Kubernetes)

The cluster ships as a container image (multi-stage build → `distroless/static`,
non-root uid 65532, ~10 MB; see [Dockerfile](../Dockerfile)) and runs on
Kubernetes as a **StatefulSet, not a Deployment**. A consensus group needs two
things a Deployment cannot give:

- **Stable identity.** Each member must keep the same name and DNS record across
  reschedules, because peers address each other by name. StatefulSet pod
  `raft-kv-0` is always `raft-kv-0`, resolvable at `raft-kv-0.raft-kv` through a
  headless `Service`; a Deployment hands out random pod names.
- **Stable per-member storage.** Each member owns its WAL + snapshots. A
  StatefulSet binds pod `raft-kv-N` to its own `PersistentVolumeClaim`
  (`data-raft-kv-N`) and reattaches the *same* volume after a reschedule, so the
  pod rejoins with its history rather than as a blank node. A Deployment shares
  no stable per-pod volume.

**Peer discovery.** A StatefulSet uses one pod template, so every pod gets the
same command line — yet each Raft node's peer list must exclude itself (quorum is
sized `len(peers)+1`). Rather than generate a per-pod list (the distroless image
has no shell to do so at startup), every pod is handed the *same* `--peers`
spanning the whole cluster plus `--id=$(POD_NAME)` from the downward API, and the
binary drops its own entry ([cmd/server/main.go](../cmd/server/main.go),
`parsePeers`). This is fixed-size static membership over predictable DNS names.

Two consensus-specific gotchas the [chart](../deploy/helm/raft-kv/) handles:
the headless Service sets `publishNotReadyAddresses: true` so peers can resolve
each other to hold the *first* election (which happens before any pod is Ready),
and `podManagementPolicy: Parallel` starts the members together instead of
blocking on a lone `raft-kv-0` that cannot reach quorum by itself.
`scripts/k8s-up.sh` stands the whole thing up on kind.

Validated end-to-end: deleting the leader pod elects a new leader within one
election timeout and the rescheduled pod rejoins from its PVC with no data loss.

## Known correctness gaps

These are real and deliberate. Listed here so reviewers don't have to find them by reading the code.

- **At-least-once writes across failover.** The text protocol carries no
  `ClientId`/`RequestId`, so `KVStore` dedup
  ([internal/kv/store.go:42](../internal/kv/store.go#L42)) never triggers.
  A client that retries a `PUT` after a leader change can apply the write
  twice. Promoting the protocol to carry both IDs is parked for later and
  will be done if measurement work needs deterministic per-client throughput.
- **Peer RPC is unauthenticated and plaintext** — any host that can reach
  the raft port can pose as a peer. See [threat model](threat-model.md);
  fix planned in M8.
- **Two server paths exist** — the legacy single-node `Server` is still
  wired into the no-flag default and three existing tests. Consolidating
  is parked for later.

## Trade-offs (full rationale in ADRs)

| Choice | We picked | We rejected | Why |
|---|---|---|---|
| Membership change | Single-server-at-a-time | Joint consensus | [ADR-001](decisions/ADR-001-single-server-membership.md) |
| Linearizable reads | ReadIndex (heartbeat round) | Leader leases (clock-bound) | [ADR-002](decisions/ADR-002-readindex-vs-leases.md) |
| Peer transport | Hand-rolled JSON-over-TCP | gRPC | [ADR-003](decisions/ADR-003-json-tcp-vs-grpc.md) |
| Corrupt persistent file on boot | Panic | Return error and continue | [ADR-004](decisions/ADR-004-panic-on-corrupt-file.md) |

## What I learned (cross-reference)

The seven lessons in [README.md "What I learned"](../README.md#what-i-learned-building-this)
— randomised timeouts, committed≠applied, dedup state in snapshots, index
math after compaction, config-changes-on-log-not-commit, single-server
membership, ReadIndex must confirm leadership — are the *implementation*
view of the same trade-offs surfaced here. This document is the *design*
view; that section is the *experience report*.
