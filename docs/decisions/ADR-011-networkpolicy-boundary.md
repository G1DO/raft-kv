# ADR-011 — NetworkPolicy client and peer boundary

**Status:** Accepted · **Date:** 2026-07-12

## Context

On Kubernetes the raft-kv StatefulSet exposes three ports on a headless
Service ([deploy/helm/raft-kv/templates/service.yaml](../../deploy/helm/raft-kv/templates/service.yaml)):

| Port | Name | Role |
|------|------|------|
| 8080 | client | Line protocol (PUT/GET/DELETE + `ADD_SERVER` / `REMOVE_SERVER`) |
| 9090 | raft | Peer RPC |
| 2112 | metrics | `/metrics`, `/healthz`, `/readyz` |

There is no NetworkPolicy today: any pod that can route to those ports can
speak the protocol. Threat-model T7 (cheap DoS of the Raft port) and T9
(unauthenticated membership changes on the client port) are mitigated in M8
**only** by narrowing who may connect — not by application auth (explicitly
out of scope).

Phase C (#11–#14) will add default-deny ingress/egress. Without a named
client boundary, the chart is likely to ship “allow 8080 from anywhere,”
which fails the M8 goal. This ADR locks selectors and egress before those
manifests are written.

Enforcement depends on the cluster CNI supporting NetworkPolicy; document
that prerequisite in Phase C, do not claim policy works on every kind
install without checking.

## Decision

### Default deny

In the `raft-kv` namespace (Argo destination), apply ingress and egress
**default-deny** NetworkPolicies selecting the raft-kv workload
(`app: raft-kv`, matching
[deploy/helm/raft-kv/templates/_helpers.tpl](../../deploy/helm/raft-kv/templates/_helpers.tpl)).
Allow only the rules below. No unrestricted CIDRs (`0.0.0.0/0`,
`10.0.0.0/8` as a shortcut for “cluster”).

### Ingress allow list

| Port | Allowed sources | Rationale |
|------|-----------------|-----------|
| **8080** (client) | Pods with label `raft-kv.client: "true"` | Named client workload identity. Prefer placing demo/bench clients in namespace `raft-kv-clients` **and** requiring that label so a stray labeled pod in another ns is not enough unless the policy’s `namespaceSelector` also matches (chart: select namespace `raft-kv-clients` **or** same-ns pods with the label for Jobs that must share the PVC ns — default chart rule: **namespace `raft-kv-clients` + label `raft-kv.client: "true"`**) |
| **9090** (raft) | Pods with `app: raft-kv` in the `raft-kv` namespace | Peers only |
| **2112** (metrics/health) | Prometheus pods in namespace `observability` | Matches the M6 stack (`deploy/observability`, `fullnameOverride: kps`). Select with `namespaceSelector` on `kubernetes.io/metadata.name: observability` and `podSelector` matching kube-prometheus-stack Prometheus pods (e.g. `app.kubernetes.io/name: prometheus`). Not open to all pods in the cluster |

**Rejected for 8080:** allow from the entire cluster, from `observability`, or
from “any namespace.” That reopens T9 to every compromised workload that can
route to the Service.

**Ingress gateway:** not required for M8. If an Ingress/Gateway is added
later, it becomes an additional named source (its controller pods or a
dedicated label), not a reason to open 8080 widely. Until then, in-cluster
clients are labeled pods only.

### Client workload contract

| Item | Contract |
|------|----------|
| Label | `raft-kv.client: "true"` (required on every allowed client pod) |
| Namespace | `raft-kv-clients` for durable clients / bench Jobs used in docs |
| Not shipped as a full app | Chart may document a one-line example Pod/Job; no productized client Deployment in M8 |
| Break-glass | `kubectl port-forward` / node access bypasses pod NetworkPolicy — operators only; not a substitute for the label boundary |

### Egress allow list (from raft-kv pods)

| Destination | Ports | When |
|-------------|-------|------|
| Cluster DNS (`kube-dns` / CoreDNS in `kube-system`) | UDP/TCP 53 | Always |
| Peer pods (`app: raft-kv` in `raft-kv`) | TCP 9090 | Always |
| Configured OTLP endpoint | TCP as in `tracing.otlpEndpoint` (Tempo default `tempo.observability.svc.cluster.local:4318`) | Only when tracing is enabled; chart values must drive the egress rule so an empty OTLP setting does not open 4318 |

No other egress (no arbitrary HTTPS, no Kubernetes API — the workload uses
`automountServiceAccountToken: false` in Phase C #13).

### Probes and CNI caveats

`/healthz` and `/readyz` share port 2112 with metrics. Kubelet probe traffic
is node→pod; whether NetworkPolicy applies depends on the CNI.

- Prefer keeping probe paths on 2112 and documenting verification on the
  target CNI (kind + the CNI used in the M8 lab from D5).
- If probes break under default-deny, add the **minimal** exception required
  by that CNI (often allowing the metrics port from the node network or
  marked probe sources) — **never** widen 8080 or 9090 to fix probes.
- Do not open 2112 to all namespaces “for convenience.”

### Honesty vs client auth

This boundary reduces who can attempt T9; it does **not** authenticate
clients or authorize `ADD_SERVER` / `REMOVE_SERVER`. Threat-model updates in
Phase C must say “network-authorized clients,” not “authenticated users.”

## Rejected alternatives

**Allow client port from anywhere / whole cluster.**
Violates the M8 goal and leaves T9 fully open inside the cluster.

**Open metrics to all pods “so scraping works.”**
Prometheus is already namespaced under `observability`; scrape from that
selector only.

**Skip naming a client label until “we have a real client.”**
Phase C cannot ship a meaningful deny without a positive allow. The label +
`raft-kv-clients` namespace is the allow.

**Rely on mTLS peer auth instead of NetworkPolicy for the client port.**
Peer mTLS ([ADR-009](ADR-009-mtls-peer-identity.md) /
[ADR-010](ADR-010-mtls-rollout.md)) does not cover 8080; client TLS/auth is
out of M8 scope.

## Consequences

- Phase C #11 implements default-deny plus the allow rules above as Helm
  templates (values for Prometheus namespace/labels and optional OTLP host/port).
- #12’s negative tests: unlabeled pod cannot reach 8080/9090/2112; labeled
  client reaches only 8080; Prometheus reaches only 2112; peers keep quorum.
- Demo/bench instructions must create clients in `raft-kv-clients` with
  `raft-kv.client: "true"` (or use break-glass port-forward and say so).
- D5’s chaos lab must use a NetworkPolicy-capable CNI so partition
  experiments and these policies can coexist (#24).
- Threat-model T7/T9 status updates wait for #12 verification, not this ADR
  alone.
