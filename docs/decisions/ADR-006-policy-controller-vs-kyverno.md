# ADR-006 — sigstore policy-controller vs. Kyverno for admission image verification

**Status:** Accepted · **Date:** 2026-06-27

## Context

The supply-chain pipeline already signs every published image keyless with cosign
([image.yml](../../.github/workflows/image.yml)): GitHub OIDC → Fulcio certificate →
Rekor transparency log, identity pinned to this repo's `image.yml` workflow. A signature
the cluster never checks is theatre, so Phase E closes the loop with an **admission
controller** that refuses to run a pod unless its image carries such a signature — the
in-cluster half of "we sign our images."

The question is *which* admission engine. Two are on the table, both of which can verify a
keyless cosign signature against a Fulcio identity + Rekor:

1. **sigstore policy-controller** — a purpose-built admission controller from the Sigstore
   project. A `ClusterImagePolicy` CRD declares image globs and keyless authorities.
2. **Kyverno** — a general-purpose Kubernetes policy engine. A `ClusterPolicy` with a
   `verifyImages` rule does cosign keyless verification, and the same engine also expresses
   posture rules (non-root, drop capabilities, require probes/limits).

## Decision

**sigstore policy-controller**, via
[deploy/policy/raft-kv-image-policy.yaml](../../deploy/policy/raft-kv-image-policy.yaml).
The signer and the verifier are then the *same* project and trust model: cosign writes the
signature, policy-controller reads it, both speak Fulcio/Rekor natively, and the keyless
identity (issuer + subject regexp) is a first-class field rather than something mapped onto
a general rule language. It is the tightest fit for a project whose entire supply-chain
story *is* Sigstore.

A second reason is **division of labour with M8**. The roadmap already earmarks Kyverno for
workload *posture* (require non-root, drop capabilities, require limits/probes). Using
policy-controller for *signature* admission keeps the two engines doing non-overlapping
jobs — image identity here, pod hardening there — instead of two general policy engines
both trying to verify images.

## Rejected alternative

**Kyverno.** This is the stronger general-purpose tool and the more widely recognised one,
and we want to state its case fairly. One engine would cover both signature verification
*and* the M8 posture policies, so the cluster runs one controller, not two. Its
`verifyImages` rule is a single readable block, and `mutateDigest` can pin tags to digests
as a bonus. For most teams "just use Kyverno for everything" is the right call.

We rejected it here because:

- **Ecosystem coherence.** policy-controller is the canonical verifier for cosign
  signatures; keeping sign and verify in one project minimises the surface where a format
  or trust-root assumption can drift between the two.
- **No capability gain.** Kyverno's image verification is built on the *same* `cosign-go`
  libraries, so it shares their limits (see below) — picking Kyverno would not have bought a
  working admit path for our bundle-format signatures. The choice is about fit, not power.
- **Avoid double-duty.** Reserving Kyverno for M8 posture work keeps responsibilities clean
  rather than having two engines that both gate images.

## Consequences

**What we commit to:**

- **Namespaces opt in.** policy-controller enforces only where the label
  `policy.sigstore.dev/include: "true"` is set; the controller's `no-match-policy` is `deny`
  by default, so images matching no policy are rejected too (we set it explicitly to pin
  that). This is deliberately surgical: `argocd`, `raft-kv`, and system
  namespaces are unaffected until opted in, so enabling the policy cannot brick the cluster.
- **Two policy engines eventually.** When M8 adds posture rules via Kyverno, the cluster
  runs both controllers. We accept that for clean separation; a single-engine consolidation
  is possible later if the operational cost outweighs the clarity.

**Bundle-signature limitation + how it's handled (verified 2026-06-27):**

- policy-controller through **v0.15.1** verifies cosign bundle *attestations* but not bundle
  *signatures* yet (`bundle support for image signatures is not yet implemented`), and cosign
  v3 signs in the bundle format by default. Rather than depend on unimplemented upstream
  support, `image.yml` **dual-signs** each image — the modern bundle (for `cosign verify`)
  plus a legacy `.sig` (`--new-bundle-format=false`) — and this policy verifies the legacy
  signature (`signatureFormat: legacy`), which policy-controller supports today. The rejection
  path is demonstrated live; the admit path is correct by construction and confirms on the next
  signed publish (details: [deploy/policy/README.md](../../deploy/policy/README.md)). Because
  Kyverno uses the same `cosign-go` code it would hit the same bundle-signature wall — an
  ecosystem property at this date, not a cost of choosing policy-controller.

## Notes for reviewers

The decision optimises for a coherent Sigstore trust chain on a learning project, not for
the minimum number of running controllers. If this grew into a real platform, standardising
on one general-purpose policy engine (Kyverno or Gatekeeper) for *all* admission concerns —
images and posture — is the expected consolidation, and nothing here forecloses it: the
`ClusterImagePolicy` is a self-contained artefact that a `verifyImages` rule could replace.
