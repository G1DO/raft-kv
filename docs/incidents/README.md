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

Tables below cite **sample kind lab runs** (often `n=1`) recorded while building
Phase F. They prove the inject + harness path works; they are **not** published
percentiles. Multi-trial statistics (≥5 clean trials per class) are Phase F
**#28**.
