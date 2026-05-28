# ADR-002 — ReadIndex vs. lease-based reads

**Status:** Accepted

## Context

A naive leader serving reads straight from its state machine is *not*
linearizable: a stale leader that has been partitioned and superseded by
a new leader will happily keep answering reads, returning data that the
new leader has already overwritten. The Raft paper offers two mechanisms
to make leader reads safe:

1. **ReadIndex** (§6.4 of the paper): before answering, the leader records
   its current `commitIndex`, sends a heartbeat round to all peers, and
   only returns the value once a majority responds. This proves it is
   still the leader at that point in time.
2. **Leader leases** (§6.4.1): the leader assumes it remains the leader for
   a bounded period after its last successful heartbeat round, and serves
   reads from memory during that window without sending fresh heartbeats.

## Decision

Use **ReadIndex**. Implemented at
[internal/raft/raft.go:1175-1214](../../internal/raft/raft.go#L1175-L1214):

1. Capture `commitIndex` and `currentTerm` under the lock.
2. Call `confirmLeadership()`, which broadcasts a heartbeat and waits for a
   majority to respond.
3. After the heartbeat round, re-check that we are still leader and that
   `currentTerm` hasn't moved (a stale leader would have stepped down).
4. Wait for the state machine to apply up to the recorded `commitIndex`.
5. Return.

Wired into the runnable binary through
[internal/server/raft_server.go:184](../../internal/server/raft_server.go#L184)
so client `GET`s actually take this path.

## Rejected alternative

**Leader leases.** Faster — reads cost an in-memory lookup instead of a
network round-trip. The cost is correctness sensitivity to clocks: lease
safety depends on the assumption that the leader's clock and the followers'
clocks do not drift further than some bound during the lease window. In a
container with frequent VM live-migration, NTP step adjustments, or a
suspended-and-resumed laptop, that assumption can silently break. The
resulting bug — a "dead" leader confidently answering reads with stale
data — is the exact bug we are trying to prevent.

We rejected leases because:

- Clock-sync guarantees on a learning-grade local cluster are weak and not
  worth modelling.
- The throughput cost of an extra RPC per read is not a constraint we are
  trying to optimise (no real workload, no benchmarks yet).
- ReadIndex is conceptually simpler to defend in writing — "we asked the
  cluster and a majority confirmed we're still leader" requires no
  assumptions beyond the existing Raft model.

## Consequences

**What we commit to:**
- Every read incurs one heartbeat round-trip to a majority of peers. Read
  latency in the steady state is dominated by the slowest of the (N-1)/2+1
  peer RTTs.
- A leader partitioned away from a majority **cannot** serve reads — it
  returns `fmt.Errorf("failed to confirm leadership")`. This is the
  desired behaviour and is verified by a partition test in the
  measurement harness.
- No reliance on synchronised clocks. The protocol works correctly under
  arbitrary clock skew.

**What becomes harder:**
- Read throughput cannot exceed leader heartbeat throughput. If the cluster
  is heartbeat-limited, reads back up first.
- Adding lease-based "fast reads" later would mean carrying both code paths
  with a runtime switch — non-trivial.

## Notes for reviewers

The subtlety this design catches is: a partitioned leader that *thinks*
it is still leader (because its heartbeats are timing out but it doesn't
know why) will fail `confirmLeadership` and return an error — not stale
data. Without ReadIndex, that scenario silently returns last-known values
until the leader finally steps down. The window can be hundreds of ms or
more depending on election timeout configuration, and during that window
a competing client through the new leader could already have written a
different value.
