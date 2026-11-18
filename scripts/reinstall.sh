#!/usr/bin/env bash
# ==============================================================================
# reinstall.sh
# Uninstalls previous binary, builds and installs latest.
# Optionally restarts running serve daemon(s) via scripts/restart.sh.
# ==============================================================================
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/Makefile" ]]; then
  PROJECT_ROOT="${SCRIPT_DIR}"
else
  PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi

DO_RESTART=0
RESTART_ARGS=()

usage() {
  cat <<'EOF'
Usage: reinstall.sh [OPTIONS]

Uninstall, build, and reinstall antigravity-account-switcher.

Options:
      --restart           Restart running serve daemon(s) after reinstall
      --serve             Alias for --restart
  -h, --help              Show this help message

When --restart/--serve is set, additional restart.sh flags (--fg, --bg,
--port <port>, --log <path>, --timeout <dur>, -- <serve-args>...) are
forwarded to scripts/restart.sh.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --restart|--serve)
      DO_RESTART=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --fg|--bg|--port=*|--log=*|--timeout=*)
      RESTART_ARGS+=("$1")
      shift
      ;;
    -p|--port|--log|--timeout)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: $1 requires an argument" >&2
        usage >&2
        exit 1
      fi
      RESTART_ARGS+=("$1" "$2")
      shift 2
      ;;
    --)
      RESTART_ARGS+=("$@")
      break
      ;;
    *)
      if [[ "${DO_RESTART}" -eq 1 ]]; then
        RESTART_ARGS+=("$1")
        shift
      else
        echo "Error: unknown option '$1'" >&2
        usage >&2
        exit 1
      fi
      ;;
  esac
done

if [[ "${DO_RESTART}" -eq 0 && "${#RESTART_ARGS[@]}" -gt 0 ]]; then
  echo "Error: restart flags (${RESTART_ARGS[*]}) require --restart or --serve" >&2
  usage >&2
  exit 1
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

if [[ "${DO_RESTART}" -eq 1 ]]; then
  exec "${SCRIPT_DIR}/restart.sh" "${RESTART_ARGS[@]}"
fi
