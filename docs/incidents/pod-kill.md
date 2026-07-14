# Incident — Leader pod kill

**Date:** 2026-07-14 (lab) · **Severity:** Lab / Sev-3 analogue (transient write
unavailability during election; quorum of remaining voters intact) · **Phase F #23**

## Summary

The current Raft leader pod was deleted once (Chaos Mesh `PodChaos` when CRDs
exist, otherwise `kubectl delete pod`). Followers elected a new leader; the
StatefulSet recreated the member on the same PVC. Acknowledged pre-fault keys
survived integrity readback. Cleanup restored **3/3 Ready** before the next
trial.

## Environment and versions

| Item | Value |
|---|---|
| Cluster | kind `raft-kv` (single control-plane node) |
| CNI | Calico (NetworkPolicy enforcing) |
| Chaos Mesh | **2.8.3** in namespace `chaos-mesh` (`./scripts/chaos-mesh-up.sh`) |
| Workload | Helm release `raft-kv` in `default`, image `raft-kv:dev` |
| Peer TLS | Enabled (mTLS) |
| NetworkPolicy | Chart default-deny + allow rules (ADR-011) |
| Harness | `./scripts/chaos-harness.sh` + `./scripts/chaos-inject-pod-kill.sh` |

## Hypothesis

Killing the leader process forces a new election among the remaining voters.
Committed entries on disk (PVC) remain; the recreated ordinal rejoins via
normal Raft catch-up. Client writes resume once a majority elects.

## Blast radius and commands/manifests

- **In blast:** current leader pod only; peer RPC and election among survivors.
- **Out of blast:** follower PVCs; observability stack; Chaos Mesh control plane;
  NetworkPolicy objects (unchanged).

```bash
./scripts/chaos-harness.sh --trials 1 \
  --inject './scripts/chaos-inject-pod-kill.sh'
```

Inject finds the leader via `raft_is_leader` (or PUT probe), then either applies
a labeled `PodChaos` (`raft-kv-chaos=true`) or `kubectl delete pod <leader>
--wait=false`, and waits until the pod UID changes (STS replace).

## UTC timeline

Sample harness trial (kind, 2026-07-14; approximate wall times from local lab):

| UTC (approx) | Event |
|---|---|
| T0 | Baseline unique writes; record leader |
| T0+ε | Inject: delete/kill leader pod |
| T0+~1–3s | Pod UID replaced (new instance from PVC) |
| T0+~4s | First post-fault committed PUT (`mttr_write`) |
| T0+~5s | StatefulSet **3/3 Ready** (`mttr_ready`) |
| End | Integrity sample of pre-inject acks; Chaos leftovers cleared |

## Measured MTTR table

Phase F **#28** — five clean trials (kind, 2026-07-14). Earlier ~124s outlier was
a stale port-forward artifact (discarded); harness now refreshes client PFs and
restarts the load writer after inject.

| Metric | n=5 |
|---|---|
| `mttr_write_s` p50 / p95 / max | **3.630** / **3.874** / 3.882 s |
| `mttr_ready_s` p50 / p95 / max | **9.455** / **12.549** / 12.855 s |
| `leader_changed` | 5/5 |
| Integrity | 5/5 |

Raw: `backups/phase-f-28-*/pod-kill/chaos-harness-*.tsv` (gitignored).

## Customer / data impact

- **Availability:** brief write downtime until a new leader commits.
- **Durability:** pre-fault acknowledged keys present on integrity GET sample.
- **No data loss** observed for keys acked before inject.

## Detection

- Pod `NotReady` / delete events on the leader ordinal.
- Client `NOT_LEADER` / retries until a survivor accepts writes.
- Harness events: `inject_start`, `first_write`, `trial_done`.

## Root cause / contributing factors

- **Direct:** intentional leader pod kill (chaos experiment).
- **Contributing:** single-leader Raft — any leader death requires election.
- **Not a product bug** for this lab scenario.

## Recovery / cleanup proof

1. StatefulSet recreates the ordinal; member rejoins from PVC.  
2. Harness `assert_cleanup`: no `raft-kv-chaos=true` CRs; **3/3 Ready**; load stopped.  
3. Integrity check on pre-inject acked keys passed.

## Follow-up actions

| Action | Status |
|---|---|
| Reusable harness + pod-kill inject (#22–#23) | **Done** |
| Refresh client port-forwards immediately after inject | **Done** (harness) |
| Run ≥5 clean trials and publish percentiles | **Done** (#28) |
| Prefer PodChaos path once Chaos Mesh is always installed | **Done** (inject prefers CRD; kubectl fallback remains) |
