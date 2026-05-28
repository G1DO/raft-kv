# ADR-001 — Single-server membership changes vs. joint consensus

**Status:** Accepted

## Context

Raft cluster membership must be changeable at runtime (add a node, remove a
dead one, replace one) without downtime. The Raft paper presents two
mechanisms:

1. **Joint consensus** (§6 of the paper): the cluster moves through an
   intermediate configuration `C_old,new` that requires a majority in *both*
   the old and new configurations to commit anything. Once `C_old,new`
   commits, the cluster transitions to `C_new`.
2. **Single-server changes** (Ongaro's thesis §4.3): only one server is added
   or removed at a time. Because any single-server change shifts the majority
   threshold by at most one, the old and new majorities are *guaranteed* to
   overlap by at least one server, which is enough for safety.

## Decision

Use **single-server changes**. Each `AddServer`/`RemoveServer` call appends
one config-change log entry and applies the new membership immediately on
the leader (and on each peer when the entry is replicated to it), before
the entry is committed.

Implemented at [internal/raft/raft.go:964-1056](../../internal/raft/raft.go#L964-L1056).
`AppendCommand`, `AddServer`, and `RemoveServer` share the named return
`(index, term, isLeader)` — `isLeader=false` tells the client to retry
elsewhere.

## Rejected alternative

**Joint consensus.** Strictly more general — it can rotate multiple servers
at once and supports configuration changes that don't pass through a shared
majority. The cost is significant: two log entries per change, an
intermediate state with quorum rules from both configurations, and more
code paths to test (committing in `C_old,new`, transitioning to `C_new`,
handling a leader crash inside the joint state).

For a learning project with no real "rotate 3 servers at once" requirement,
this is complexity for a use case that does not exist.

## Consequences

**What we commit to:**
- Cluster changes are serialised — operators must wait for one change to
  finish before starting the next.
- Replacing a server is two steps (add the new one, then remove the old),
  not one atomic operation.
- The config change takes effect on each node when it is *logged*, not when
  it is committed. This is correct for Raft and was a subtle bug source
  during implementation (see [README "What I learned" #6](../../README.md#what-i-learned-building-this)).

**What becomes harder:**
- Operations that *would* be expressible as a single joint-consensus step
  (e.g. swap two servers atomically) are not available.

## Notes for reviewers

The safety argument is: a single-server change moves the majority threshold
by at most ±1, so the old majority and the new majority always share at
least one server. That shared server prevents two disjoint majorities from
forming during the transition. With multi-server changes this guarantee
does not hold — adding two servers to a 3-node cluster can yield a 5-node
cluster where 3 new servers form a majority while 2 old servers still
believe they have one. That is the split-brain joint consensus exists to
prevent and that single-server changes sidestep entirely.
