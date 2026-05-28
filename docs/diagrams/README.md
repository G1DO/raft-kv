# Diagrams

Mermaid diagram sources live here as `*.mmd`. The rendered SVGs live in
[`rendered/`](rendered/) and are committed alongside their sources. Docs
under `docs/` reference diagrams as `![alt](diagrams/rendered/name.svg)`
image links — **never** as inline ` ```mermaid ` blocks. The CI gate
([.github/workflows/diagrams.yml](../../.github/workflows/diagrams.yml))
fails any PR that edits a `.mmd` without re-rendering.

## Authoring

1. Create or edit `docs/diagrams/<name>.mmd`. Keep the diagram source the
   *single source of truth* — comments at the top of each file should
   explain what it depicts and which doc references it.
2. Render locally:
   ```
   npm ci          # first time only — installs the pinned mermaid-cli
   npm run docs:diagrams
   ```
3. Commit the `.mmd`, the corresponding `rendered/<name>.svg`, and the
   updated `rendered/.manifest` together. Splitting them across commits
   makes the CI gate fail.

## How drift is prevented

[`rendered/.manifest`](rendered/) holds `sha256(name.mmd) name.mmd` lines,
one per source. [`scripts/check-diagrams.sh`](../../scripts/check-diagrams.sh)
recomputes those hashes from the live `.mmd` files and fails if anything
differs, or if any `.mmd` is missing its `.svg` sibling. The check does
**not** re-invoke `mmdc` — mermaid-cli's SVG output embeds non-deterministic
element IDs, so a byte-level re-render diff would be flaky even with a
pinned version.

`scripts/render-diagrams.sh` is the only thing that writes the manifest;
it always rewrites it atomically from the current set of sources.

## System dependencies (Ubuntu 24.04 / Noble)

`@mermaid-js/mermaid-cli` is pinned to **11.4.0** in [package.json](../../package.json);
it pulls Puppeteer, which needs a Chromium-class browser plus a handful of
shared libraries. On Ubuntu 24.04 the package names have the `t64` ABI
suffix — older mermaid-cli setup guides reference the pre-transition names
(`libasound2`, `libgtk-3-0`) which no longer exist on Noble.

```
sudo apt-get install -y \
  libnss3 libxkbcommon0 libgbm1 libasound2t64 \
  libatk-bridge2.0-0 libgtk-3-0t64
```

[`.puppeteerrc.cjs`](../../.puppeteerrc.cjs) prefers a system Chromium
(`/usr/bin/chromium`, `/usr/bin/google-chrome`, …) when one is present
and skips the bundled Chromium download. If no system browser is found it
falls back to Puppeteer's bundled download (~170 MB into `node_modules/`).

On CI, the GitHub Actions workflow does not run `mmdc` at all — the
manifest check is pure `sha256sum` and `diff`, so no browser is required.

## Existing diagrams

| Source | Rendered | Referenced from |
|---|---|---|
| [architecture.mmd](architecture.mmd) | [rendered/architecture.svg](rendered/architecture.svg) | [docs/design.md](../design.md) |
| [trust-boundary.mmd](trust-boundary.mmd) | [rendered/trust-boundary.svg](rendered/trust-boundary.svg) | [docs/threat-model.md](../threat-model.md) |
