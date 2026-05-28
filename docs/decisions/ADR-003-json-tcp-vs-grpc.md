# ADR-003 — Hand-rolled JSON-over-TCP vs. gRPC for peer RPC

**Status:** Accepted (with honest trade-offs)

## Context

Raft peers need to exchange three RPC types: `RequestVote`, `AppendEntries`,
`InstallSnapshot`. Each carries structured data and expects a structured
reply. The standard options:

1. **gRPC**: code-generated stubs from `.proto`, HTTP/2 transport,
   bidirectional streams, mature retry/timeout/deadline propagation,
   detailed transport errors.
2. **net/rpc (stdlib)**: built-in Go RPC, less ceremony than gRPC, but
   Go-only and limited.
3. **Hand-rolled JSON over TCP**: open a `net.Dial` connection, write a
   `{Type, Data}` envelope, read a response envelope, hang up.

## Decision

Use **hand-rolled JSON over TCP**. Implemented at
[internal/raft/raft.go:861-900](../../internal/raft/raft.go#L861-L900)
(`callRPC`) and dispatched on the server side by a string switch on
`RPCMessage.Type` (`"RequestVote"` / `"AppendEntries"` / `"InstallSnapshot"`).

This is consistent with the project's hard constraint: zero third-party
dependencies, every layer hand-rolled on the standard library. The point
of the project is to understand consensus end-to-end, not to glue together
existing components.

## Rejected alternative

**gRPC.** The right industrial choice. It gives us:
- Detailed, typed transport errors (we can tell "connection refused" apart
  from "decode failure" apart from "deadline exceeded").
- HTTP/2 multiplexing — one connection per peer instead of dial-per-RPC.
- Code-generated client and server stubs that cannot drift apart.
- Mature `context.Context` deadline and cancellation propagation.

We rejected it because:
- It would dwarf the consensus code with generated boilerplate, undermining
  the "implementation as a teaching artefact" framing.
- It adds a dependency tree the project is explicitly avoiding.
- For a single-LAN learning prototype, the operational benefits do not
  materialise — there is no real traffic to multiplex and no real ops
  team to read the rich errors.

## Consequences

**What we commit to (the real costs of this choice):**

- **All transport errors are flattened to `bool`.** `callRPC(...) bool`
  returns `false` for dial errors, encode errors, timeout errors, decode
  errors — all the same. Callers treat an unreachable peer as a silent
  no-op (`if !ok { return }`). This means the leader cannot distinguish
  "peer is slow" from "peer is gone" from "peer's reply was malformed" —
  and in a real partition we lose the signal that would let us tell a
  partition apart from a crashed follower.
- **Connection-per-RPC.** Every RPC opens a fresh TCP connection
  (`net.DialTimeout`, defer `conn.Close()`). Under high heartbeat
  frequency this is wasteful — TLS would make it untenable. This is
  fine for a few-nodes-on-localhost demo and will become a real cost the
  first time we run on real networks.
- **Type strings are duplicated at every call site** — `"RequestVote"`,
  `"AppendEntries"`, `"InstallSnapshot"`. A typo is a silent runtime bug,
  not a compile error. Mitigated only by tests.
- **JSON parses every byte of every payload.** Binary log commands get
  base64-wrapped through JSON. Fine here, would be the first thing to
  rip out under any throughput pressure.

**What becomes harder:**
- Adding mTLS for peer authentication (M8) means hand-rolling TLS on top
  of `net.Dial` instead of toggling a gRPC dial option.
- Adding any kind of stream-based RPC (e.g. streaming `InstallSnapshot`
  for very large snapshots) means designing the framing ourselves.

## Notes for reviewers

This is the decision most likely to surface in code review as "you should
have used gRPC." The honest answer is: yes, for a production system you
should. The teaching value of writing the wire format from scratch — and
the resulting clarity about *what* a Raft RPC actually is — is the reason
this project exists.

The `bool` return is the single biggest concession. A future change worth
making, even without abandoning the hand-rolled transport, is returning
typed errors (`errPeerUnreachable` / `errPeerStale` / `errDecode`) so
the leader can at least log meaningfully.
