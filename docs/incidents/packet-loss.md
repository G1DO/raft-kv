# Incident — Packet loss

**Date:** 2026-07-14 (lab) · **Severity:** Lab / Sev-3 analogue (degraded peer
path; quorum remained write-capable) · **Phase F #25**

## Summary

Bounded Chaos Mesh `NetworkChaos` **loss** (not 100%) was applied on traffic
between the current leader and both followers. Client PUTs continued to succeed
(quorum healthy). Commit latency was sampled; whether leadership changed was
logged explicitly. After CR deletion, a recovery write succeeded and harness
integrity passed.

## Environment and versions

| Item | Value |
|---|---|
| Cluster | kind `raft-kv`, Calico, Chaos Mesh **2.8.3** |
| Harness | `./scripts/chaos-harness.sh` + `./scripts/chaos-inject-packet-loss.sh` |
| Loss | `PACKET_LOSS_PERCENT=25` (lab sample) |
| Duration | `PACKET_LOSS_DURATION=20s` (lab sample) |
| Scope | **Leader ↔ followers only** (not full-mesh loss) |

## Hypothesis

Partial loss on leader–follower links increases heartbeat / AppendEntries
retries and commit latency but does not remove quorum. Unlike partition (#24),
the cluster should keep accepting some (ideally most) client writes. Leadership
may or may not change — must be reported, not assumed.

## Blast radius and commands/manifests

- **In blast:** packets between leader and each follower (bidirectional loss %).
- **Out of blast:** follower↔follower under this inject shape; full partition
  behaviour (use #24 for that).

```bash
PACKET_LOSS_PERCENT=25 PACKET_LOSS_DURATION=20s \
  ./scripts/chaos-harness.sh --trials 1 \
  --inject './scripts/chaos-inject-packet-loss.sh'
```

Rejects `PACKET_LOSS_PERCENT >= 100` — that would be a partition claim.

## UTC timeline

Sample harness trial (kind, 2026-07-14):

| Event | Note |
|---|---|
| Inject | `NetworkChaos` action `loss` on leader↔followers |
| AllInjected | + short settle |
| Probes | 12/12 PUT `OK`; p50≈52 ms, p95≈63 ms, max≈66 ms |
| Leadership | changed (sample: `raft-kv-1` → `raft-kv-2`) |
| Heal | CR deleted; recovery PUT `OK` |
| Harness | integrity pass; 3/3 Ready |

## Measured MTTR table

Phase F **#28** — five clean trials (`PACKET_LOSS_PERCENT=25`). Inject deletes
`NetworkChaos` after probe proofs (may skip remaining duration) so MTTR is
inject wall-clock through harness recovery, not necessarily a full 15–30s hold.
If elections livelock after netem clear on single-node kind, the inject may bounce
pods (PVCs intact) once before failing.

| Metric | n=5 |
|---|---|
| Probe success (per trial) | ≥1 OK required (lab runs were typically 12/12) |
| `mttr_write_s` p50 / p95 / max | **11.425** / **14.991** / 15.307 s |
| `mttr_ready_s` p50 / p95 / max | **11.831** / **15.275** / 15.589 s |
| `leader_changed` | 4/5 |
| Integrity | 5/5 |

Raw: `backups/phase-f-28-*/packet-loss/chaos-harness-*.tsv` (gitignored).

Note: early all-mesh loss attempts thrash elections; scoped leader↔followers
is the supported shape.

## Customer / data impact

- **Availability:** writes succeeded throughout the sample window.
- **Latency:** modest commit-path latency observed under 25% loss.
- **Durability:** pre-fault acks intact after heal.

## Detection

- Inject probe log lines (`OK`/`FAIL` + ms).
- Explicit `leadership: A -> B (changed=0|1)`.
- Optional raft-kv `rpc_failed` warnings under loss.

## Root cause / contributing factors

- **Direct:** intentional packet loss inject.
- **Design:** Raft retries tolerate loss below partition severity.
- **Operator:** loss percent and link scope control severity — full-mesh high
  loss can look like continuous elections (avoided in the shipped inject).

## Recovery / cleanup proof

1. Inject deletes the labeled `NetworkChaos` CR.  
2. Post-loss recovery PUT must succeed before inject exits.  
3. Harness: no leftovers, **3/3 Ready**, integrity pass.

## Follow-up actions

| Action | Status |
|---|---|
| Packet-loss inject with quorum check (#25) | **Done** |
| Scope loss to leader↔followers (not all-mesh) | **Done** |
| ≥5 trials + percentile publish | **Done** (#28) |
| Correlate probe latency with Prometheus commit metrics | **Planned** (optional polish) |
