# Runbook — NetworkPolicy boundary (default deny)

Operator guide for the **Phase C #11** ingress/egress boundary on Kubernetes.
Selectors and port matrix are fixed in
[ADR-011](../decisions/ADR-011-networkpolicy-boundary.md).

**Enforcement requires a CNI that implements NetworkPolicy** (Calico or Cilium).
Stock **kindnet does not enforce** — policies are no-ops until a capable CNI is
installed ([ADR-013](../decisions/ADR-013-chaos-lab-environment.md)).

---

## Chart defaults

The Helm chart ships `networkPolicy.enabled: true` with four objects:

| Object | Role |
|--------|------|
| `default-deny-ingress` | Block all traffic **to** raft-kv pods |
| `default-deny-egress` | Block all traffic **from** raft-kv pods |
| `allow-ingress` | Client :8080, peer :9090, Prometheus :2112 |
| `allow-egress` | DNS :53, peer :9090, OTLP (only when `tracing.otlpEndpoint` set) |

Authorized clients must run in namespace `raft-kv-clients` with label
`raft-kv.client: "true"`. Break-glass: `kubectl port-forward` (bypasses
pod NetworkPolicy).

Disable for plaintext lab only:

```bash
helm upgrade raft-kv deploy/helm/raft-kv --set networkPolicy.enabled=false
```

---

## Verify (Phase C #12)

```bash
kubectl create namespace raft-kv-clients --dry-run=client -o yaml | kubectl apply -f -
./scripts/verify-networkpolicy.sh --namespace default
```

The script checks:

1. Helm renders four NetworkPolicy manifests
2. **Negative:** unlabeled pod in the release namespace cannot reach :8080, :9090, :2112
3. **Positive:** labeled client in `raft-kv-clients` reaches :8080 only; `PUT` returns `OK`
4. **Positive:** Prometheus-labelled pod in `observability` reaches :2112 only
5. All three raft-kv pods stay Ready

### Verified on kind + Calico (2026-07-13)

Cluster: kind `raft-kv`, Calico v3.28.2 (`calico-node` in `kube-system`, kindnet
CNI config removed), raft-kv release in `default`, `networkPolicy.enabled=true`.

```
OK: helm renders 4 NetworkPolicies
OK: CNI with NetworkPolicy enforcement detected
OK: unlabeled pod blocked on :8080 (enforcement active)
OK: unlabeled pod denied :8080 / :9090 / :2112
OK: labeled client allowed :8080; denied :9090 / :2112; PUT returned OK
OK: Prometheus-labeled pod allowed :2112; denied :8080
OK: all 3 raft-kv pods Ready
OK: NetworkPolicy verification complete
```

---

## kind + Calico (lab)

ADR-013 pins Calico for a trustworthy policy story. On an **existing** kind
cluster (kindnet already present):

```bash
# Install Calico with the cluster pod CIDR (kind default 10.244.0.0/16).
curl -fsSL https://raw.githubusercontent.com/projectcalico/calico/v3.28.2/manifests/calico.yaml \
  | sed 's|value: "192.168.0.0/16"|value: "10.244.0.0/16"|' \
  | kubectl apply -f -
kubectl delete daemonset -n kube-system kindnet --ignore-not-found
docker exec raft-kv-control-plane rm -f /etc/cni/net.d/10-kindnet.conflist
kubectl delete pod -n default -l app=raft-kv   # re-plug CNI
```

Greenfield kind clusters should set `networking.disableDefaultCNI: true` in
`deploy/kind-config.yaml` and install Calico before workloads (scripted install
is a Phase F follow-up).

---

## Troubleshooting

| Symptom | Likely cause |
|---------|----------------|
| Unlabeled pods can still connect | kindnet-only CNI, or `10-kindnet.conflist` still in `/etc/cni/net.d` |
| raft-kv pods Not Ready after policies | Egress DNS selector mismatch — CoreDNS must match `networkPolicy.dns.podLabels` (`k8s-app: kube-dns` on kind) |
| Client `PUT` times out | Missing `raft-kv-clients` namespace or `raft-kv.client=true` label |
| Probes fail under deny | CNI-specific kubelet→pod path — see ADR-011 probes section; do not widen :8080/:9090 |
