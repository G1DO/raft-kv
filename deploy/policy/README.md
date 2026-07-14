# Admission policy — only run images this repo signed

This is the **in-cluster half of the supply chain**. The CI side
([image.yml](../../.github/workflows/image.yml)) builds, scans, and *signs* every
published image keyless (GitHub OIDC → Fulcio cert → Rekor transparency log). That
proves provenance, but proof is worthless if the cluster will still run anything. This
directory closes the loop: a [sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/)
admission webhook that makes the API server **reject a pod whose image is not signed by
this repo's `image.yml` workflow** — turning "we sign our images" from a claim into an
enforced control.

- [`raft-kv-image-policy.yaml`](raft-kv-image-policy.yaml) — the `ClusterImagePolicy`:
  glob `ghcr.io/g1do/raftkv**`, keyless authority pinned to our workflow identity
  (issuer `https://token.actions.githubusercontent.com`, subject = `…/image.yml@…`),
  Rekor inclusion required.
- Why policy-controller and not Kyverno: [ADR-006](../../docs/decisions/ADR-006-policy-controller-vs-kyverno.md).

## How it enforces

policy-controller runs a validating webhook on pod-creating resources, **but only in
namespaces you explicitly opt in** with the label `policy.sigstore.dev/include: "true"`.
Inside an opted-in namespace:

- An image matching a `ClusterImagePolicy` must satisfy its authorities (here: a valid
  keyless signature from our workflow identity, logged in Rekor) or the pod is denied.
- An image matching **no** policy is governed by the controller's `no-match-policy`, which
  is `deny` by default — so an unrelated unsigned image is rejected, not admitted unverified.
  The install step below sets it explicitly anyway, to pin that posture against a chart
  upgrade that might change the default.

Unlabeled namespaces (e.g. `argocd`, `raft-kv`, `kube-system`) are untouched — the
webhook's `namespaceSelector` excludes them — so turning this on cannot brick the cluster.

## Install + apply

```bash
helm repo add sigstore https://sigstore.github.io/helm-charts
helm repo update
helm install policy-controller sigstore/policy-controller \
  --namespace cosign-system --create-namespace --wait

# Pin default-deny (already the controller's default) so images matching no policy stay denied.
kubectl patch configmap config-policy-controller -n cosign-system \
  --type merge -p '{"data":{"no-match-policy":"deny"}}'

kubectl apply -f deploy/policy/raft-kv-image-policy.yaml
```

Opt a namespace in (do this for `raft-kv` once enforcement is end-to-end — see the gap below):

```bash
kubectl label namespace raft-kv policy.sigstore.dev/include=true
```

## Demonstrate (throwaway namespace)

```bash
kubectl create namespace policy-demo
kubectl label namespace policy-demo policy.sigstore.dev/include=true

# Unsigned / unknown image -> DENIED by default-deny ("no matching policies").
kubectl run unsigned -n policy-demo --image=busybox:1.36 --restart=Never --command -- sleep 3600
#   Error from server (BadRequest): admission webhook "policy.sigstore.dev" denied the
#   request: validation failed: no matching policies: ... busybox ...

# Our published image -> the gate resolves the tag to a digest and runs keyless verification
# against our Fulcio identity. A DUAL-SIGNED image (legacy `.sig` present) is admitted; images
# published before dual-signing are bundle-only and denied (see the limitation note below).
kubectl run signed -n policy-demo --image=ghcr.io/g1do/raftkv:main --restart=Never \
  --dry-run=server --command -- /raft-kv

# Control: the SAME unsigned image in an un-opted-in namespace is admitted, proving the
# gate is scoped to the include label and the rest of the cluster is unaffected.
kubectl run ctl -n default --image=busybox:1.36 --restart=Never --dry-run=server --command -- sleep 1
```

`--dry-run=server` runs the real admission webhooks (the controller's `sideEffects: None`),
so you see the verdict without creating pods.

## How the signature is verified (and the bundle-format limitation)

policy-controller — checked through **v0.15.1** (verified live 2026-06-27) — verifies cosign
bundle *attestations* but **not** bundle *signatures* yet: against a bundle-format signature it
runs the full keyless path (resolves the digest, matches our authority, reaches Fulcio) and then
returns `bundle support for image signatures is not yet implemented`. cosign **v3** signs in that
bundle format by default, so `signatureFormat: bundle` cannot admit our images (and the legacy
default reports `no signatures found`, since no `.sig` tag would exist).

To make admission enforceable today *without* giving up the modern format, `image.yml`
**dual-signs** every image — the modern bundle (read by `cosign verify`) **and** a legacy
`sha256-<digest>.sig` (`--new-bundle-format=false`). This policy verifies the **legacy**
signature (`signatureFormat: legacy`), which policy-controller supports now. When upstream adds
bundle-signature verification, drop the legacy sign step in `image.yml` and flip this policy back
to `signatureFormat: bundle`.

Two caveats, stated plainly:

- **Confirmed at the config level, not yet end-to-end.** The dual-sign step and the legacy
  policy are correct by construction, but the full "legacy `.sig` published → policy-controller
  admits it" loop only runs on the next `main`/`v*` publish (PRs build but never sign). What was
  demonstrated live above is the *rejection* path.
- **Pre-existing images are bundle-only.** Images published before the dual-signing change carry
  no legacy `.sig`, so they are denied until re-published — opt the `raft-kv` namespace in only
  after a dual-signed image has rolled out.

The bundle-signature limitation applies to any cosign-go-based verifier (e.g. Kyverno
`verifyImages`), so it is a property of the ecosystem at this date, not of this tool choice —
see [ADR-006](../../docs/decisions/ADR-006-policy-controller-vs-kyverno.md).

## Uninstall

```bash
kubectl delete namespace policy-demo --ignore-not-found
kubectl delete -f deploy/policy/raft-kv-image-policy.yaml --ignore-not-found
kubectl label namespace raft-kv policy.sigstore.dev/include- 2>/dev/null || true
helm uninstall policy-controller -n cosign-system
kubectl delete namespace cosign-system --ignore-not-found
```
