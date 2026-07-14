# ADR-013 — Chaos lab environment (kind + Calico + Chaos Mesh)

**Status:** Accepted · **Date:** 2026-07-12

## Context

Phase F (#22–#28) must run reproducible Chaos Mesh experiments (pod kill,
network partition, packet loss, clock skew) and write postmortems with
measured MTTR. The M8 gate (#24) requires a network partition **while
default-deny NetworkPolicy is active** ([ADR-011](ADR-011-networkpolicy-boundary.md)).

Today’s lab path is a disposable kind cluster named `raft-kv`
([deploy/kind-config.yaml](../../deploy/kind-config.yaml),
[scripts/k8s-up.sh](../../scripts/k8s-up.sh)). Stock kind uses **kindnet**,
which does not give a trustworthy NetworkPolicy story for that gate.
`scripts/chaos-demo.sh` kills a local process outside Kubernetes — useful
for demos, not Phase F.

Chaos Mesh installs privileged components (chaos-daemon). Those must not
live in the `raft-kv` workload namespace or share its ServiceAccount.

## Decision

### Cluster

| Item | Contract |
|------|----------|
| Runtime | Disposable **kind** cluster (same family as M4–M7 labs) |
| Lifecycle | Create/destroy via existing down/up scripts (extended in Phase F as needed); never a shared long-lived “prod” cluster |
| Workload ns | `raft-kv` (Argo destination / chart default) |
| CNI | **Calico** installed for NetworkPolicy enforcement (replace or disable reliance on kindnet for policy). Cilium is an acceptable alternate if documented with the same verification; default docs pin **Calico** |
| Honesty | Stock kindnet-only clusters are **insufficient** for claiming ADR-011 + #24 coexistence |

Exact Calico install steps land in a Phase F runbook/script; this ADR only
requires a NetworkPolicy-capable CNI before those experiments are called
passing.

### Chaos Mesh

| Item | Contract |
|------|----------|
| Product | [Chaos Mesh](https://chaos-mesh.org/) via official Helm chart |
| Pin | Chart/app version **2.8.3** (`helm install … --version 2.8.3`, images `v2.8.3`) — bump only with an intentional doc/script change |
| Namespace | `chaos-mesh` (create dedicated; not `raft-kv`, not `observability`) |
| Privileges | chaos-daemon / controllers stay in `chaos-mesh` (or chart-default system ns under that release); **no** Chaos Mesh pods scheduled into `raft-kv` |
| Targets | Experiments select only raft-kv workloads (`app: raft-kv` in `raft-kv`), time-bounded; do not chaos the observability stack or Chaos Mesh itself in M8 trials |
| Credentials | No long-lived Chaos Mesh secrets committed to this repo |

### Isolation from the app

- raft-kv keeps its least-privilege ServiceAccount (Phase C #13); it does not
  need RBAC to create chaos objects.
- Operators apply Chaos CRs from a break-glass kubeconfig (lab laptop), not
  from inside raft-kv pods.
- NetworkChaos must be written so it partitions **peer** traffic as intended
  without permanently disabling the default-deny policies; after the
  experiment duration, policies remain as ADR-011 specifies.

### Cleanup and “experiment ended” proof

Before the next trial or before declaring a run complete:

1. Delete or confirm finished all Chaos Mesh experiment CRs used in the run
   (`PodChaos`, `NetworkChaos`, `TimeChaos`, etc.) in the lab.
2. `kubectl get` (or equivalent) shows **no** active experiments targeting
   `raft-kv`.
3. StatefulSet is **3/3 Ready**; harness stopped.
4. Postmortem timeline records UTC end + the cleanup commands.

`scripts/chaos-harness.sh` (Phase F #22) encodes this checklist after every
trial and at run end; failing cleanup is a failed trial, not a footnote.
Inject scripts should label Chaos CRs with `raft-kv-chaos=true` so the
harness can delete leftovers. Lab install: `scripts/chaos-mesh-up.sh`
(Chaos Mesh **2.8.3**); partition gate: `scripts/chaos-inject-network-partition.sh`
([runbook](../runbooks/chaos.md)).

### Out of scope for this ADR

- Postmortem templates filled in [docs/incidents/](../incidents/) (#27);
  multi-trial publish (#28) remains.
- Production multi-tenant Chaos Mesh operations.
- Claiming clock-skew or partition results before the pinned versions above
  are actually installed and verified in the lab.

## Rejected alternatives

**Run Phase F only with `chaos-demo.sh` (local process kill).**
Does not exercise NetworkPolicy, Chaos Mesh, or Kubernetes failure domains.
Rejected as the M8 gate.

**Install Chaos Mesh into the `raft-kv` namespace.**
Mixes privileged daemons with the workload; rejected.

**Keep kindnet and “assume” NetworkPolicy works.**
Would fake the #24 coexistence claim. Rejected.

**Unpinned `helm install` latest.**
Non-reproducible postmortems. Rejected; pin **2.8.3** (or a later pin via
ADR/script update).

## Consequences

- Phase F docs/scripts install Calico (or documented Cilium) + Chaos Mesh
  **2.8.3** into `chaos-mesh` on a disposable kind cluster before #23–#26.
- Postmortems (#27) cite kind, CNI, and Chaos Mesh **2.8.3** in the
  environment section.
- ADR-011 verification and #24 share the same lab; recreate the cluster if
  CNI/chaos install is uncertain.
- Existing `chaos-demo.sh` remains a non-K8s demo path; it does not satisfy
  Phase F exit criteria.
