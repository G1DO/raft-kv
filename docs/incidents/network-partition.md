# Incident — Network partition (M8 gate)

**Date:** 2026-07-14 (lab) · **Severity:** Lab / Sev-2 analogue (split-brain
*pressure*: isolated old leader cannot commit; majority continues) · **Phase F #24**

## Summary

A time-bounded bidirectional Chaos Mesh `NetworkChaos` **partition** isolated
the current Raft leader from both followers **while default-deny NetworkPolicy
remained applied**. The old leader could not commit new client writes; the
majority elected and committed. After the Chaos CR was deleted, the cluster
healed, integrity held, and **3/3 Ready** returned.

This is the Phase F **M8 gate**: Chaos Mesh fault + ADR-011 NetworkPolicy
coexistence on a Calico kind lab ([ADR-013](../decisions/ADR-013-chaos-lab-environment.md)).

## Environment and versions

| Item | Value |
|---|---|
| Cluster | kind `raft-kv` |
| CNI | Calico (required — kindnet insufficient for this claim) |
| Chaos Mesh | **2.8.3** in `chaos-mesh` |
| NetworkPolicy | `raft-kv-default-deny-ingress/egress` + allow rules present before **and** after |
| Harness | `./scripts/chaos-harness.sh` + `./scripts/chaos-inject-network-partition.sh` |
| Duration | `PARTITION_DURATION` (lab sample used `25s`) |

## Hypothesis

Cutting peer RPC between the leader and both followers leaves a majority of two
that can elect and commit. The isolated old leader cannot achieve majority
replication, so client PUTs return `NOT_LEADER` or commit timeout — not a
durable dual-commit. Healing reconnects peers onto one history.

## Blast radius and commands/manifests

- **In blast:** IP connectivity between leader pod and the two followers (peer
  path). Client port-forward from the laptop may still reach pods.
- **Out of blast:** NetworkPolicy objects (must stay); follower↔follower should
  remain open; observability / Chaos Mesh namespaces.

```bash
./scripts/chaos-mesh-up.sh   # once per lab
PARTITION_DURATION=25s ./scripts/chaos-harness.sh --trials 1 \
  --inject './scripts/chaos-inject-network-partition.sh'
```

Inject applies a labeled `NetworkChaos` (`action: partition`, `direction: both`)
with `selector` = leader, `target` = both followers, waits `AllInjected=True`,
then asserts during the window.

## UTC timeline

Sample harness trial (kind, 2026-07-14):

| Event | Note |
|---|---|
| Preflight | Default-deny ingress+egress present |
| Inject start | `NetworkChaos` applied |
| AllInjected | Chaos Mesh reports injected |
| +~8s | Majority election window |
| During split | PUT to old leader → `ERROR: timeout waiting for commit` |
| During split | PUT via survivor → `OK` (majority) |
| Duration end | CR deleted to heal |
| Post | First harness post-fault write; 3/3 Ready; NP still present |

## Measured MTTR table

Harness clocks inject start → first write / Ready **including** the held
partition duration (`PARTITION_DURATION=20s` in the #28 batch; inject returns
after heal), so numbers are “fault window + heal”, not pure election latency.

Phase F **#28** — five clean trials:

| Metric | n=5 |
|---|---|
| Old leader PUT (per trial) | not `OK` (`ERROR: timeout…` / `NOT_LEADER`) |
| Majority PUT (per trial) | `OK` |
| `mttr_write_s` p50 / p95 / max | **22.685** / **23.157** / 23.259 s |
| `mttr_ready_s` p50 / p95 / max | **23.044** / **23.460** / 23.549 s |
| `leader_changed` | 5/5 |
| Integrity | 5/5 |
| Default-deny after | present every trial |

Raw: `backups/phase-f-28-*/network-partition/chaos-harness-*.tsv` (gitignored).

## Customer / data impact

- **Writes on minority (old leader):** rejected / timed out — no false success.
- **Writes on majority:** succeeded during the split.
- **Durability:** pre-fault acked keys passed integrity after heal.
- **Policy:** NetworkPolicy was **not** disabled to make Chaos Mesh work.

## Detection

- Inject assertions + structured harness events.
- Client timeouts / `NOT_LEADER` against the isolated pod.
- Optional: peer RPC failure warnings in raft-kv logs on the cut links.

## Root cause / contributing factors

- **Direct:** intentional `NetworkChaos` partition.
- **Design:** Raft majority commit rule — correct behaviour under split.
- **Lab risk:** leftover iptables from aborted Chaos runs can thrash elections;
  cleanup must delete CRs and confirm Ready (ADR-013).

## Recovery / cleanup proof

1. Inject deletes the `NetworkChaos` CR after duration.  
2. Default-deny NetworkPolicies still present.  
3. Harness cleanup: no labeled Chaos CRs; **3/3 Ready**; integrity pass.

## Follow-up actions

| Action | Status |
|---|---|
| Chaos Mesh 2.8.3 install script + partition inject (#24) | **Done** |
| Prove NP coexistence on Calico kind | **Done** (this incident) |
| ≥5 clean partition trials + publish stats | **Done** (#28) |
| Document leftover-chaos recovery (delete CR + bounce if needed) | **Done** (runbook cleanup; lab note) |
