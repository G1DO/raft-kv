#!/usr/bin/env bash
# Render every docs/diagrams/*.mmd to docs/diagrams/rendered/<name>.svg
# and (re)write docs/diagrams/rendered/.manifest with sha256 of each source.
#
# Determinism comes from the pinned mermaid-cli + the manifest, not from the
# SVG bytes (mermaid-cli embeds non-deterministic IDs).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="${REPO_ROOT}/docs/diagrams"
OUT_DIR="${SRC_DIR}/rendered"
MANIFEST="${OUT_DIR}/.manifest"
MMDC="${REPO_ROOT}/node_modules/.bin/mmdc"

if [[ ! -x "${MMDC}" ]]; then
  echo "mmdc not found at ${MMDC}" >&2
  echo "Run: npm ci  (in ${REPO_ROOT})" >&2
  exit 1
fi

mkdir -p "${OUT_DIR}"

shopt -s nullglob
sources=( "${SRC_DIR}"/*.mmd )
if (( ${#sources[@]} == 0 )); then
  echo "No .mmd files under ${SRC_DIR}; nothing to do."
  : > "${MANIFEST}"
  exit 0
fi

for src in "${sources[@]}"; do
  name="$(basename "${src}" .mmd)"
  out="${OUT_DIR}/${name}.svg"
  echo "render ${name}.mmd -> rendered/${name}.svg"
  "${MMDC}" \
    --input "${src}" \
    --output "${out}" \
    --backgroundColor transparent \
    --quiet
done

tmp="$(mktemp)"
( cd "${SRC_DIR}" && sha256sum *.mmd ) | sort > "${tmp}"
mv "${tmp}" "${MANIFEST}"
echo "wrote ${MANIFEST} (${#sources[@]} entries)"
