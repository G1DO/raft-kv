# ADR-004 — Panic on corrupt persistent file at startup

**Status:** Accepted

## Context

`NewRaft` opens three persistent files at startup: the WAL
(`<data>/raft.log`), the term/vote state file, and the snapshot file.
Each of them can be unparsable on disk — a partial flush during a crash,
a hardware fault, or an operator who edited the file by hand. The question
is what `NewRaft` should do when one of them fails to parse.

Options:

1. **Panic.** Refuse to start. The operator must investigate and recover
   (typically by replacing the corrupt file with a snapshot from a healthy
   peer, or by deleting the data dir and letting the node catch up via
   `InstallSnapshot`).
2. **Return an error.** Let `main` decide. Likely the binary still exits
   non-zero, but unit tests and library callers get a recoverable signal.
3. **Quarantine and continue.** Rename the bad file, start with an empty
   log/state/snapshot, let normal Raft replication catch the node up.

## Decision

**Panic.** Implemented at
[internal/raft/raft.go:84-105](../../internal/raft/raft.go#L84-L105):
each of `log.NewLog`, `LoadRaftState`, `LoadSnapshot`, and
`persistentLog.Replay` panics on error.

This was deferred from M1; it is the accepted behaviour going forward.

## Rejected alternatives

**Return an error.** The "correct" Go idiom and consistent with
`NewServer` ([internal/server/server.go:19](../../internal/server/server.go#L19)),
which returns `(*Server, error)`. We rejected it for now because:
- It would require changing every call site (tests, the cluster-mode
  server, the legacy `Server`) without changing behaviour — `main` and
  the tests would all still exit / `t.Fatal` on the error.
- It introduces an asymmetry where some setup failures are fatal panics
  and others are errors, with no clear principle for which is which.
- The change is mechanical and low-risk to do later. Parked as a
  follow-up.

**Quarantine and continue.** Dangerous. A corrupt WAL could mean we have
committed entries that we just chose not to read — silently starting
from an empty log makes us a divergent voter in the cluster, which can
violate Raft's safety invariants (we could vote for a candidate whose
log is *behind* what we previously committed). The cure is worse than
the disease.

## Consequences

**What we commit to:**
- Disk corruption is an *operator* problem, not a *runtime* one. The
  node stops; the operator fixes it.
- `NewRaft` has the constructor signature `*Raft` (no error). This is
  inconsistent with `NewServer` ([internal/server/server.go:19](../../internal/server/server.go#L19))
  and is a documented wart.
- Tests that want to simulate a corrupt-file recovery have to use
  `recover()`. None do today.

**What becomes harder:**
- Embedding `raft-kv` as a library inside a long-running process is
  awkward — a corrupt file kills the host process, not just the Raft
  instance. (Not a real use case today.)
- Distinguishing "fresh install, no files yet" from "corrupt files" in
  the panic message requires care; we currently rely on `os.IsNotExist`
  checks inside the loaders to treat "missing" as "empty" rather than
  as an error.

## Notes for reviewers

The safety argument for refusing to start is the important one: a Raft
node's identity in the cluster is its on-disk state. If that state is
unreadable, we cannot safely act as the node we are claiming to be —
votes we previously cast might be lost, committed log entries we
acknowledged might be invisible to us. Continuing in that state risks
correctness; the right answer is to stop and let a human reinitialise
the node from a healthy peer's snapshot.

Returning an error instead of panicking is a stylistic improvement that
does not change this safety story. It is on the parked list.
