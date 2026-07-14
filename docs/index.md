# raft-kv

Distributed key-value store built **from scratch** on the [Raft](https://raft.github.io/raft.pdf) consensus algorithm, in Go.

**Code:** [github.com/G1DO/raft-kv2](https://github.com/G1DO/raft-kv2)

## Docs

| Doc | What |
|---|---|
| [Design](design.md) | Architecture, trade-offs, conventions |
| [Threat model](threat-model.md) | Risks and mitigations |
| [Benchmarks](benchmarks.md) | Measured recovery / throughput |
| [ADRs](decisions/) | Architecture decision records |
| [Runbooks](runbooks/) | Audit, chaos, NetworkPolicy, TLS, restore |
| [Incidents](incidents/) | Chaos Mesh postmortems (Phase F) |
| [Diagrams](diagrams/) | Architecture + trust-boundary SVGs |

## Quick links

- [Architecture diagram (SVG)](diagrams/rendered/architecture.svg)
- [Trust boundary (SVG)](diagrams/rendered/trust-boundary.svg)
- Helm chart: `deploy/helm/raft-kv`
- Chaos lab: `./scripts/chaos-phase-f-28.sh` / [chaos runbook](runbooks/chaos.md)
