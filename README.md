# raft-kv

A distributed key-value store built on the Raft consensus algorithm. From scratch, in Go.

## What is this?

Multiple servers that agree on data, even when some crash or lose network connectivity. Write to one node, the cluster makes sure everyone agrees, your data survives failures.

This is how real systems like **etcd**, **CockroachDB**, and **TiKV** work under the hood. I built it to actually understand it, not just read about it.

```
           Client
              |
              v
    +------------------+
    |     Leader       |-------- replicates to --------+
    |    (Node 1)      |                               |
    +------------------+                               |
              |                                        |
              |             +-------------+------------+------------+
              |             |             |            |            |
              v             v             v            v            v
    +------------------+  +------------------+  +------------------+
    |    Follower      |  |    Follower      |  |    Follower      |
    |    (Node 2)      |  |    (Node 3)      |  |    (Node 4)      |
    +------------------+  +------------------+  +------------------+

    If leader dies -> followers elect a new one -> no data loss
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
  | |    Log     | |  | |    Log     | |  | |    Log     | |
  | |   (disk)   | |  | |   (disk)   | |  | |   (disk)   | |
  | +------------+ |  | +------------+ |  | +------------+ |
  +----------------+  +----------------+  +----------------+
           |                   |                   |
           +-------------------+-------------------+
                               |
                    AppendEntries / RequestVote RPCs
```

The Raft module doesn't know anything about key-value operations. It just sees bytes. The KVStore interprets those bytes as PUT/GET/DELETE commands. This separation is important — Raft is a consensus algorithm, not a database.

---

## Project Structure

```
raft-kv/
|-- cmd/server/          # Entry point
|-- internal/
|   |-- kv/              # Key-value state machine
|   |   |-- store.go     # PUT/GET/DELETE + snapshots
|   |   +-- store_test.go
|   |-- log/             # Persistent append-only log
|   |   |-- log.go
|   |   +-- log_test.go
|   |-- raft/            # Consensus module
|   |   |-- raft.go      # Election, replication, commit
|   |   |-- state.go     # Persistence (term, votedFor)
|   |   +-- *_test.go
|   +-- server/          # TCP server, client handling
|       |-- server.go
|       +-- server_test.go
+-- README.md
```

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

---

## What's done

- [x] **Phase 1: Foundation** — Persistent log, KV state machine, TCP server
- [x] **Phase 2: Leader Election** — Terms, voting, election timeout, heartbeats
- [x] **Phase 3: Log Replication** — AppendEntries, conflict resolution, commit logic
- [x] **Phase 4: Safety** — Persistence, duplicate detection
- [x] **Phase 5: Snapshots** — Log compaction, state serialization

**Not implemented:**
- InstallSnapshot RPC (sending snapshots to far-behind followers)
- Cluster membership changes
- Linearizable reads

---

## Resources

- [Raft Paper](https://raft.github.io/raft.pdf) — Read this multiple times
- [Raft Visualization](https://thesecretlivesofdata.com/raft/) — Helped a lot early on
- [Students' Guide to Raft](https://thesquareplanet.com/blog/students-guide-to-raft/) — Covers common implementation mistakes
