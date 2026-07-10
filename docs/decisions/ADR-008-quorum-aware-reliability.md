# ADR-008 — Quorum-aware reliability (probes, PDB, resources)

**Status:** Accepted · **Date:** 2026-07-10

## Context

M7 puts this Raft cluster on Kubernetes. The kubelet and eviction APIs do not
know about majority: a well-intentioned probe, drain, or resource limit can take
out enough voters to lose quorum. The chart and `Raft.Ready()` therefore need
explicit mappings from Kubernetes knobs to Raft state — and honest limits on
what those knobs can guarantee.

Three coupled questions:

1. **Probes** — what should `/healthz` and `/readyz` mean, and how do they
   interact with elections and WAL replay?
2. **PDB** — how do we bound voluntary disruption without lying about OOM /
   node death / rolling updates?
3. **Resources** — how do we size a voter so CFS throttling and OOMKill do not
   become consensus failures?

Backup/restore (quiesced follower, wipe vs disaster) is operational procedure,
not a second ADR — see [docs/runbooks/restore.md](../runbooks/restore.md).

## Decision

### Probes

| Probe | Endpoint | Meaning |
|-------|----------|---------|
| startup | HTTP `/healthz` | Process finished `NewRaft` (WAL replay is **synchronous** before the metrics server listens). Budget = `failureThreshold × periodSeconds` (default 30×2s = 60s). Raise the threshold for a fat WAL; do not fake it with a long liveness `initialDelaySeconds`. |
| liveness | HTTP `/healthz` | Dumb "HTTP loop answers." Unconditional 200. |
| readiness | HTTP `/readyz` | `Raft.Ready()` — see below. Hysteresis: `periodSeconds: 2`, `failureThreshold: 3` so election blips (~150–300ms when `leaderID` clears) do not flap endpoints. |

Handlers live in
[internal/server/raft_server.go](../../internal/server/raft_server.go)
(`/healthz` ~381, `/readyz` ~385). Helm knobs:
[deploy/helm/raft-kv/values.yaml](../../deploy/helm/raft-kv/values.yaml).

**`Ready()` (D1 option 2 — harden, do not ReadIndex-per-probe):**

```
listener up
∧ ¬needsCatchUp
∧ lastApplied ≥ commitIndex
∧ (no peers ∨ Leader ∨ leaderID ≠ "")
```

Implemented at
[internal/raft/raft.go](../../internal/raft/raft.go) (`Ready` ~1400).
`needsCatchUp` is set on WAL-only restart when commit/applied boot at 0 while
the log is non-empty — otherwise `0 ≥ 0` would report Ready with an empty state
machine. Cleared when catch-up applies (or a snapshot installs).
`becomeLeader` appends a current-term no-op and does **not** clear
`needsCatchUp` eagerly; Ready waits until that entry commits and applies.

**Full-cluster restart caveat:** every pod can be NotReady until a leader is
known and catch-up finishes. The client Service drops NotReady endpoints;
`publishNotReadyAddresses` on the headless Service only preserves peer DNS.
Clients regain access once quorum forms — verified on kind in M7 Verify A.

**SIGTERM:** `Raft.Stop()` closes the WAL (`Sync`+`Close`) so polite shutdown
does not leave a torn last record. Crash-stop torn tails remain possible;
recovery is still ADR-004 (panic) + wipe-rejoin / restore runbook.

### PDB

- Default `pdb.maxUnavailable: 1`, enabled.
- Prefer **`maxUnavailable` over `minAvailable`**: `minAvailable: 2` at
  `replicaCount: 5` silently allows three simultaneous evictions.
- Helm **fails install** if `maxUnavailable ≥ ceil(N/2)`
  ([templates/pdb.yaml](../../deploy/helm/raft-kv/templates/pdb.yaml)).

**Honesty:** a PDB only bounds **voluntary** disruptions (Eviction API /
`kubectl drain`). It does **not** cover OOMKill, node crash, liveness restart,
`kubectl delete pod`, or StatefulSet rolling updates. Rolling updates are
serialized by the Ready gate (`/readyz`), not the PDB.

### Resources

- **CPU:** request only (`100m` default), **no limit** — CFS throttling on
  election/heartbeat paths looks like a slow peer and can amplify storms.
- **Memory:** request == limit (`128Mi`) → Guaranteed on the OOM dimension.
  OOM bypasses the PDB; a second OOM during a roll is quorum loss.
- **`GOMEMLIMIT=115MiB` (~90% of limit):** soft-caps the Go heap so snapshot
  marshal prefers GC over growing into a kubelet OOMKill.

### No HPA

More voters ≠ more write throughput (every write still needs a majority).
Autoscaler scale-down can destroy quorum. Membership is deliberate
single-server Raft config change, not a Deployment replica bump. Read
scale-out would need non-voting learners (not implemented).

## Rejected alternatives

**ReadIndex (or any leadership confirmation) on every readiness tick.**
Strongest freshness story, wrong cost: each probe would fan out RPCs under the
kubelet's schedule, couple readiness latency to peer health, and risk probe
timeouts that remove healthy followers from the Service during load. Linearizable
reads already exist for **clients** via `ReadIndex`; probes only need "safe to
send traffic here."

**Liveness = `Ready()` or take `r.mu`.** Restarting a healthy follower because
catch-up or a slow fsync made Ready false is a quorum risk. Liveness must stay
dumb; readiness absorbs Raft state.

**Keep vacuous Ready after restart (D1 option 1).** Cheap, but a restarted
follower with empty KV and `0≥0` would enter endpoints before re-applying —
clients could see `NOT_FOUND` for keys that exist on the leader. Hardening
`needsCatchUp` is the minimum correct fix.

**`minAvailable: 2` as the PDB primary knob.** Wrong at N>3; see above.

**CPU limit "for fairness."** Latency-sensitive consensus paths lose to CFS
throttling; request-only is the deliberate trade.

**Live `tar` of a writing WAL.** Can capture a torn last record → ADR-004 panic
on restart. Backup is quiesced follower only (delete pod / helper on PVC); see
the restore runbook.

## Consequences

- Probe, PDB, and resource policy are part of the public reliability contract
  (README + design.md Reliability; this ADR for the rejected alternatives).
- Operators must not equate "PDB present" with "quorum cannot be lost."
- Fat WALs need a larger `startupProbe.failureThreshold`, not a longer liveness
  delay — liveness is suspended until startup succeeds.
- Restore decision rule stays outside this ADR: quorum alive → wipe-rejoin;
  full loss → restore/full from backup ([runbook](../runbooks/restore.md)).
