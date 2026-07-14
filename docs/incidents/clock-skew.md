# Incident — Clock skew

**Date:** 2026-07-14 (lab) · **Severity:** Lab / Sev-4 analogue (bounded skew on
a **non-leader**; availability held; no leftover skew) · **Phase F #26**

## Summary

Chaos Mesh `TimeChaos` applied a bounded wall-clock offset (`+5m`) to **one
follower** only. The cluster kept accepting writes; leadership did **not**
change in the sample run (consistent with ReadIndex rather than clock-bound
leases — [ADR-002](../decisions/ADR-002-readindex-vs-leases.md)). The TimeChaos
CR was deleted before the inject returned; no skew remained for the next trial.

## Environment and versions

| Item | Value |
|---|---|
| Cluster | kind `raft-kv`, Calico, Chaos Mesh **2.8.3** |
| Harness | `./scripts/chaos-harness.sh` + `./scripts/chaos-inject-clock-skew.sh` |
| Offset | `CLOCK_SKEW_OFFSET=+5m` |
| Duration | `CLOCK_SKEW_DURATION=20s` (lab sample) |
| Target policy | Non-leader only (v1); leader skew is escalation, not default |

## Hypothesis

Clock skew on a single follower may perturb local timeout perception but should
not produce split-brain commits in this stack because linearizable leadership
confirmation uses **ReadIndex** (heartbeat round), not leases that assume a
bounded clock skew. Expect: availability mostly preserved; `leader_changed`
often `0`; mandatory CR delete for rollback.

## Blast radius and commands/manifests

- **In blast:** PID 1 (and children) in the skewed pod’s `raft-kv` container
  clocks (`CLOCK_REALTIME` default in TimeChaos).
- **Out of blast:** other pods’ clocks; permanent skew (forbidden — duration +
  delete).

```bash
CLOCK_SKEW_OFFSET=+5m CLOCK_SKEW_DURATION=20s \
  ./scripts/chaos-harness.sh --trials 1 \
  --inject './scripts/chaos-inject-clock-skew.sh'
```

## UTC timeline

Sample harness trial (kind, 2026-07-14):

| Event | Note |
|---|---|
| Select | Leader `raft-kv-1`; target follower `raft-kv-0` |
| Inject | `TimeChaos` `timeOffset=+5m` |
| Probes | 8/8 PUT `OK` |
| Leadership | `raft-kv-1` → `raft-kv-1` (`changed=0`) |
| Rollback | Delete TimeChaos; assert no labeled leftovers |
| Recovery | PUT `OK`; harness integrity pass |

## Measured MTTR table

| Metric | Sample (n=1) |
|---|---|
| Target | follower (`raft-kv-0`) |
| Probe success | ok=8 fail=0 |
| `leader_changed` | **0** |
| `mttr_write_s` | ~26.8 s (includes held duration) |
| `mttr_ready_s` | ~27.2 s |
| TimeChaos after inject | **none** |
| Integrity | pass |

≥5 trials → **#28**. Leader-skew escalation not run in this write-up.

## Customer / data impact

- **Availability:** writes succeeded under follower skew in the sample.
- **Correctness:** no observed dual-leader commit; leadership stable.
- **Residual:** TimeChaos only affects injected container processes — do not
  confuse with host NTP changes.

## Detection

- Inject probe summary + `leadership: … (changed=0|1)`.
- `kubectl get timechaos -l raft-kv-chaos=true` empty after run.

## Root cause / contributing factors

- **Direct:** intentional TimeChaos offset.
- **Why limited impact:** ADR-002 ReadIndex design reduces dependence on
  synchronised clocks for lease safety (none claimed).
- **Risk if mishandled:** forgetting to delete TimeChaos leaves a permanent
  skew — inject and ADR-013 cleanup forbid that.

## Recovery / cleanup proof

1. Inject always `kubectl delete timechaos … --wait=true`.  
2. Asserts zero leftovers with label `raft-kv-chaos=true`.  
3. Post-skew recovery PUT required.  
4. Harness Ready + integrity pass.

## Follow-up actions

| Action | Status |
|---|---|
| Non-leader clock-skew inject with forced rollback (#26) | **Done** |
| Document ReadIndex vs lease expectation in postmortem | **Done** (this doc) |
| Optional: escalate to leader skew after repeatable follower case | **Planned** (not blocking) |
| ≥5 trials + publish | **Planned** (#28) |
