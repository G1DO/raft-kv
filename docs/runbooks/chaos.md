# Runbook — Chaos Mesh lab (Phase F)

Operator guide for the disposable kind lab used by Phase F experiments
([ADR-013](../decisions/ADR-013-chaos-lab-environment.md)).

## Prerequisites

1. kind cluster with **Calico** (or Cilium) enforcing NetworkPolicy — see
   [networkpolicy.md](networkpolicy.md). Stock kindnet is insufficient for the
   M8 partition gate (#24).
2. raft-kv release with `networkPolicy.enabled=true` (chart default).
3. helm + kubectl on the laptop (Chaos CRs are applied from outside the
   workload — raft-kv has no chaos RBAC).

## Install Chaos Mesh 2.8.3

```bash
./scripts/chaos-mesh-up.sh
```

Pins chart/app **2.8.3** into namespace `chaos-mesh`, sets containerd socket
paths for kind, raises inotify limits on kind nodes, and uses a single
controller replica (avoids `too many open files` on small clusters).

Verify:

```bash
kubectl -n chaos-mesh get pods
kubectl get crd networkchaos.chaos-mesh.org podchaos.chaos-mesh.org
```

## Experiments (via harness)

```bash
# #23 leader pod-kill
./scripts/chaos-harness.sh --trials 5 \
  --inject './scripts/chaos-inject-pod-kill.sh'

# #24 network partition (M8 gate) — requires Chaos Mesh + Calico + default-deny
PARTITION_DURATION=30s ./scripts/chaos-harness.sh --trials 5 \
  --inject './scripts/chaos-inject-network-partition.sh'

# #25 packet loss (bounded; quorum stays healthy)
PACKET_LOSS_PERCENT=25 PACKET_LOSS_DURATION=30s ./scripts/chaos-harness.sh --trials 5 \
  --inject './scripts/chaos-inject-packet-loss.sh'

# #26 clock skew (non-leader first; always rolled back)
CLOCK_SKEW_OFFSET=+5m CLOCK_SKEW_DURATION=30s ./scripts/chaos-harness.sh --trials 3 \
  --inject './scripts/chaos-inject-clock-skew.sh'
```

Inject scripts label Chaos CRs with `raft-kv-chaos=true`. The harness deletes
leftovers and requires 3/3 Ready between trials (ADR-013 cleanup).

### #24 proofs (per trial)

While the leader is partitioned from both followers:

- PUT to the isolated old leader → `NOT_LEADER` or commit timeout (not `OK`)
- PUT via the majority → `OK`
- Default-deny NetworkPolicies remain present before and after

### #25 proofs (per trial)

While bounded loss is applied on **leader ↔ followers** (not a full cut):

- At least one (usually most) client PUTs succeed — quorum stays healthy
- Sample commit latency (p50/p95) is logged
- Leadership change is reported explicitly (`changed=0|1`)
- After CR deletion, a recovery PUT succeeds

### #26 proofs (per trial)

Bounded `TimeChaos` on **one non-leader** (`CLOCK_SKEW_OFFSET`, default `+5m`):

- Cluster stays available (at least one successful PUT under skew)
- Leadership change yes/no is logged (often `changed=0` — ReadIndex, not leases)
- TimeChaos is deleted before return — **no leftover skew**
- Recovery PUT succeeds after rollback

### Verified on kind (2026-07-14)

Cluster: kind `raft-kv`, Calico, Chaos Mesh **2.8.3** in `chaos-mesh`,
raft-kv in `default` with default-deny NetworkPolicy.

```
# Full Phase F #28 batch (≥5 clean trials per class → backups/phase-f-28-*/)
./scripts/chaos-phase-f-28.sh --trials 5
```

Published n=5 (p50 / p95 / max), `mttr_write_s`:

| Class | write | ready | integrity | leader_changed |
|---|---|---|---|---|
| pod-kill | 3.630 / 3.874 / 3.882 | 9.455 / 12.549 / 12.855 | 5/5 | 5/5 |
| network-partition | 22.685 / 23.157 / 23.259 | 23.044 / 23.460 / 23.549 | 5/5 | 5/5 |
| packet-loss (25%) | 11.425 / 14.991 / 15.307 | 11.831 / 15.275 / 15.589 | 5/5 | 4/5 |
| clock-skew (+5m follower) | 21.763 / 22.724 / 22.789 | 22.133 / 23.077 / 23.142 | 5/5 | 0/5 |

Caveats: partition/skew MTTR includes the held fault window; packet-loss deletes
Chaos after probes; single-node kind lab only. Details:
[docs/incidents/](../incidents/README.md).

## Cleanup

```bash
kubectl delete networkchaos,podchaos,timechaos -A -l raft-kv-chaos=true --ignore-not-found
kubectl -n default get pods -l app=raft-kv   # expect 3/3 Ready
```

Failing cleanup is a failed trial — do not start the next inject until Ready.

## Postmortems (Phase F #27)

Incident write-ups for each fault class (same template):

- [pod-kill](../incidents/pod-kill.md)
- [network-partition](../incidents/network-partition.md) (M8 gate)
- [packet-loss](../incidents/packet-loss.md)
- [clock-skew](../incidents/clock-skew.md)

Index: [docs/incidents/README.md](../incidents/README.md). Multi-trial percentiles
published in Phase F **#28** (tables above + each postmortem).

