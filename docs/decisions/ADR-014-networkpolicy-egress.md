# ADR-014 — NetworkPolicy egress exceptions (DNS, peers, OTLP)

**Status:** Accepted · **Date:** 2026-07-13

## Context

Phase C #11 ships default-deny ingress **and** egress for raft-kv pods
([ADR-011](ADR-011-networkpolicy-boundary.md)). Ingress selectors (client,
peer, Prometheus) are documented there. Egress needs its own ADR because
operators will ask why DNS and telemetry are the only outbound exceptions,
and why the Kubernetes API is not.

Enforcement depends on the cluster **CNI** implementing NetworkPolicy
(Calico/Cilium). Stock kindnet does not — verified in
[docs/runbooks/networkpolicy.md](../runbooks/networkpolicy.md).

## Decision

From raft-kv pods, egress is allowed only to:

| Destination | Ports | When | Rationale |
|-------------|-------|------|-----------|
| Cluster DNS (CoreDNS in `kube-system`, selector `k8s-app: kube-dns`) | UDP/TCP 53 | Always | StatefulSet peer DNS (`<pod>.<service>`) must resolve before the first election |
| Peer pods (`app: raft-kv` in release namespace) | TCP 9090 | Always | Outbound Raft RPC (elections, replication) |
| OTLP receiver (default Tempo in `observability`, selector `app.kubernetes.io/name: tempo`) | TCP from `tracing.otlpEndpoint` | Only when `tracing.otlpEndpoint` is non-empty | ADR-007 tracing; chart does not open OTLP egress when tracing is off |

**No other egress.** In particular:

- No unrestricted HTTPS or `0.0.0.0/0` CIDR shortcuts
- No Kubernetes API (`kubernetes.default.svc:443`) — the workload does not
  call the API; Phase C #13 sets `automountServiceAccountToken: false` and
  grants no workload RBAC

Workload identity (#13) and egress deny are paired: even if a process inside
the pod tried to reach the API, there is no mounted token and NetworkPolicy
does not allow that path.

## Rejected alternatives

**Allow all cluster egress / wide private CIDRs.**
Defeats default-deny; any compromised raft-kv pod could reach any Service.

**Always open OTLP :4318.**
Tracing is optional (`tracing.otlpEndpoint` empty by default). Egress rule
is conditional in the chart.

**Open DNS to all of `kube-system` without pod selector.**
DNS is scoped to CoreDNS pods via `networkPolicy.dns.podLabels` (override in
values if your distro labels differ).

**Rely on NetworkPolicy alone for workload API access.**
Policy blocks egress to the API, but #13 still removes the token and RBAC so
compromise does not inherit `default` or broad RoleBindings.

## Consequences

- Chart: `deploy/helm/raft-kv/templates/networkpolicy.yaml` allow-egress rule;
  OTLP port from `raft-kv.otlpPort` helper when tracing is on.
- Verify: `./scripts/verify-networkpolicy.sh` (peers/DNS implicit in raft-kv
  Ready); `./scripts/verify-workload-identity.sh` (no API token/RBAC).
- Operators on kind must install a policy-capable CNI before claiming
  egress/ingress boundaries — see ADR-013 and the networkpolicy runbook.
- Threat-model T7/T9 and workload RBAC posture updated in Phase C #14.
