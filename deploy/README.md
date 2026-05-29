# Deploying raft-kv

The same binary runs everywhere — Raft doesn't care whether nodes are processes
on your laptop, containers, or machines in different datacenters. They just
exchange JSON-over-TCP at the addresses you give them via `--peers`. So the
question isn't *can* a target run it, but *what failure modes you want to test*.

| Target | What it adds | Cost | Test guide |
|---|---|---|---|
| **Local processes** | nothing — fastest feedback | free | `scripts/cluster-up.sh` (repo root) |
| **Docker Compose** | true per-node isolation; easy partitions | free | [below](#2-docker-compose) |
| **Kubernetes (kind/minikube)** | orchestration, self-healing, PVCs | free | [below](#3-kubernetes) |
| **AWS EC2** | survives a whole **machine** dying | 💵 | [`aws/README.md`](aws/README.md) |
| **AWS EKS** | managed k8s across AZs | 💵💵 | [`aws/README.md`](aws/README.md) |

Recommended order: master the local script, then Compose (the sweet spot), then
k8s on kind, and only reach for AWS if you specifically need real multi-machine
fault tolerance.

> **The cluster has no auth or encryption** (see `docs/threat-model.md`). Keep
> ports `8080`/`9000` on trusted networks only.

---

## 1. Local processes

Already in the repo, no containers needed:

```bash
./scripts/cluster-up.sh
printf 'PUT foo bar\n' | nc localhost 8081   # follow any NOT_LEADER hint
```

---

## 2. Docker Compose

Three containers on a private bridge network; each node's client port is
published to the host as **8081/8082/8083**.

```bash
docker compose up --build          # build image + start 3-node cluster
printf 'PUT foo bar\nGET foo\n' | nc localhost 8081
```

Hit a follower and you'll get `NOT_LEADER localhost:8083` — the redirect hints
are wired to the **host-published** addresses, so you can follow them straight
from your laptop.

**Exercise the failure modes:**

```bash
docker compose pause node1     # simulate a partition (node1 frozen)
docker compose unpause node1   # it catches up
docker compose stop node2      # kill a node; 2/3 still forms quorum
docker compose down -v         # stop everything and wipe the data volumes
```

Data persists in named volumes (`node1-data`, …) across restarts; `-v` wipes them.

---

## 3. Kubernetes

A **StatefulSet** (stable DNS names + per-pod persistent volumes) plus a
headless Service. Each pod derives its id and peer list from its ordinal
hostname — see the entrypoint in [`k8s/raft-kv.yaml`](k8s/raft-kv.yaml).

### Local cluster with kind (free)

```bash
kind create cluster
docker build -t raft-kv:local .
kind load docker-image raft-kv:local     # push image into the kind node
kubectl apply -f deploy/k8s/raft-kv.yaml
kubectl rollout status statefulset/raft-kv
```

Talk to it:

```bash
kubectl port-forward svc/raft-kv-client 8080:8080
printf 'PUT foo bar\n' | nc localhost 8080
```

Watch self-healing — delete the leader's pod and the StatefulSet recreates it,
reattaching its PVC, while a new leader is elected meanwhile:

```bash
kubectl get pods -l app=raft-kv -w
kubectl delete pod raft-kv-0
```

### Notes / caveats

- **Quorum & startup:** `podManagementPolicy: Parallel` starts all 3 pods
  together so they can elect a leader (a single pod can't reach majority).
- **Scaling:** Raft membership is fixed from the `--peers` list at startup.
  Bumping `replicas` does **not** auto-join the new pod — you'd issue
  `ADD_SERVER` against the leader and regenerate peers. The manifest is sized
  for a fixed 3-node cluster.
- **Leader redirect:** the `NOT_LEADER` hint is an in-cluster DNS name, so
  following it only works from inside the cluster. From a port-forward you may
  need to forward a specific pod (`kubectl port-forward pod/raft-kv-2 ...`).

---

## The one thing every target has in common

Inside containers/hosts the binary **binds `0.0.0.0`** for both listeners, while
`--peers` advertises **reachable addresses** (service names, DNS, or private
IPs). The advertised client address for `NOT_LEADER` redirects comes from the
`clientAddr` field of `--peers`, *not* from the bind address — which is why the
same binary drops cleanly into Compose, k8s, and EC2.
