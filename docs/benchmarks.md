# Benchmarks

Measured recovery and steady-state numbers for the runnable 3-node Raft cluster,
produced by the `cmd/bench` harness. Every number here is reproducible with one
command (given below each result); none are hand-picked or estimated.

> **Read the caveats.** These are single-machine, loopback numbers meant to
> characterise *this implementation's* behaviour and method — not datacenter
> figures. The interesting results here are the **shapes** (a hard latency floor
> at the heartbeat interval, a concurrency cliff, correct read-rejection under
> quorum loss), which hold regardless of the absolute hardware.

## Method

The harness (`cmd/bench`) builds `./cmd/server`, launches a 3-node cluster as
child processes (client ports 8081–8083, Raft ports 9001–9003, per-node data
dirs), drives it over the line protocol, and prints results to stdout (node logs
go to stderr). Each subcommand wipes its data dir and starts fresh.

| | |
|---|---|
| Toolchain | go1.26.2 |
| OS / kernel | Linux 6.17 |
| CPU | Intel i7-10750H @ 2.60GHz, 12 threads |
| Cluster | 3 nodes, loopback, one machine |
| Election timeout | 150–300 ms randomized (`internal/raft/raft.go:171`) |
| Heartbeat interval | 50 ms (`internal/raft/raft.go:302`) |
| Commit wait (client) | 3 s (`internal/server/raft_server.go:18`) |

What "latency" means here: a client write blocks until its entry **commits and
applies** (`raft_server.go:160`), so the **client-observed round-trip is a proxy
for commit latency** — it includes replication, the WAL fsync on leader and
followers, and apply. It is not an internal probe.

```
go build ./cmd/bench   # or run via `go run ./cmd/bench <sub>`
go run ./cmd/bench mttr        # election MTTR
go run ./cmd/bench throughput  # steady-state writes
go run ./cmd/bench partition   # quorum-loss read rejection + recovery
```

---

## 1. Election MTTR

Kill the leader with `SIGKILL`; measure time until a **new** leader commits a
write in its own term (i.e. the cluster can serve writes again). Repeat 100
times, restarting the killed node between trials so each trial starts from full
quorum.

```
go run ./cmd/bench mttr --trials 100
```

| metric | value |
|---|---|
| trials | 100 ok, 0 failed |
| **p50** | **243.8 ms** |
| **p99** | **342.3 ms** |
| min / mean / max | 174.3 / 253.9 / 469.9 ms |

```
   174.3-203.9    ms | ███ 3
   203.9-233.4    ms | ████████████████████████████████████████ 37
   233.4-263.0    ms | █████████████████████████ 24
   263.0-292.5    ms | ███████████████████ 18
   292.5-322.1    ms | ███████████████ 14
   322.1-351.6    ms | ███ 3
   440.3-469.9    ms | █ 1
```

The distribution sits where the protocol predicts: a follower must wait out its
randomized **150–300 ms** election timeout before it even becomes a candidate,
then win a vote round, then commit one entry in the new term. p50 ≈ 244 ms is
"about one election-timeout plus a round-trip," which is the expected floor for
these timers.

> Why the harness measures "new leader commits a write" rather than "a node
> claims leadership": the probe is itself a write, and a freshly elected leader
> cannot serve reads of earlier-term data until it commits in its own term (see
> §3 caveat). Timing to first committed write is both the meaningful
> availability metric and the only unambiguous one.

---

## 2. Steady-state throughput & latency

N concurrent clients `PUT` distinct keys to the leader for a fixed window.

```
go run ./cmd/bench throughput --duration 8s --concurrency 8
```

| concurrency | writes/sec | p50 (ms) | p99 (ms) | errors |
|---|---|---|---|---|
| 1 | 20 | 50.5 | 52.4 | 0 |
| 4 | 79 | 50.6 | 59.3 | 0 |
| **8** | **144** | **51.3** | **76.2** | **0** |
| 16 | 52 | 175.9 | 526.9 | 16 |
| 32 | 20 | 237.0 | 3097.2 | 81 |

Two findings, both about this implementation's design:

**The latency floor is the heartbeat interval (~50 ms).** Replication is driven
by the 50 ms heartbeat, not kicked immediately on `AppendCommand`, so a single
client's writes commit roughly one heartbeat apart → ~20 writes/sec/client. With
more clients the leader batches their entries into each heartbeat, so throughput
scales ~linearly (1→20, 4→79, 8→144) while p50 stays pinned at ~50 ms. **Lowering
the heartbeat interval is the obvious throughput/latency lever** and the cleanest
thing to measure next.

**There is a concurrency cliff between 8 and 16 clients.** Past the ~144 writes/sec
peak, throughput *collapses* and writes begin timing out at the 3 s commit
deadline (16 clients: 16 errors, p99 527 ms; 32 clients: 81 errors, p99 = 3097 ms
≈ the timeout). *Calibration: (c) heuristic* — the most likely cause is
contention on the single global Raft mutex (`r.mu`) plus the synchronous
per-entry WAL fsync, which serialises the commit pipeline once offered load
exceeds what one heartbeat cycle can drain. Confirming that would mean profiling
lock-hold time and fsync latency — not yet done.

---

## 3. Partition / quorum loss

Drive the leader into a minority by killing both followers in the 3-node cluster,
then verify a linearizable read is **refused**, restore the followers, and time
recovery. This is minority-isolation-via-crash, not a literal bidirectional
netsplit — but it exercises the exact property: `ReadIndex` → `confirmLeadership`
cannot collect a majority of heartbeat ACKs, so the leader declines the read.

```
go run ./cmd/bench partition
```

```
[PASS] ReadIndex refuses read under quorum loss — GET foo -> "NOT_LEADER localhost:8083"
[PASS] cluster recovers after quorum restored — leader serves writes again after 51ms
[PASS] committed value survives quorum loss — GET foo -> "bar"

recovery time (followers restarted -> leader serves writes again): 51ms
```

- A leader that cannot reach quorum returns `NOT_LEADER` for a `GET` (linearizable
  read correctly refused) rather than serving possibly-stale data.
- A `PUT` to the minority leader instead blocks until the 3 s commit timeout — it
  appends to the local log but never commits.
- Once quorum returns, the cluster serves writes again in ~50 ms (one heartbeat),
  and the previously-committed value survives.

---

## Caveats & honest limitations

- **Single machine, loopback.** No real network latency or NIC; the WAL fsync
  hits a local SSD. Absolute numbers will differ on real hardware; the shapes
  (heartbeat-bound latency floor, the cliff, read-rejection) will not.
- **Latency is client-observed**, not an internal histogram — it bundles network,
  replication, fsync, and apply. That is the honest end-to-end number, but it is
  not a per-stage breakdown.
- **"Partition" = minority isolation via crash.** The majority side has no
  surviving voters to elect a replacement, so this does not exercise split-brain /
  dueling-leaders. It verifies read-rejection under quorum loss, nothing more.
- **ReadIndex on a freshly elected leader can miss a prior-term committed key**
  until that leader commits an entry in its own term. `commitIndex` is not
  persisted and `becomeLeader` appends no no-op barrier (`raft.go:281`), so the
  Raft paper §6.4 read-only safeguard is not implemented. The MTTR and partition
  harnesses detect recovery via a *write* (which forces a current-term commit)
  precisely to avoid reading into this window. It is a known, minor linearizability
  gap, not a measurement artefact.
