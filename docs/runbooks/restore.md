# Runbook — Backup & restore

Operator guide for recovering a raft-kv node (or the whole cluster) on
Kubernetes / kind. Scripts: [`scripts/backup.sh`](../../scripts/backup.sh),
[`scripts/restore.sh`](../../scripts/restore.sh).

## Decision rule (read this first)

```
Quorum still alive?   →  ALWAYS wipe-rejoin. Never restore a backup into a live cluster.
Full cluster loss?    →  restore / full from backup is allowed (disaster only).
```

| Situation | Path | Why |
|-----------|------|-----|
| One bad pod, ≥ majority of *others* Ready | **wipe** | Empty PVC → fresh `NewRaft` → catch up via AppendEntries / InstallSnapshot |
| All voters gone / all PVCs lost | **restore** or **full** | Only then is unpacking a backup safe enough to attempt |
| Quorum alive *and* you unpack a backup anyway | **Don't** | Stale membership in the archive can resurrect a removed peer or split-brain |

**Wipe discards term/vote** on that node. That is only safe while a majority of
*other* voters remains intact — otherwise a wiped node that had voted this term
could double-vote if you later restore its old state.

**Restore embeds membership.** A backup's log/snapshot can carry an old
AddServer/RemoveServer config. Restoring it into a cluster that later changed
membership can bring back a removed peer or elect a leader on a stale config.

---

## Symptoms → path

| What you see | Likely cause | Do this |
|--------------|--------------|---------|
| Pod CrashLoop; logs show panic in `NewRaft` / `Replay` / `LoadRaftState` / `LoadSnapshot` | Corrupt `raft.log`, `raft.state`, or `raft.snapshot` on the PVC | If quorum alive → **wipe**. If whole cluster dead → **restore** / **full** from a *prior good* backup. See [ADR-004](../decisions/ADR-004-panic-on-corrupt-file.md). |
| PVC deleted / volume empty / node boots with empty KV while peers have data | Lost disk or fresh volume | **wipe** (or just let it catch up if already empty and peers are healthy) |
| Operator deleted the wrong pod's data / botched a manual copy | Human error | Quorum alive → **wipe**. Do **not** copy a live `/data` tarball onto a live cluster. |
| All three pods down, PVCs gone or unreadable | Disaster | Take the newest *quiesced* backup you trust → **full** (preferred) or **restore** onto one seed then wipe-rejoin others |
| `/readyz` 503 on one pod, others Ready and serving | Catch-up / no known leader / `needsCatchUp` | Wait for AE; if stuck after minutes with quorum healthy → **wipe** that pod |

---

## Backup (before you need it)

Never tar a **live** writer's `/data`. The WAL is append-only and fsync'd per
entry, but a concurrent copy can still capture a torn last record — exactly the
corrupt-file case that makes `NewRaft` panic ([ADR-004](../decisions/ADR-004-panic-on-corrupt-file.md)).

`backup.sh` quiesces a **Ready follower** (not the leader): delete the pod
(process stops, RWO PVC released), mount the PVC on a busybox helper, tar
`raft.log` / `raft.state` / `raft.snapshot` (if present), release the helper,
wait for Ready. Distroless images have no shell, so `kill -STOP` via exec is
not an option.

```bash
./scripts/backup.sh
./scripts/backup.sh --namespace default --out ./backups
./scripts/backup.sh --pod raft-kv-1          # must be a follower
```

Requires ≥2 Ready pods (keep quorum while the follower is down). Archives land
under `./backups/` (gitignored).

---

## Procedures

Assume namespace `default`, release `raft-kv`, pods `raft-kv-0..2`. Adjust
`--namespace` / `--pod` as needed.

### (a) Wipe-rejoin — quorum survives

**When:** One damaged node; at least a majority of *other* Ready pods remain
(for N=3: ≥2 Ready among the others before wipe).

```bash
# Confirm quorum without the target
kubectl get pods -l app=raft-kv -o wide

./scripts/restore.sh wipe --pod raft-kv-2
```

What the script does: delete the pod → busybox helper binds `data-<pod>` →
empty `/data` → release helper → wait until the StatefulSet pod is Ready.

**Verify:**

```bash
kubectl get pods -l app=raft-kv
# PUT a sentinel via the leader, then check the recovered pod's metrics:
#   raft_applied_index ≥ leader's applied index after the PUT
```

### (b) Restore-from-backup — disaster, one seed

**When:** Full cluster loss (or you are sure no quorum survives). Refuses if
≥ majority pods are already Ready unless `--force-disaster`.

```bash
./scripts/restore.sh restore \
  --pod raft-kv-0 \
  --archive backups/raft-kv-….tar.gz \
  --force-disaster
```

Prefer **wipe** for any other damaged members once a seed is up. Prefer **full**
if every PVC must be rebuilt from one archive.

### (c) Full-cluster restore — all PVCs lost

**When:** Disaster; you want one seed from backup and the other voters to
rejoin empty.

```bash
./scripts/restore.sh full \
  --pod raft-kv-0 \
  --archive backups/raft-kv-….tar.gz
```

Implies `--force-disaster`: restore onto the seed, then wipe-rejoin each other
member. Stale-membership warning still applies to the seed's archive.

---

## Measured MTTR (kind, 2026-07-10)

Wall-clock from damage start to **Ready** (`/readyz` / D1 semantics) **and**
recovery proof (sentinel `PUT` through the leader; recovered pod's
`raft_applied_index` ≥ leader's after that PUT). n=5 per path.
Harness: `scripts/measure-restore-mttr.sh` (Kubernetes-level; not `cmd/bench`).

| Path | min | p50 | mean | max |
|------|-----|-----|------|-----|
| wipe (a) | 37.3s | **38.0s** | 51.8s | 107.8s |
| restore (b) | 37.4s | **37.6s** | 37.6s | 37.8s |
| full (c) | 109.4s | **110.1s** | 109.9s | 110.2s |

**How to read these**

- Prefer **p50** (and max). Wipe **mean** is pulled by one ~108s outlier
  (DNS / Ready settle after the helper releases the PVC).
- ~36s of a single-PVC path is mostly the **helper + RWO + STS recreate +
  probes**, not Raft catch-up. Proof after Ready is ~1–2s.
- Full ≈ seed restore (~36s) + wipe-rejoin of two peers (~36s each).

These are kind / local numbers for *this* scripted path — not a datacenter SLO.

---

## Gotchas

### Corrupt file → panic on boot (ADR-004)

Unparsable `raft.log`, `raft.state`, or `raft.snapshot` makes `NewRaft` **panic**.
The process will not start with a quarantined empty log (that would risk a
divergent voter). Treat it as an operator recovery: wipe-rejoin if quorum
survives; restore from a known-good quiesced backup only if the cluster is
gone. Details: [ADR-004](../decisions/ADR-004-panic-on-corrupt-file.md).

### Never live-copy `/data`

A torn last WAL record is a silent landmine until the next boot. Always use
`backup.sh` (quiesced follower) or an equivalent stop-then-copy.

### Stale membership on restore

If the cluster ever ran `ADD_SERVER` / `REMOVE_SERVER` after the backup was
taken, restoring that archive can resurrect a removed peer or elect on an old
config. Rule again: **quorum alive → wipe only**.

### Distroless / no `kubectl exec` shell

Production image has no shell — you cannot `kill -STOP` or `tar` from inside
the raft-kv container. The scripts use a short-lived busybox helper that mounts
the same PVC (RWO: the StatefulSet pod stays Pending while the helper holds it).

### Ready wait after disaster

After restore, `/readyz` stays false until the node knows a leader and has
cleared catch-up (`needsCatchUp`). `restore.sh` waits up to ~6 minutes. If it
times out, check peer DNS (`raft-kv-N.raft-kv`), elections in logs, and whether
a quorum of empty peers is electing without the seed.

---

## Quick reference

```bash
# Backup (follower, quiesced)
./scripts/backup.sh --out ./backups

# Quorum alive — rebuild one node from peers
./scripts/restore.sh wipe --pod raft-kv-2

# Disaster — one seed from archive
./scripts/restore.sh restore --pod raft-kv-0 --archive backups/….tar.gz --force-disaster

# Disaster — seed + wipe-rejoin others
./scripts/restore.sh full --pod raft-kv-0 --archive backups/….tar.gz

# Re-measure MTTR (optional)
./scripts/measure-restore-mttr.sh --path "wipe restore full" --trials 5 --load 500
```
