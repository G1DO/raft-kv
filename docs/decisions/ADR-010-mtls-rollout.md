# ADR-010 — Peer mTLS rollout and failure mode

**Status:** Accepted · **Date:** 2026-07-11

## Context

[ADR-009](ADR-009-mtls-peer-identity.md) defines *what* a peer certificate is
(Vault PKI, per-ordinal DNS SANs, Secret mount contract). It deliberately
left open *how* the binary and chart behave when TLS is enabled, when material
is missing, and whether CA rotation needs a dual-trust window.

Without that contract, Phase A is likely to ship a “try TLS, else plaintext”
path that looks convenient in demos and silently reopens threat-model T1.
This ADR locks rollout and failure modes before transport code lands.

## Decision

### Mode selection (explicit, not opportunistic)

| Condition | Behaviour |
|-----------|-----------|
| TLS config **set** and CA/cert/key files **readable** | Dial and listen with mTLS; enforce ADR-009 identity checks |
| TLS config **set** and any required file **missing / unreadable** | **Fail closed** — do not start peer RPC (or abort process startup). Log which path failed; never fall back to plaintext |
| TLS config **unset** | Plaintext peer RPC (legacy / test path only) |
| TLS handshake or verify failure | Treat peer as unreachable (existing `callRPC` bool path). **Do not** retry the same RPC over plaintext |

“TLS config set” means the server/Raft boundary was given non-empty CA, cert,
and key paths (flags / struct fields fixed in Phase A #1). Partial config
(only one of three paths) is also fail-closed.

There is **no** silent TLS→plaintext fallback in any path.

### Kubernetes / Helm

- Chart defaults: mTLS **on**. Every ordinal mounts `{{ fullname }}-N-tls` and
  passes the three paths into the container.
- Missing Secret, empty volume, or wrong keys → pod fails startup (fail
  closed). That is correct: a plaintext Raft port in-cluster would violate M8.
- Operators must have Vault/ESO (or a lab equivalent) before the release is
  healthy. Document prerequisites; do not add a Helm `tls.enabled: false`
  escape that ships as a “secure” chart.

### Tests

- Unit and white-box Raft tests may omit TLS config and keep plaintext
  transport. That is an **explicit absence of config**, not a downgrade after
  a TLS error.
- Focused TLS tests (Phase A #4) exercise: valid mTLS, missing client cert,
  unknown CA, wrong server name, wrong peer identity, and “TLS configured but
  files missing → fail closed.”
- Test helpers must not implement “if tls.Dial fails, net.Dial.”

### Local kind / `cluster-up.sh`

- Prefer the **same** TLS-on path as production: scripted self-signed CA
  issuing per-ordinal leaves with ADR-009 SANs, then pass the three paths.
- Do not teach a default plaintext multi-node demo once Phase A lands; a
  documented opt-in for emergency debugging is allowed only as unset TLS
  flags, never as automatic fallback.

### Certificate and CA rotation

| Change | M8 stance |
|--------|-----------|
| Leaf renew under the **same** CA | Supported ops path: ESO refreshes Secret → **restart** that pod → rejoin (ADR-009; drill in Phase B #9) |
| In-process cert reload | **Not** required for M8; restart is the documented mechanism unless #9 proves otherwise |
| **CA** replacement / dual-trust window | **Not claimed.** Do not document “CA rotation is supported” until a dual-trust (old+new CA in the trust store) drill is designed, tested, and written up |
| Rolling leaf change while quorum alive | One follower at a time; wait Ready before the next. Chart/StatefulSet roll already serializes on `/readyz` |

Failing closed during a bad mount is preferred over running with a stale or
empty trust store.

## Rejected alternatives

**Try mTLS, then fall back to plaintext on dial/handshake failure.**
Hides misconfiguration and lets an attacker force plaintext by breaking TLS.
Rejected.

**Helm `tls.enabled: false` as a first-class production toggle.**
Guarantees a documented insecure mode that demos will use by accident.
Rejected for the default chart; plaintext remains “flags unset” for tests only.

**Claim dual-trust CA rotation in M8 without a tested drill.**
Would overstate the security story. Leaf renewal + restart is enough for the
milestone; CA roll is a follow-up.

**Fail open (listen plaintext) when the Secret is missing “so the cluster
still forms.”**
Quorum on an unauthenticated port is worse than a CrashLoop that forces the
operator to fix delivery. Rejected.

## Consequences

- Phase A (#1–#6) implements the three-way mode table above; chart wiring
  always supplies TLS paths in the default values.
- Phase B (#7–#10) must make Secret presence a hard dependency of a Ready
  pod; rotation runbooks say restart-required and do **not** claim CA
  dual-trust until tested.
- ADR-009’s “tests may use plaintext if D2 records it” is satisfied: plaintext
  = TLS config unset only.
- Threat-model T1 / T3 / T8 are **Mitigated** (T6 peer half **Partial**)
  after Phase A verification; this ADR remains the rollout contract
  (fail-closed, no dual-trust CA claim without a drill).
- Operators see loud startup failure on missing mounts instead of a quiet,
  insecure cluster.
