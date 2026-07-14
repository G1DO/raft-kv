# Incidents — Phase F chaos postmortems

Lab incident write-ups for the four Chaos Mesh fault classes exercised in
Phase F (#23–#26). These are **learning-project postmortems** against a
disposable kind cluster, not production SEV tickets.

| Document | Fault | Inject |
|---|---|---|
| [pod-kill.md](pod-kill.md) | Leader process death | `scripts/chaos-inject-pod-kill.sh` |
| [network-partition.md](network-partition.md) | Leader↔followers cut (M8 gate) | `scripts/chaos-inject-network-partition.sh` |
| [packet-loss.md](packet-loss.md) | Bounded loss on leader↔followers | `scripts/chaos-inject-packet-loss.sh` |
| [clock-skew.md](clock-skew.md) | TimeChaos on one follower | `scripts/chaos-inject-clock-skew.sh` |

**Operator how-to:** [runbooks/chaos.md](../runbooks/chaos.md) · **Lab contract:** [ADR-013](../decisions/ADR-013-chaos-lab-environment.md)

## Shared template

Every postmortem uses the same sections (in order):

1. Summary / severity  
2. Environment and versions  
3. Hypothesis  
4. Blast radius and commands/manifests  
5. UTC timeline  
6. Measured MTTR table  
7. Customer / data impact  
8. Detection  
9. Root cause / contributing factors  
10. Recovery / cleanup proof  
11. Follow-up actions (with status — planned ≠ done)

## Caveat on numbers

Phase F **#28** published **n=5** clean trials per class (kind `raft-kv`, Calico,
Chaos Mesh 2.8.3, 2026-07-14). Raw TSVs live under gitignored `backups/phase-f-28-*`
(see each postmortem’s Measured MTTR table).

**How to read MTTR:** harness `mttr_write_s` / `mttr_ready_s` are wall-clock from
inject start → first post-fault committed write / 3/3 Ready. For
network-partition and clock-skew that includes the held fault window (inject
returns after heal). Packet-loss deletes `NetworkChaos` after probe proofs (may
skip remaining duration). Integrity is a sample of pre-inject acks, not a full
WAL scan. Single-node kind + Chaos Mesh netem can briefly livelock elections
after loss clear; the inject may bounce pods (PVCs intact) before failing a trial.
