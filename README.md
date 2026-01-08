# raft-kv

A distributed key-value store built on the Raft consensus algorithm. From scratch, in Go.

## What is this?

Multiple servers that agree on data, even when some of them crash or lose network connectivity. You write to one node, the cluster makes sure everyone agrees, your data survives failures.

This is how real systems like etcd, CockroachDB, and TiKV work under the hood.

## Why build this?

**The skill:** Understanding distributed consensus separates senior engineers from juniors. It's not something you can fake in an interview.

**The proof:** A working implementation shows you can handle complexity — networking, concurrency, failure modes, correctness under chaos.

## The journey

This project is built in phases. Each one adds a layer of understanding.

```
Phase 1: Foundation     → "I can store data reliably on one machine"
Phase 2: Elections      → "I understand how nodes pick a leader"
Phase 3: Replication    → "I can keep multiple nodes in sync"
Phase 4: Safety         → "I can handle failures without losing data"
Phase 5: Production     → "I can make this actually usable"
```

---

## Phase 1: Single-Node Foundation

This is where we start. No consensus yet — just the building blocks.

### What we're building

1. **Persistent log** — entries survive restarts
2. **State machine** — applies commands, produces results
3. **Client protocol** — talk to the server (PUT/GET/DELETE)

### What you'll understand after

- How logs provide durability and ordering
- Why state machines must be deterministic
- The boundary between "storage" and "application logic"

### The pieces

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │ PUT/GET/DELETE
       ▼
┌─────────────┐
│   Server    │
│             │
│ ┌─────────┐ │
│ │ State   │ │  ← in-memory KV map
│ │ Machine │ │
│ └────┬────┘ │
│      │      │
│ ┌────▼────┐ │
│ │   Log   │ │  ← persisted to disk
│ └─────────┘ │
└─────────────┘
```

---

## Phase 2: Leader Election

Nodes vote. One becomes leader. If leader dies, they vote again.

### What you'll understand after

- How terms (logical clocks) prevent split-brain
- Why randomized timeouts matter
- What makes a vote "safe"

---

## Phase 3: Log Replication

Leader receives writes, sends them to followers, waits for majority, commits.

### What you'll understand after

- The log matching invariant
- How conflicts get resolved
- Why "committed" and "applied" are different things

---

## Phase 4: Safety & Edge Cases

Network partitions, duplicate requests, stale reads. The hard stuff.

### What you'll understand after

- Why Raft is correct (not just "seems to work")
- How to reason about distributed failure modes
- What linearizability actually means

---

## Phase 5: Production Hardening

Snapshots (log compaction), membership changes, observability.

### What you'll understand after

- How to manage unbounded growth
- How to change cluster membership safely
- What to monitor in a consensus system

---

## Running

```bash
# (coming soon)
```

## Testing

```bash
# (coming soon)
```

## Progress

- [ ] Phase 1: Foundation
- [ ] Phase 2: Elections
- [ ] Phase 3: Replication
- [ ] Phase 4: Safety
- [ ] Phase 5: Hardening

---

## Resources

- [Raft Paper](https://raft.github.io/raft.pdf) — the source of truth
- [Raft Visualization](https://thesecretlivesofdata.com/raft/) — watch it work
- [Students' Guide to Raft](https://thesquareplanet.com/blog/students-guide-to-raft/) — common pitfalls
