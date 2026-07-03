# ADR-007 — OpenTelemetry dependency vs. strictly zero-dep tracing

**Status:** Accepted · **Date:** 2026-07-03

## Context

Until M6 this project's headline was absolute: hand-rolled in Go on the standard
library, **zero third-party dependencies, no `go.sum`**. M6 (observability) keeps
that stance effortlessly for two of the three signals:

- **Metrics** — a hand-rolled Prometheus text-exposition registry
  (`internal/metrics/`, ~250 LOC): gauges, counters, histograms with explicit
  buckets, concurrency-safe, golden-tested output. No `client_golang`.
- **Logs** — stdlib `log/slog` with a JSON handler; Promtail ships stdout to Loki.

**Distributed tracing is the exception**: there is no stdlib OpenTelemetry, and a
span that never leaves the process is not distributed tracing. Three options:

1. **Strictly zero-dep** — propagate a trace-ID through
   request→append→replicate→commit→apply, correlate logs↔metrics by that ID.
   No Tempo waterfall; the "OpenTelemetry" line comes off the CV claim.
2. **OTel in a nested Go module** — root `go.mod`/`go.sum` stay pristine; the
   OTLP exporter lives in a separate module with a second build target. (A build
   tag alone is *not* enough — `go mod tidy` would still pull OTel into the root
   `go.sum`.)
3. **Accept the OTel dependency and re-frame the claim** — add
   `go.opentelemetry.io/otel` + the OTLP exporter to the root module: the first
   `go.sum`, roughly a dozen transitive dependencies, real Tempo traces.

## Decision

**Option 3: take the dependency, re-frame the headline.** The claim becomes:

> **Zero-dependency consensus core** — the Raft engine, WAL, snapshots, KV state
> machine, RPC transport, and metrics registry are hand-rolled on the standard
> library — **with industry-standard OpenTelemetry observability** at the edge.

Two rules keep that sentence honest in the code:

- **`internal/raft`, `internal/log`, `internal/kv`, and `internal/metrics` must
  not import OTel** (nor anything outside the stdlib and each other). The
  instrumentation lives in `internal/server` and `cmd/server` — the same layer
  that already owns client I/O.
- Tracing is **opt-in at runtime** (`--otlp-endpoint`, default off): the binary
  behaves identically, dependency-loaded or not, when tracing is disabled.

The exporter speaks **OTLP/HTTP directly to Tempo's built-in OTLP receiver** —
no OpenTelemetry Collector pod. At one binary and one backend, a Collector is a
hop that adds failure modes and YAML but no capability; if fan-out or
redirection is ever needed, inserting one later is a config change, not a code
change.

## Rejected alternatives

**Option 1 (strictly zero-dep)** preserves the purest headline but caps the
milestone's learning value: the point of M6 was chosen to be the *real* tooling
(OTel SDK semantics, OTLP, Tempo), and a hand-rolled trace-ID convention teaches
none of it. The trace-ID propagation it prescribes was built anyway (Phase B) —
it is the log-correlation key and the span seed — so option 3 strictly contains
option 1.

**Option 2 (nested module)** buys go.sum purity at the cost of a second build
target, a second module to version, and a permanent "which binary am I running?"
question. The purity it protects is also less than it appears: the *claim* that
matters ("the consensus engine is hand-rolled") is about which packages import
what, not about which file lists the checksums. Import discipline is enforceable
and inspectable; a pristine root `go.sum` alongside a shadow module is mostly
optics.

## Consequences

- `go.sum` exists now. The SBOM grows from "stdlib + distroless" to also include
  the OTel modules; the supply-chain story (SBOM attestation, Trivy scans)
  already handles third-party code, so the pipeline needs no change.
- `docs/design.md` and the README trade "zero third-party dependencies" for
  "zero-dependency consensus core"; anywhere the old absolute claim appears is
  a bug.
- The import rule above is the new invariant to review against: OTel creep into
  `internal/raft` (spans inside the consensus engine) would silently void the
  claim. Stage timings inside consensus are exposed as metrics + slog events,
  which the trace links to by trace-ID.
