# raft-kv

A distributed key-value store built on the Raft consensus algorithm. From scratch, in Go.

> **Status:** The single-node KV store (PUT/GET/DELETE over TCP, persistent WAL) runs
> today. The full Raft layer — leader election, log replication, snapshots, dynamic
> membership — is implemented and tested as a library, but is not yet wired into the
> runnable binary; doing so is the next milestone.

## What is this?

Multiple servers that agree on data, even when some crash or lose network connectivity. Write to one node, the cluster makes sure everyone agrees, your data survives failures.

This is how real systems like **etcd**, **CockroachDB**, and **TiKV** work under the hood. I built it to actually understand it, not just read about it.

```
                    Client
                       |
                       v
                 +-----------+
                 |  Leader   |
                 |  Node 1   |
                 +-----------+
                       |
         replicates to all followers
                       |
       +---------------+---------------+
       |               |               |
       v               v               v
 +-----------+   +-----------+   +-----------+
 | Follower  |   | Follower  |   | Follower  |
 |  Node 2   |   |  Node 3   |   |  Node 4   |
 +-----------+   +-----------+   +-----------+

 If leader dies -> followers elect new one -> no data loss
```

---

## Why I built this

I wanted to understand distributed consensus at a level where I could actually debug it, not just talk about it. Reading the Raft paper is one thing. Implementing it and watching nodes argue about who's leader at 2am is another.

---

## How it works

### Leader Election

When a node doesn't hear from a leader for a while, it starts an election. It asks other nodes for votes. If it gets a majority, it becomes the new leader.

```
  Node 1              Node 2              Node 3
    |                   |                   |
    | (timeout)         |                   |
    |------------------>|                   |
    |    RequestVote    |                   |
    |<------------------|                   |
    |     vote: yes     |                   |
    |                   |                   |
    |---------------------------------------->
    |              RequestVote              |
    |<----------------------------------------
    |               vote: yes               |
    |                   |                   |
    |===========================================
    |       I have majority -> I'm leader   |
```

The trick is randomized timeouts. Without them, all nodes would timeout at the same time and keep fighting over who gets to be leader. I learned this the hard way.

### Log Replication

Leader gets a command, writes it to its log, sends it to followers. Once a majority have it, the entry is "committed" — meaning it's safe and won't be lost even if the leader crashes.

```
  Leader Log:     [PUT x=1] [PUT y=2] [DEL x]
                      |         |        |
                      v         v        v
  Follower 1:     [PUT x=1] [PUT y=2] [DEL x]   <-- in sync
  Follower 2:     [PUT x=1] [PUT y=2]           <-- slightly behind
  Follower 3:     [PUT x=1]                     <-- more behind

  Committed = majority has it = safe forever
```

There's a subtle distinction here: "committed" means it's replicated to a majority (safe). "Applied" means the state machine actually executed it. A committed entry might not be applied yet — this confused me for a while.

### Persistence

Everything important goes to disk. When a node restarts, it reloads:
- `currentTerm` and `votedFor` — so it doesn't vote twice in the same election
- Log entries — so it can rebuild state

```
  Crash!                           Restart
    |                                 |
    v                                 v
+-----------+                   +-----------+
|  Memory   |                   |  Memory   |
|  (gone)   |                   |  (empty)  |
+-----------+                   +-----+-----+
                                      | reload
+-----------+                   +-----v-----+
|   Disk    | ----------------> |   Disk    |
|  (intact) |                   |  (intact) |
+-----------+                   +-----------+
```

### Duplicate Detection

Networks are unreliable. Clients retry requests when they don't get a response. Without deduplication, a retry could execute the same command twice.

```
  Client sends: PUT counter 1 (requestId=5)

  Network drops response, client retries:

  PUT counter 1 (requestId=5)  -> First time: executes, returns OK
  PUT counter 1 (requestId=5)  -> Retry: returns cached OK, doesn't execute again

  Counter is 1, not 2.
```

Each client has a unique ID and a monotonically increasing request ID. The server remembers the last request ID per client and rejects anything it's already seen.

### Snapshots

Logs grow forever. After a while, you don't want to store every command since the beginning of time. Snapshots capture the current state and let you throw away old log entries.

```
  BEFORE snapshot:
  Log: [cmd1] [cmd2] [cmd3] [cmd4] [cmd5] [cmd6] [cmd7]
  State: { x: 10, y: 20 }

  AFTER snapshot at index 5:
  Log:                             [cmd6] [cmd7]
  Snapshot: { x: 10, y: 20, lastIndex: 5, lastTerm: 2 }

  Same state, way less storage.
```

Snapshots are persisted to disk using an atomic write pattern (write to temp file, then rename) to prevent corruption during crashes.

### InstallSnapshot RPC

When a follower falls too far behind — the entries it needs have already been compacted — the leader sends its snapshot instead of log entries.

```
  Leader: "Your nextIndex is 100, but I compacted up to 900000."
  Leader: "Here's my snapshot. Load this, then we continue."

  Follower receives snapshot:
    1. Verify term (reject stale leaders)
    2. Persist snapshot to disk (crash safety)
    3. Discard log entries covered by snapshot
    4. Signal state machine to restore from snapshot
    5. Update commitIndex and lastApplied
```

This is essential for adding new nodes to a cluster — they can't replay a log from the beginning of time.

### Linearizable Reads (ReadIndex)

Problem: A partitioned leader might serve stale data. It doesn't know it's been replaced.

Solution: Before serving a read, confirm you're still leader.

```
  Client: GET x

  Leader (maybe stale):
    1. Record current commitIndex
    2. Send heartbeat to all peers
    3. Wait for majority to respond
    4. If majority confirms -> serve read at commitIndex
    5. If no majority -> reject (might be partitioned)
```

This guarantees reads see all committed writes, even during network partitions.

### Dynamic Cluster Membership

Servers can be added or removed at runtime using single-server changes. The key insight: changing one server at a time guarantees old and new majorities overlap.

```
  3-node cluster: [A, B, C]
  Majority = 2

  Adding D:
  - Leader logs config change: AddServer(D)
  - Config takes effect IMMEDIATELY (when logged, not committed)
  - Now 4-node cluster: [A, B, C, D]
  - New majority = 3
  - Old majority (2) and new majority (3) overlap

  This overlap prevents split-brain.
```

Config changes are stored in the log and replayed on restart. When a leader removes itself, it steps down to follower.

---

## Architecture

```
+------------------------------------------------------------------+
|                            Client                                |
|                              |                                   |
|                        PUT/GET/DELETE                            |
+------------------------------------------------------------------+
                               |
           +-------------------+-------------------+
           |                   |                   |
           v                   v                   v
  +----------------+  +----------------+  +----------------+
  |    Node 1      |  |    Node 2      |  |    Node 3      |
  |   (Leader)     |  |  (Follower)    |  |  (Follower)    |
  |                |  |                |  |                |
  | +------------+ |  | +------------+ |  | +------------+ |
  | |  KVStore   | |  | |  KVStore   | |  | |  KVStore   | |
  | |  (state)   | |  | |  (state)   | |  | |  (state)   | |
  | +-----^------+ |  | +-----^------+ |  | +-----^------+ |
  |       | apply  |  |       | apply  |  |       | apply  |
  | +-----+------+ |  | +-----+------+ |  | +-----+------+ |
  | |    Raft    |<----->    Raft    |<----->    Raft    | |
  | |   Module   | |  | |   Module   | |  | |   Module   | |
  | +-----+------+ |  | +-----+------+ |  | +-----+------+ |
  |       |        |  |       |        |  |       |        |
  | +-----v------+ |  | +-----v------+ |  | +-----v------+ |
  | | Log + Snap | |  | | Log + Snap | |  | | Log + Snap | |
  | |   (disk)   | |  | |   (disk)   | |  | |   (disk)   | |
  | +------------+ |  | +------------+ |  | +------------+ |
  +----------------+  +----------------+  +----------------+
           |                   |                   |
           +-------------------+-------------------+
                               |
              AppendEntries / RequestVote / InstallSnapshot RPCs
```

The Raft module doesn't know anything about key-value operations. It just sees bytes. The KVStore interprets those bytes as PUT/GET/DELETE commands. This separation is important — Raft is a consensus algorithm, not a database.

### RPC Types

| RPC | Purpose | When Used |
|-----|---------|-----------|
| **RequestVote** | Request vote during election | Candidate to all peers |
| **AppendEntries** | Heartbeat + log replication | Leader to followers (50ms interval) |
| **InstallSnapshot** | Send snapshot to far-behind follower | Leader to follower when nextIndex <= lastIncludedIndex |

---

## Project Structure

```
raft-kv/
|-- cmd/server/          # Entry point
|-- internal/
|   |-- kv/              # Key-value state machine
|   |   |-- store.go     # PUT/GET/DELETE + snapshots + dedup state
|   |   +-- store_test.go
|   |-- log/             # Persistent append-only log
|   |   |-- log.go
|   |   +-- log_test.go
|   |-- raft/            # Consensus module
|   |   |-- raft.go      # Election, replication, snapshots, membership, ReadIndex
|   |   |-- state.go     # Persistence (term, votedFor, snapshots)
|   |   +-- *_test.go
|   +-- server/          # TCP server, client handling
|       |-- server.go
|       +-- server_test.go
+-- README.md
```

### Key Types

| Type | Location | Purpose |
|------|----------|---------|
| `Raft` | raft.go | Core consensus state machine |
| `LogEntry` | raft.go | Log entry with term, command, and optional config change |
| `ConfigChange` | raft.go | Membership change (add/remove server) |
| `ApplyMsg` | raft.go | Message sent to state machine (command or snapshot) |
| `KVStore` | store.go | State machine that interprets commands |
| `RaftState` | state.go | Persisted term and votedFor |
| `SnapshotState` | state.go | Persisted snapshot metadata and data |

---

## Running

With no flags the binary runs as a **single node without Raft**: it listens on
`localhost:8080` and writes its log to `data/raft.log`. Passing `--id` switches it into
**cluster mode**, where client commands go through Raft consensus.

```bash
# Build
go build -o raft-kv ./cmd/server

# Run a single node (no Raft), listening on localhost:8080
./raft-kv
```

Talk to it over the line protocol. Commands are **case-sensitive** and
**single-space-delimited**:

| Command | Result |
|---|---|
| `PUT <key> <value>` | `OK` |
| `GET <key>` | the value, or `NOT_FOUND` |
| `DELETE <key>` | `OK` |

```bash
$ printf 'PUT foo bar\nGET foo\nDELETE foo\nGET foo\n' | nc localhost 8080
OK
bar
OK
NOT_FOUND
```

### Running a Raft cluster

In cluster mode each node needs its **own** client port, peer-RPC port, and data dir.
The `--peers` flag lists the *other* nodes as `id@raftAddr@clientAddr`:

| Flag | Meaning |
|---|---|
| `--id` | this node's id (enables cluster mode) |
| `--addr` | client/KV listen address |
| `--raft-addr` | peer-RPC listen address |
| `--peers` | other nodes as `id@raftAddr@clientAddr`, comma-separated |
| `--data` | per-node data directory |

The quickest way to start a local 3-node cluster:

```bash
./scripts/cluster-up.sh        # nodes on client ports 8081/8082/8083
```

Only the **leader** accepts writes and serves linearizable reads (via ReadIndex). A
non-leader replies `NOT_LEADER <leaderClientAddr>` — point your client at that address:

```bash
$ printf 'PUT foo bar\n' | nc localhost 8081
NOT_LEADER 127.0.0.1:8083        # 8081 is a follower; the leader is on 8083
$ printf 'PUT foo bar\nGET foo\n' | nc localhost 8083
OK
bar
```

Membership can be changed at runtime against the leader:

| Command | Result |
|---|---|
| `ADD_SERVER <id> <raftAddr>` | `OK` (or `NOT_LEADER <addr>`) |
| `REMOVE_SERVER <id> <raftAddr>` | `OK` (or `NOT_LEADER <addr>`) |

`kill -9` the leader and a new one is elected within a couple of seconds; committed data
is preserved and a restarted node rejoins by replaying its data dir. Nodes shut down
gracefully on SIGINT/SIGTERM (step down, close listener).

> **Known limitation:** the text protocol sends raw commands with no client/request id,
> so write retries across a failover are *at-least-once* (the `KVStore` dedup in
> `store.go` only triggers for JSON `Command` payloads).

---

## Verifying the published image

CI builds a distroless image and publishes it to
[`ghcr.io/g1do/raft-kv`](https://github.com/G1DO/raft-kv/pkgs/container/raft-kv) on every
push to `main` and every `v*` tag. Each published image is **signed** with
[cosign](https://github.com/sigstore/cosign) and carries a **SLSA build-provenance** and an
**SPDX SBOM** attestation. All three are *keyless*: GitHub mints a short-lived OIDC token,
Fulcio issues a ~10-minute signing certificate from it, and the result is recorded in the
Rekor transparency log — no private keys, no repository secrets.

Everything binds to the image **digest**, not a tag, so resolve a tag to its digest first
and verify against that:

```bash
# Resolve a tag to its immutable digest reference.
REF="ghcr.io/g1do/raft-kv@$(crane digest ghcr.io/g1do/raft-kv:main)"
# Without crane:
# REF="ghcr.io/g1do/raft-kv@$(docker buildx imagetools inspect ghcr.io/g1do/raft-kv:main --format '{{.Manifest.Digest}}')"
echo "$REF"
```

### Signature (cosign)

Requires **cosign v3+** (CI signs with v3.0.6):

```bash
cosign verify \
  --certificate-identity-regexp '^https://github.com/G1DO/raft-kv/\.github/workflows/image\.yml@' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  "$REF"
```

The identity regexp pins the signature to **this repo's `image.yml` workflow**, and the
issuer to GitHub's OIDC endpoint that Fulcio validates — a signature from any other
workflow, repo, or issuer fails. Only `main` and `v*` tags ever sign (PRs build but never
push), so a valid signature comes from a release run.

### Build provenance (gh)

```bash
gh attestation verify "oci://$REF" \
  --repo G1DO/raft-kv \
  --signer-workflow G1DO/raft-kv/.github/workflows/image.yml
```

`gh attestation verify` defaults to the SLSA provenance predicate
(`https://slsa.dev/provenance/v1`), so this checks the provenance. `--signer-workflow`
enforces that *this* workflow produced it — `--repo` alone would accept an attestation
from any workflow in the repo.

### SBOM (gh)

The image also carries an SPDX SBOM attestation; verify it by selecting the SBOM predicate
type:

```bash
gh attestation verify "oci://$REF" \
  --repo G1DO/raft-kv \
  --signer-workflow G1DO/raft-kv/.github/workflows/image.yml \
  --predicate-type "$SPDX_URI"
```

> `actions/attest-sbom@v4` emits the SPDX predicate type `https://spdx.dev/Document/v2.3`
> (read off the published attestation) — set `SPDX_URI` to that. The exact URI is
> version-specific, so re-confirm it against a real attestation if you bump `attest-sbom`. Note
> that `gh attestation verify` always filters to a single predicate (provenance by default), so
> it can't be used to *discover* the SBOM predicate — you must pass `--predicate-type` with the
> value above.

Because the runtime image is a single static Go binary on `distroless/static`, the SBOM
is intentionally small — the Go toolchain/stdlib (read from the binary's embedded build
info), the OpenTelemetry modules used for trace export (the project's only third-party
Go dependencies, see [ADR-007](docs/decisions/ADR-007-otel-vs-zero-dep-tracing.md)),
plus the few distroless base packages.

---

## GitOps delivery (Argo CD)

The Kubernetes deploy is **declarative and pull-based**: an Argo CD `Application`
([deploy/argo/application.yaml](deploy/argo/application.yaml)) watches this repo and
reconciles the [Helm chart](deploy/helm/raft-kv) into the cluster — `automated` sync with
`prune` + `selfHeal`, so the live cluster is kept equal to git and out-of-band edits are
reverted. The rollout knob is the image tag in the `Application`: bump it in git, Argo rolls
it out. Code and chart live in one repo on purpose — monorepo vs. config-repo split is
[ADR-005](docs/decisions/ADR-005-monorepo-vs-config-repo.md).

Demonstrated end-to-end on kind: `Synced + Healthy`, 3/3 running the signed GHCR image; a
`kubectl scale` was reverted by selfHeal within seconds, and a `values.yaml` change pushed
to `main` rolled out on the next sync. One honest limitation: Argo reconciles *Kubernetes
objects, not Raft membership* — bumping `replicaCount` grows the StatefulSet and regenerates
`--peers`, rolling-restarting the pods, but the new pods are **not** added as voters through
joint-consensus `AddServer`. Declarative deploys: yes. Safe consensus reconfiguration: not
from the chart — flagged in ADR-005 and tracked as future work.

## Admission policy (signature enforcement)

Signing the image only matters if the cluster refuses to run unsigned ones. A
[sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/)
`ClusterImagePolicy` ([deploy/policy/](deploy/policy/)) pins admission to **this repo's
keyless signing identity** — issuer `token.actions.githubusercontent.com`, subject
`…/image.yml@…`, Rekor inclusion required — in any namespace labelled
`policy.sigstore.dev/include: "true"`. The controller's `no-match-policy` is `deny` by
default, so unmatched images are rejected too. Why policy-controller over Kyverno:
[ADR-006](docs/decisions/ADR-006-policy-controller-vs-kyverno.md).

Demonstrated on kind: an unsigned image (`busybox`) is **denied at admission** while
un-opted-in namespaces stay untouched. One honest caveat — policy-controller (through v0.15.1)
can't yet verify cosign's modern *bundle*-format signatures (only attestations), so `image.yml`
**dual-signs** every image (a bundle for `cosign verify` plus a legacy `.sig`) and the policy
verifies the legacy one; that admit path is config-correct and confirms on the next signed
publish. Full runbook: [deploy/policy/README.md](deploy/policy/README.md).

## Pod posture admission (Kyverno)

Image signatures and pod hardening use **separate** admission engines
([ADR-006](docs/decisions/ADR-006-policy-controller-vs-kyverno.md)): policy-controller
verifies cosign; [Kyverno](https://kyverno.io/) will enforce non-root, capability,
probe, and resource posture on `raft-kv` pods (M8 Phase D). The controller installs
as platform infrastructure — its own Argo CD Application
([deploy/argo/kyverno-app.yaml](deploy/argo/kyverno-app.yaml),
[deploy/platform/kyverno/](deploy/platform/kyverno/)) — not inside the app chart.
Lab: `./scripts/install-kyverno.sh`. Policies and Audit→Enforce rollout are #16–#17.

---

## Observability

The consensus system's internal state is first-class telemetry, correlated across all
three signals by a per-request trace ID:

- **Metrics** — a hand-rolled, zero-dependency Prometheus registry
  (`internal/metrics/`, no `client_golang`) served on `--metrics-addr` (default `:2112`,
  alongside `/healthz` and `/readyz`). Raft internals are instrumented at their mutation
  points: `raft_term`, `raft_is_leader`, `raft_elections_total`,
  `raft_election_duration_seconds`, `raft_commit_index` **and** `raft_applied_index`
  (committed ≠ applied, made visible), `raft_log_length`, `raft_last_included_index`, and
  `raft_commit_latency_seconds` — leader-side append→commit time, the live version of the
  benchmark's p99 number. The client API gets RED metrics:
  `raftkv_requests_total{op,result}` and `raftkv_request_duration_seconds{op}`.
- **Logs** — stdlib `log/slog`, JSON to stdout. Elections, step-downs, snapshot installs,
  membership changes, and previously black-holed `callRPC` transport failures are all
  logged; every client request carries a `trace_id` across its lines.
- **Traces** — OpenTelemetry spans at the server layer (`raftkv.request` →
  `raft.append` → `raft.replicate_commit_apply` / `raft.read_index` → `raft.apply_wait`),
  exported OTLP straight to Tempo when `--otlp-endpoint` is set (off by default). The
  OTel SDK is the project's **only third-party dependency**; the consensus core stays
  stdlib-only — the trade-off is recorded in
  [ADR-007](docs/decisions/ADR-007-otel-vs-zero-dep-tracing.md). With tracing on, the
  log `trace_id` *is* the OTel trace ID, so a Loki log line links straight to its Tempo
  waterfall.

The stack itself (kube-prometheus-stack + Loki + Promtail + Tempo) deploys as a separate
Argo CD Application from [deploy/observability/](deploy/observability/), with Grafana
dashboards committed as JSON (Raft internals, client-API RED, write-SLO burn rate).
`./scripts/observability-demo.sh` runs the repeatable failover demo: kill the leader
under load and watch the same incident as a leader-timeline flip, an election-counter
tick, a commit-latency spike, the `election_won` log line, and the slow request's trace.

---

## Reliability

The Helm chart (`deploy/helm/raft-kv`) treats the StatefulSet as a **consensus
group**, not a horizontally scalable web tier. Rationale and rejected
alternatives:
[docs/design.md](docs/design.md#reliability),
[ADR-008](docs/decisions/ADR-008-quorum-aware-reliability.md).
Backup/restore (wipe vs disaster) and measured MTTR:
[docs/runbooks/restore.md](docs/runbooks/restore.md).

- **Probes** — `startup`/`liveness` hit `/healthz` (process alive; never leadership).
  `readiness` hits `/readyz` → `Raft.Ready()` with probe hysteresis so elections
  do not flap endpoints.
- **Resources** — CPU **request only** (no limit: throttling elections is an
  availability risk). Memory request == limit, plus `GOMEMLIMIT` ≈ 90% of that
  limit so snapshot GC does not OOMKill a voter.
- **PDB** — `maxUnavailable: 1` so a `kubectl drain` cannot voluntarily remove
  enough pods to break quorum. Helm refuses a knob that would
  (`maxUnavailable ≥ ceil(replicaCount/2)`). PDBs do **not** stop OOM, node
  crashes, liveness restarts, `kubectl delete pod`, or StatefulSet rolling
  updates — rolling updates are serialized by the Ready gate, not the PDB.
- **No HPA** — more voters do not increase write throughput (every write still
  needs a majority), and autoscaler scale-down can destroy quorum. Membership
  changes are deliberate one-at-a-time Raft config changes. Read replicas would
  need non-voting learners (not implemented).
- **Rolling updates** — explicit `RollingUpdate`; optional `partition` for a
  one-pod canary. Serialized by `/readyz`, not the PDB. See
  [design.md](docs/design.md#binary-upgrades-one-pod-canary).

---

## Testing

```bash
# Run all tests
go test ./...

# Verbose
go test ./... -v

# Specific package
go test ./internal/raft/... -v
go test ./internal/kv/... -v
```

---

## Benchmarks

Measured on a local 3-node cluster by the `cmd/bench` harness (full method, tables,
and caveats in **[docs/benchmarks.md](docs/benchmarks.md)**). Single-machine,
loopback numbers — the point is the *shape*, not datacenter figures.

| What | Result | How |
|---|---|---|
| **Election MTTR** (kill leader → new leader serves writes) | **p50 244 ms · p99 342 ms** over 100 trials | `go run ./cmd/bench mttr` |
| **Write throughput** | peak **~144 writes/sec** (8 clients); latency floor **~50 ms** = the heartbeat interval | `go run ./cmd/bench throughput` |
| **Quorum loss** | minority leader **refuses** the linearizable read (`NOT_LEADER`); recovers in ~50 ms, data preserved | `go run ./cmd/bench partition` |

Two honest findings worth calling out: write latency is floored at the **50 ms
heartbeat interval** (replication is heartbeat-driven), and throughput **collapses
past ~8 concurrent writers** as writes hit the 3 s commit timeout — a contention
cliff, documented rather than hidden.

A scripted demo of an induced failure → re-election → successful write:

```bash
./scripts/chaos-demo.sh
```

<!-- TODO: record an asciinema cast of chaos-demo.sh and link it here:
     asciinema rec demo.cast -c ./scripts/chaos-demo.sh   (then upload & paste the URL) -->

---

## What I learned building this

**1. Randomized timeouts aren't optional**

I tried fixed timeouts first. Nodes kept timing out at the same time, all became candidates, split the vote, timed out again... forever. Random jitter fixes it. The Raft paper mentions this but you don't really feel it until you watch your cluster deadlock.

**2. "Committed" ≠ "Applied"**

This tripped me up. An entry is committed when a majority has it — it's safe. But it's not applied until the state machine actually executes it. A leader might have committed entries it hasn't applied yet. You have to track both.

**3. Duplicate detection is harder than it looks**

My first attempt: `if requestId == lastRequestId { return cached }`. Wrong. Old requests can arrive late due to network delays. You need `requestId <= lastRequestId` to catch stale messages. And you have to persist this across restarts. And include it in snapshots.

**4. Snapshots need to include dedup state**

I forgot this initially. After restoring from a snapshot, old duplicate requests would execute again because the server didn't remember what it had already processed. The snapshot has to include the duplicate detection tables, not just the key-value data.

**5. Index math after compaction is annoying**

After you snapshot and throw away log entries, `log[0]` isn't index 1 anymore. You need `lastIncludedIndex` to convert between array indices and logical Raft indices. Off-by-one errors everywhere until I got this right.

**6. Config changes must take effect immediately**

I initially thought membership changes should take effect when committed, like regular commands. Wrong. They take effect when *logged*. Why? Consider adding a fourth node: if you wait for commit, the new node might already be receiving entries but isn't counted in the majority calculation. The Raft paper is subtle here.

**7. Single-server changes prevent split-brain**

You can't add multiple servers at once. With 3 nodes, adding 2 simultaneously could let the new nodes form a majority (3/5) while the old nodes also have a majority (2/3). Disaster. One at a time guarantees old and new majorities always overlap.

**8. ReadIndex needs to confirm leadership**

A partitioned leader can keep serving reads forever without knowing it's been replaced. The solution is simple but non-obvious: before serving any read, send heartbeats and wait for majority response. If you can't reach a majority, you might be partitioned — reject the read.

**9. Snapshot installation is where things get weird**

When a follower receives a snapshot, it has to throw away its entire log and state machine. But what if there are log entries *after* the snapshot boundary? Keep them. And always persist the snapshot *before* updating in-memory state — crashes happen.

---

## What's done

- [x] **Phase 1: Foundation** — Persistent log, KV state machine, TCP server
- [x] **Phase 2: Leader Election** — Terms, voting, election timeout, heartbeats
- [x] **Phase 3: Log Replication** — AppendEntries, conflict resolution, commit logic
- [x] **Phase 4: Safety** — Persistence, duplicate detection
- [x] **Phase 5: Snapshots** — Log compaction, state serialization, snapshot persistence
- [x] **Phase 6: InstallSnapshot RPC** — Snapshot transfer to far-behind followers
- [x] **Phase 7: Linearizable Reads** — ReadIndex for consistent reads
- [x] **Phase 8: Cluster Membership** — Dynamic add/remove servers

---

## Design & decisions

- [docs/design.md](docs/design.md) — problem, goals & non-goals, failure modes, consistency stance.
- [docs/decisions/](docs/decisions/) — ADRs, each naming the rejected alternative:
  - [ADR-001](docs/decisions/ADR-001-single-server-membership.md) — single-server membership vs. joint consensus
  - [ADR-002](docs/decisions/ADR-002-readindex-vs-leases.md) — ReadIndex vs. lease-based reads
  - [ADR-003](docs/decisions/ADR-003-json-tcp-vs-grpc.md) — hand-rolled JSON-over-TCP vs. gRPC
  - [ADR-004](docs/decisions/ADR-004-panic-on-corrupt-file.md) — panic vs. return-error on corrupt persistent file
  - [ADR-005](docs/decisions/ADR-005-monorepo-vs-config-repo.md) — monorepo vs. separate config repo
  - [ADR-006](docs/decisions/ADR-006-policy-controller-vs-kyverno.md) — sigstore policy-controller vs. Kyverno
  - [ADR-007](docs/decisions/ADR-007-otel-vs-zero-dep-tracing.md) — OpenTelemetry vs. strictly zero-dep tracing
  - [ADR-008](docs/decisions/ADR-008-quorum-aware-reliability.md) — quorum-aware probes, PDB, resources
  - [ADR-009](docs/decisions/ADR-009-mtls-peer-identity.md) — peer mTLS cert/identity (Vault PKI + per-ordinal SANs)
  - [ADR-010](docs/decisions/ADR-010-mtls-rollout.md) — peer mTLS fail-closed rollout (no plaintext fallback)
  - [ADR-011](docs/decisions/ADR-011-networkpolicy-boundary.md) — NetworkPolicy client/peer/metrics boundary
  - [ADR-014](docs/decisions/ADR-014-networkpolicy-egress.md) — NetworkPolicy egress (DNS, peers, OTLP) + workload API posture
  - [ADR-012](docs/decisions/ADR-012-security-audit-events.md) — app security audit events (not K8s API audit)
  - [ADR-013](docs/decisions/ADR-013-chaos-lab-environment.md) — chaos lab (kind + Calico + Chaos Mesh 2.8.3)
- [docs/runbooks/restore.md](docs/runbooks/restore.md) — backup / wipe / disaster restore + MTTR
- [docs/runbooks/tls-certificates.md](docs/runbooks/tls-certificates.md) — Vault/ESO peer TLS bootstrap, renewal, revocation
- [docs/runbooks/networkpolicy.md](docs/runbooks/networkpolicy.md) — default-deny NetworkPolicy boundary + verification
- [deploy/platform/tls-delivery/README.md](deploy/platform/tls-delivery/README.md) — scoped Vault policy + ESO RBAC examples
- [docs/threat-model.md](docs/threat-model.md) — STRIDE-lite; peer mTLS status + M8 residuals.
- [docs/benchmarks.md](docs/benchmarks.md) — measured election MTTR, throughput, and quorum-loss behaviour, with the exact method.

---

## Resources

- [Raft Paper](https://raft.github.io/raft.pdf) — Read this multiple times
- [Raft Visualization](https://thesecretlivesofdata.com/raft/) — Helped a lot early on
- [Students' Guide to Raft](https://thesquareplanet.com/blog/students-guide-to-raft/) — Covers common implementation mistakes
