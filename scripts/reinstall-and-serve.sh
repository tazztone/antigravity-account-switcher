#!/usr/bin/env bash
# ==============================================================================
# reinstall-and-serve.sh
# Uninstalls previous binary, builds and installs latest.
# ==============================================================================
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/Makefile" ]]; then
  PROJECT_ROOT="${SCRIPT_DIR}"
else
  PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi

cd "${PROJECT_ROOT}"

echo "==> Running make uninstall..."
make uninstall

echo "==> Running make install..."
make install

BIN_PATH="${HOME}/.local/bin/antigravity-account-switcher"
if [[ ! -x "${BIN_PATH}" ]] && command -v antigravity-account-switcher >/dev/null 2>&1; then
  BIN_PATH="$(command -v antigravity-account-switcher)"
fi

if [[ -x "${BIN_PATH}" ]]; then
  echo "==> Successfully reinstalled: ${BIN_PATH}"
  "${BIN_PATH}" version
else
  echo "==> Reinstall failed: antigravity-account-switcher binary not found or not executable" >&2
  exit 1
fi
