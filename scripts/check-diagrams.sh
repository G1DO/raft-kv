#!/usr/bin/env bash
# Verify that docs/diagrams/rendered/.manifest matches the current .mmd sources
# AND that every .mmd has a corresponding .svg sibling.
#
# Does NOT invoke mmdc: fast, deterministic, no Chromium required. Suitable
# for CI gates and pre-commit hooks.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="${REPO_ROOT}/docs/diagrams"
OUT_DIR="${SRC_DIR}/rendered"
MANIFEST="${OUT_DIR}/.manifest"

shopt -s nullglob
sources=( "${SRC_DIR}"/*.mmd )

expected="$( cd "${SRC_DIR}" && sha256sum *.mmd 2>/dev/null | sort || true )"
actual="$( [[ -f "${MANIFEST}" ]] && sort "${MANIFEST}" || true )"

if [[ "${expected}" != "${actual}" ]]; then
  echo "ERROR: docs/diagrams/rendered/.manifest is out of date." >&2
  echo "Run: npm run docs:diagrams && git add docs/diagrams/rendered" >&2
  echo >&2
  diff <(printf '%s\n' "${actual}") <(printf '%s\n' "${expected}") >&2 || true
  exit 1
fi

missing=0
for src in "${sources[@]}"; do
  name="$(basename "${src}" .mmd)"
  if [[ ! -f "${OUT_DIR}/${name}.svg" ]]; then
    echo "ERROR: missing ${OUT_DIR}/${name}.svg for ${name}.mmd" >&2
    missing=1
  fi
done
[[ "${missing}" == 0 ]] || exit 1

echo "diagrams: manifest OK (${#sources[@]} sources)"
