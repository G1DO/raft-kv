# ADR-012 — App security audit events (not K8s API audit)

**Status:** Accepted · **Date:** 2026-07-12

## Context

Threat-model T5 (repudiation) is open: there is no security audit trail that
records security-relevant actions. M6 already ships JSON `slog` to stdout →
Promtail → Loki for **operability** (elections, RPC failures, membership
applied). That stream is not a security audit schema: it can mix routine
Raft noise with sensitive payloads if Phase E is careless, and it does not
answer “who attempted what.”

Two different “audit” ideas get conflated:

1. **App security audit events** — raft-kv emits at the client/TLS boundary.
2. **Kubernetes API-server audit logs** — control-plane records of API calls
   (`kubectl`, controllers). Collected only if the API server is configured
   for it; **not** created by Promtail scraping pod stdout.

This ADR defines (1) for Phase E (#19–#21). It does not implement emit code.

## Decision

### Scope split

| Kind | Owner | M8 stance |
|------|--------|-----------|
| App security audit events | raft-kv → stdout → Promtail → Loki | **In scope.** Minimal schema below |
| Kubernetes API-server audit | Cluster/control-plane config | **Out of app scope.** Do not claim Loki/Promtail provides it. Optional lab note only if the disposable cluster enables API audit separately |

### Events to emit (server boundary)

| Event name (`event`) | When |
|----------------------|------|
| `audit.client.mutate` | Client `PUT` / `DELETE` accepted for Raft append or rejected (not leader / error) — one event per attempt at the server edge |
| `audit.client.membership` | `ADD_SERVER` / `REMOVE_SERVER` attempt (success or reject) |
| `audit.peer.tls_fail` | Peer TLS handshake or identity verification failure (after Phase A mTLS) |

Routine Raft chatter (`election_started`, `rpc_failed` dial timeouts, heartbeats)
stays **non-audit** unless it is specifically a TLS auth failure covered above.
NetworkPolicy drops are often invisible to the app; do not invent app events
for packets the process never sees (#21 may note “not observable from Loki”
for pure NP denies).

### Minimal schema (JSON `slog` attrs)

Every audit line includes:

| Field | Meaning |
|-------|---------|
| `audit` | Always `true` (Loki/filter marker) |
| `event` | One of the fixed names above |
| `outcome` | `allow` \| `deny` \| `error` |
| `time` | Handler timestamp (slog default) |
| `node` | Raft / pod id |
| `term` | Current term when known; omit if not |
| `remote` | Peer or client `host:port` only (no URL path, no cert PEM) |
| `actor` | Always `unauthenticated` for client events until the protocol has identity — **not** a username |
| `action` | Protocol verb or TLS stage (`PUT`, `DELETE`, `ADD_SERVER`, `REMOVE_SERVER`, `handshake`, `identity`) |
| `request_id` | Optional correlation / client request id if present in the framed command; omit if absent |
| `trace_id` | Optional if tracing is on; same as existing log correlation |

**Never log:** request **values**, raw keys when avoidable (prefer omit; if a
key id is required for ops, hash it — e.g. truncated SHA-256 — and document
that choice), certificate or private-key material, bearer tokens, full
peer cert chains.

### Actor honesty

The line protocol has no authenticated client identity (T2 / T9 fence).
Audit rows identify **network source** (`remote`) plus `actor:
unauthenticated`. Docs and the threat-model update after #21 must call this
a **network-source audit**, not a user attribution trail. Closing T5 fully
would need client auth (out of M8).

### Delivery and retention

- **Path:** existing stdout JSON → Promtail → Loki (no second sink in M8).
- **Query:** filter `audit="true"` (or `{event=~"audit\\..+"}`) so audit
  lines are separable from operational logs.
- **Retention:** whatever the observability stack is configured for (demo
  Loki/Prometheus-style short retention, e.g. on the order of **24h** in
  [deploy/observability/values.yaml](../../deploy/observability/values.yaml)
  for Prometheus; Loki follows the lab chart). Document as **lab retention**,
  not compliance-grade WORM storage.

### Kubernetes API audit

If a lab needs control-plane audit, configure the API server (or managed
equivalent) independently, restrict who can read those logs, and state
retention there. Phase E runbooks must not imply Promtail collects API
audit events.

## Rejected alternatives

**Treat all existing Raft `slog` lines as the security audit.**
Too noisy, wrong semantics, and easy to leak values if commands are logged
verbatim.

**Claim “audit to Loki” includes Kubernetes API audit.**
False pipeline; rejected.

**Log full keys and values “for forensics.”**
Reopens information disclosure in the log store (T6-shaped). Rejected.

**Call `remote` a user identity.**
Overclaims T5 closure. Rejected.

**Dedicated audit sidecar / separate syslog product in M8.**
Unnecessary given Promtail→Loki; keep one path.

## Consequences

- Phase E #19 emits only the events and fields above at the server/TLS
  boundary; #20 documents LogQL examples with `audit="true"`; #21 verifies
  allow/deny cases and updates threat-model T5 with the unauthenticated-actor
  residual.
- T5 remains **Open** until #21; this ADR is the contract, not the proof.
- Operators querying Loki for “who deleted key X” will see source address and
  verb at best — not a named user.
- No change to Kubernetes API-server logging is required to complete M8 app
  audit work.
