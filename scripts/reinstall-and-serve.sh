#!/usr/bin/env bash
# ==============================================================================
# reinstall-and-serve.sh
# Uninstalls previous binary, builds and installs latest, then starts serve.
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

echo "==> Starting antigravity-account-switcher serve..."
exec "${BIN_PATH}" serve "$@"
