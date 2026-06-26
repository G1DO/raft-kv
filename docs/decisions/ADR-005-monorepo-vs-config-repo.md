# ADR-005 — Monorepo vs. config-repo split for GitOps

**Status:** Accepted

## Context

Phase D puts the cluster under GitOps with Argo CD: an in-cluster controller
continuously reconciles what is running against a desired state declared in Git.
Today the repo already holds the application source, the Helm chart
([deploy/helm/raft-kv](../../deploy/helm/raft-kv)), and a signed-image supply
chain ([.github/workflows/image.yml](../../.github/workflows/image.yml)). The
open question is *structural*: where do the deploy manifests live relative to the
application code, and therefore what does Argo's `Application` point its
`repoURL`/`path` at.

Two layouts are on the table:

1. **Config-repo split.** Application code in this repo; Kubernetes/Helm manifests
   in a *separate* repo that Argo watches. CI in the app repo builds and signs an
   image, then writes the new image tag into the config repo, which Argo syncs.
2. **Monorepo.** Code and `deploy/` in one repo. Argo points at a `path`
   (`deploy/helm/raft-kv`) inside this same repo at `targetRevision: main`.

## Decision

**Monorepo.** The Argo `Application`
([deploy/argo/application.yaml](../../deploy/argo/application.yaml)) sources
`path: deploy/helm/raft-kv` from this repo at `targetRevision: main`, with
automated `prune` + `selfHeal`. There is no second repository; the chart and the
code it deploys move together in one history.

## Rejected alternative

**Config-repo split.** This is the right industrial choice and we want to state
its strengths plainly. It gives a clean separation of concerns: the deploy
history is not polluted by application commits, so "what is running and when did
it change" is answerable from one short log. It isolates blast radius and RBAC —
people who can change deploys need not be able to change code, and vice versa. It
is the layout most GitOps tooling assumes.

We rejected it because:

- This is a **solo learning project**, not a multi-team platform. The separation
  it buys protects against coordination problems we do not have.
- A split means a second repo to create, mirror, and keep in lockstep, plus the
  CI machinery to write image tags across the repo boundary — pure overhead for
  one maintainer.
- A monorepo lets **one PR change code and its deploy atomically** and be reviewed
  as a single unit. For a teaching artefact, having everything in one place to read
  is a feature, not a smell.

## Consequences

**What we commit to:**

- **Path-filtered CI is the mitigation, and it is uneven by design.** The
  deploy-relevant and code-relevant workflows are scoped so unrelated changes do
  not trigger them: [helm.yml](../../.github/workflows/helm.yml),
  [go.yml](../../.github/workflows/go.yml), and
  [diagrams.yml](../../.github/workflows/diagrams.yml) filter on both pull-request
  and push — a Go-only change skips Helm lint, a chart-only change skips Go tests.
  The security and publish workflows deliberately do *not*:
  [gitleaks.yml](../../.github/workflows/gitleaks.yml) scans every file on every
  event, and the push/tag side of [image.yml](../../.github/workflows/image.yml)
  and [trivy.yml](../../.github/workflows/trivy.yml) builds regardless of which
  files changed (a release must publish whatever the commit is).
- **Auto-sync couples merge to deploy.** Every merge to `main` that touches
  `deploy/helm` is a live change to the cluster the moment Argo reconciles.

**What becomes harder:**

- The deploy history is interleaved with application history in one `git log`;
  answering "when did the deployment last change" means `git log -- deploy/`
  rather than reading a dedicated repo's log.
- A noisy `main` is a noisier reconcile source; we lean on Argo's path-scoped
  `source.path` so only `deploy/helm/raft-kv` changes are material.
- At team scale this layout couples code-merge cadence to deploy cadence and
  shares one RBAC surface — an acknowledged wart, acceptable for one person.

## Notes for reviewers

The config-repo split is the correct answer once more than one team shares the
cluster; we are optimising for a single learner's comprehension and for atomic,
single-PR changes, not for multi-team blast-radius isolation. If this project
ever grew real operators, splitting the deploy manifests into their own repo is
the expected next step, and nothing here forecloses it — the `Application` would
simply repoint its `repoURL`.

One caveat worth flagging because it is easy to over-read: Argo reconciles
Kubernetes objects, not Raft membership. Bumping `replicaCount` and letting Argo
sync grows the StatefulSet, but the new pods do not automatically join the
consensus quorum as voters — driving Raft membership from a StatefulSet is a
known unsolved limitation, tracked separately. Until that is addressed, auto-sync
scaling of this chart is cosmetic for the consensus group, and the GitOps story
here is about *declarative deploys*, not *safe cluster reconfiguration*.
