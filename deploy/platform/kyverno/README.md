# Platform — Kyverno admission controller

Kyverno is the **platform half** of M8 pod-posture admission. The app chart
(`deploy/helm/raft-kv`) renders the workload; this directory installs the
controller that will enforce posture rules in Phase D #16–#17.

**Do not use Kyverno for image signatures.** cosign admission stays on
[sigstore policy-controller](../../policy/) per
[ADR-006](../../../docs/decisions/ADR-006-policy-controller-vs-kyverno.md).

| Engine | Job |
|--------|-----|
| policy-controller (`cosign-system`) | Keyless cosign signature on opted-in namespaces |
| Kyverno (`kyverno`) | Pod security posture (non-root, caps, probes, limits) |

## Pin

| Component | Version |
|-----------|---------|
| Helm chart | `kyverno/kyverno` **3.8.2** |
| Kyverno app | **v1.18.2** (chart `appVersion`) |

Bump only after reading upstream release notes; chart v3 upgrades from v2 are not in-place.

## GitOps (recommended)

```bash
kubectl apply -f deploy/argo/kyverno-app.yaml
```

Argo CD renders this umbrella chart into namespace `kyverno`. It is intentionally
**not** bundled with the `raft-kv` or `observability` Applications.

## Lab / kind (Helm)

```bash
./scripts/install-kyverno.sh
```

Or manually:

```bash
helm dependency build deploy/platform/kyverno
helm upgrade --install kyverno deploy/platform/kyverno \
  --namespace kyverno --create-namespace --wait --timeout 300s
```

## Verify (#15 smoke)

```bash
kubectl get pods -n kyverno
kubectl get crd clusterpolicies.kyverno.io policies.kyverno.io
```

All controller pods should be `Running`. Posture policies:

```bash
./scripts/apply-kyverno-policies.sh
kubectl get clusterpolicy raft-kv-posture
./scripts/verify-kyverno-posture.sh --live
```

Policies ship in **Audit** mode (#17 → Enforce).

## Uninstall

```bash
helm uninstall kyverno -n kyverno
kubectl delete namespace kyverno --ignore-not-found
```

If deployed via Argo CD, delete the Application (cascade prunes the release).

## What is deliberately not here

- `verifyImages` / cosign rules (policy-controller owns signatures)
- Audit → Enforce rollout (#17)
- Helm CI policy drift checks (#18)
