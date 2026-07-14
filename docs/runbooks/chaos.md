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
```

Inject scripts label Chaos CRs with `raft-kv-chaos=true`. The harness deletes
leftovers and requires 3/3 Ready between trials (ADR-013 cleanup).

### #24 proofs (per trial)

While the leader is partitioned from both followers:

- PUT to the isolated old leader → `NOT_LEADER` or commit timeout (not `OK`)
- PUT via the majority → `OK`
- Default-deny NetworkPolicies remain present before and after

### Verified on kind (2026-07-14)

Cluster: kind `raft-kv`, Calico, Chaos Mesh **2.8.3** in `chaos-mesh`,
raft-kv in `default` with default-deny NetworkPolicy.

```
./scripts/chaos-harness.sh --trials 1 \
  --inject './scripts/chaos-inject-network-partition.sh'
# old leader: ERROR: timeout waiting for commit
# majority: OK; leader raft-kv-0→raft-kv-1; integrity=1; NP still present
```

## Cleanup

```bash
kubectl delete networkchaos,podchaos,timechaos -A -l raft-kv-chaos=true --ignore-not-found
kubectl -n default get pods -l app=raft-kv   # expect 3/3 Ready
```

Failing cleanup is a failed trial — do not start the next inject until Ready.
