# raft-kv

A distributed key-value store built on the Raft consensus algorithm. From scratch, in Go.

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

```bash
# Build
go build -o raft-kv ./cmd/server

# Run a single node (for testing)
./raft-kv --id node1 --addr localhost:8001 --data ./data/node1

# Run a 3-node cluster
./raft-kv --id node1 --addr localhost:8001 --peers localhost:8002,localhost:8003 --data ./data/node1
./raft-kv --id node2 --addr localhost:8002 --peers localhost:8001,localhost:8003 --data ./data/node2
./raft-kv --id node3 --addr localhost:8003 --peers localhost:8001,localhost:8002 --data ./data/node3
```

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

## Resources

- [Raft Paper](https://raft.github.io/raft.pdf) — Read this multiple times
- [Raft Visualization](https://thesecretlivesofdata.com/raft/) — Helped a lot early on
- [Students' Guide to Raft](https://thesquareplanet.com/blog/students-guide-to-raft/) — Covers common implementation mistakes
