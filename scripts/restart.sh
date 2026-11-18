#!/usr/bin/env bash
# ==============================================================================
# restart.sh
# Safely stops running antigravity-account-switcher serve instance(s) and
# restarts them while preserving active ports and --bind settings.
# Never targets launch or wrap processes.
# ==============================================================================
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${SCRIPT_DIR}/Makefile" ]]; then
  PROJECT_ROOT="${SCRIPT_DIR}"
else
  PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi

EXPLICIT_PORT=""
MODE="bg"
LOG_FILE="/tmp/antigravity-switcher.log"
TIMEOUT="5s"
EXTRA_ARGS=()

usage() {
  cat <<'EOF'
Usage: restart.sh [OPTIONS] [-- <serve-args>...]

Safely stop running `antigravity-account-switcher serve` daemon(s) and restart
on the same port(s) without disturbing `launch` or `wrap` sessions.

Options:
  -p, --port <port>       Explicit port to stop and restart on
      --bg                Run serve in background (default)
      --fg                Run serve in foreground (exec)
      --log <path>        Log file for background mode (default: /tmp/antigravity-switcher.log)
      --timeout <dur>     Shutdown timeout passed to stop (default: 5s)
  -h, --help              Show this help message
  -- <args...>            Extra arguments forwarded verbatim to serve
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--port)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: $1 requires a port argument" >&2
        exit 1
      fi
      EXPLICIT_PORT="$2"
      shift 2
      ;;
    --port=*)
      EXPLICIT_PORT="${1#--port=}"
      shift
      ;;
    --bg)
      MODE="bg"
      shift
      ;;
    --fg)
      MODE="fg"
      shift
      ;;
    --log)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --log requires a path argument" >&2
        exit 1
      fi
      LOG_FILE="$2"
      shift 2
      ;;
    --log=*)
      LOG_FILE="${1#--log=}"
      shift
      ;;
    --timeout)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "Error: --timeout requires a duration argument" >&2
        exit 1
      fi
      TIMEOUT="$2"
      shift 2
      ;;
    --timeout=*)
      TIMEOUT="${1#--timeout=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      EXTRA_ARGS=("$@")
      break
      ;;
    *)
      echo "Error: unknown option '$1' (use -- to pass extra arguments to serve)" >&2
      usage >&2
      exit 1
      ;;
  esac
done

resolve_binary() {
  if [[ -n "${SWITCHER_BIN:-}" && -x "${SWITCHER_BIN}" ]]; then
    echo "${SWITCHER_BIN}"
    return 0
  fi
  if [[ -x "${HOME}/.local/bin/antigravity-account-switcher" ]]; then
    echo "${HOME}/.local/bin/antigravity-account-switcher"
    return 0
  fi
  if command -v antigravity-account-switcher >/dev/null 2>&1; then
    command -v antigravity-account-switcher
    return 0
  fi
  if [[ -x "${PROJECT_ROOT}/bin/antigravity-account-switcher" ]]; then
    echo "${PROJECT_ROOT}/bin/antigravity-account-switcher"
    return 0
  fi
  return 1
}

BIN_PATH="$(resolve_binary)" || {
  echo "Error: antigravity-account-switcher binary not found" >&2
  exit 1
}

read_config_port() {
  local cfg="${HOME}/.config/antigravity-account-switcher/config.json"
  if [[ -f "${cfg}" ]]; then
    local p
    p="$(grep -oE '"port"[[:space:]]*:[[:space:]]*[0-9]+' "${cfg}" 2>/dev/null | head -n1 | grep -oE '[0-9]+$' || true)"
    if [[ -n "${p}" ]]; then
      echo "${p}"
      return 0
    fi
  fi
  echo "8080"
}

CONFIG_PORT="$(read_config_port)"

probe_status() {
  local port="$1"
  local host="${2:-127.0.0.1}"
  if [[ -z "${host}" || "${host}" == "0.0.0.0" || "${host}" == "::" ]]; then
    host="127.0.0.1"
  fi
  curl -s -m 1 -f "http://${host}:${port}/api/status" >/dev/null 2>&1
}

# Discover running serve processes (strictly excluding launch and wrap).
# Populates SERVE_PIDS, SERVE_PORTS, and PORT_BIND[<port>].
SERVE_PIDS=()
SERVE_PORTS=()
declare -A PORT_BIND=()
declare -A PORT_PID=()
declare -A SEEN_PORTS=()

add_discovered_port() {
  local port="$1"
  if [[ -z "${SEEN_PORTS[${port}]:-}" ]]; then
    SEEN_PORTS["${port}"]=1
    SERVE_PORTS+=("${port}")
  fi
}

scan_serve_processes() {
  if ! command -v ps >/dev/null 2>&1; then
    return 0
  fi

  local pid args
  while read -r pid args; do
    [[ -z "${pid}" || -z "${args}" || "${pid}" == "$$" ]] && continue

    # First token must be the antigravity-account-switcher executable itself
    # (prevents matching wrapper shells like `sh -c ...`).
    local exe="${args%% *}"
    if [[ "${exe}" != *antigravity-account-switcher ]]; then
      continue
    fi

    # Must invoke subcommand `serve` and must NOT be `launch` or `wrap`.
    local is_serve=0 is_other=0
    local port_val="" bind_val="" prev=""
    local tok
    for tok in ${args}; do
      if [[ "${prev}" == "--port" ]]; then
        port_val="${tok}"
        prev=""
        continue
      fi
      if [[ "${prev}" == "--bind" ]]; then
        bind_val="${tok}"
        prev=""
        continue
      fi
      case "${tok}" in
        serve)
          is_serve=1
          ;;
        launch|wrap)
          is_other=1
          ;;
        --port)
          prev="--port"
          ;;
        --port=*)
          port_val="${tok#--port=}"
          ;;
        --bind)
          prev="--bind"
          ;;
        --bind=*)
          bind_val="${tok#--bind=}"
          ;;
      esac
    done

    if [[ "${is_serve}" -eq 1 && "${is_other}" -eq 0 ]]; then
      SERVE_PIDS+=("${pid}")
      local p="${port_val:-${CONFIG_PORT}}"
      add_discovered_port "${p}"
      PORT_PID["${p}"]="${pid}"
      if [[ -n "${bind_val}" ]]; then
        PORT_BIND["${p}"]="${bind_val}"
      fi
    fi
  done < <(ps -eo pid=,args= 2>/dev/null || true)
}

scan_serve_processes

# Determine which ports to stop.
# When --port is explicitly set, only target that port (never touch unrelated ports).
PORTS_TO_STOP=()
if [[ -n "${EXPLICIT_PORT}" ]]; then
  if [[ -n "${SEEN_PORTS[${EXPLICIT_PORT}]:-}" ]] || probe_status "${EXPLICIT_PORT}" "${PORT_BIND[${EXPLICIT_PORT}]:-}"; then
    PORTS_TO_STOP=("${EXPLICIT_PORT}")
  fi
else
  for p in "${CONFIG_PORT}" "1831" "8080"; do
    if [[ -z "${SEEN_PORTS[${p}]:-}" ]] && probe_status "${p}"; then
      add_discovered_port "${p}"
    fi
  done
  PORTS_TO_STOP=("${SERVE_PORTS[@]}")
fi

# Stop every targeted running serve port.
for p in "${PORTS_TO_STOP[@]}"; do
  echo "==> Stopping serve on port ${p}..."
  if ! "${BIN_PATH}" stop --port "${p}" --timeout "${TIMEOUT}" >/dev/null 2>&1; then
    local_pid="${PORT_PID[${p}]:-}"
    if [[ -n "${local_pid}" ]] && kill -0 "${local_pid}" 2>/dev/null; then
      echo "==> Graceful stop timed out; sending SIGTERM to serve PID ${local_pid}..."
      kill -TERM "${local_pid}" 2>/dev/null || true
      for _ in $(seq 1 20); do
        if ! kill -0 "${local_pid}" 2>/dev/null; then
          break
        fi
        sleep 0.1
      done
    fi
  fi
done

# Determine target port(s) to start.
TARGET_PORTS=()
if [[ -n "${EXPLICIT_PORT}" ]]; then
  TARGET_PORTS=("${EXPLICIT_PORT}")
elif [[ "${#SERVE_PORTS[@]}" -gt 0 ]]; then
  TARGET_PORTS=("${SERVE_PORTS[@]}")
else
  TARGET_PORTS=("${CONFIG_PORT}")
fi

has_bind_in_extra() {
  local arg
  for arg in "${EXTRA_ARGS[@]}"; do
    case "${arg}" in
      --bind|--bind=*)
        return 0
        ;;
    esac
  done
  return 1
}

build_serve_args() {
  local port="$1"
  SERVE_CMD_ARGS=("serve" "--port" "${port}")
  if [[ -n "${PORT_BIND[${port}]:-}" ]] && ! has_bind_in_extra; then
    SERVE_CMD_ARGS+=("--bind" "${PORT_BIND[${port}]}")
  fi
  if [[ "${#EXTRA_ARGS[@]}" -gt 0 ]]; then
    SERVE_CMD_ARGS+=("${EXTRA_ARGS[@]}")
  fi
}

wait_for_status() {
  local port="$1"
  local host="${2:-127.0.0.1}"
  local attempts=50
  for _ in $(seq 1 "${attempts}"); do
    if probe_status "${port}" "${host}"; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

if [[ "${MODE}" == "fg" ]]; then
  if [[ "${#TARGET_PORTS[@]}" -gt 1 ]]; then
    echo "Error: multiple serve ports (${TARGET_PORTS[*]}) detected; pass --port <port> with --fg" >&2
    exit 1
  fi
  target_port="${TARGET_PORTS[0]}"
  build_serve_args "${target_port}"
  echo "==> Starting serve in foreground on port ${target_port}..."
  exec "${BIN_PATH}" "${SERVE_CMD_ARGS[@]}"
fi

for target_port in "${TARGET_PORTS[@]}"; do
  build_serve_args "${target_port}"
  echo "==> Starting serve in background on port ${target_port} (log: ${LOG_FILE})..."
  nohup "${BIN_PATH}" "${SERVE_CMD_ARGS[@]}" >> "${LOG_FILE}" 2>&1 </dev/null &
  bg_pid=$!
  disown "${bg_pid}" 2>/dev/null || true
  if wait_for_status "${target_port}" "${PORT_BIND[${target_port}]:-}"; then
    echo "==> serve is healthy on http://127.0.0.1:${target_port} (PID ${bg_pid})"
  else
    echo "Error: serve failed to become healthy on port ${target_port} (check ${LOG_FILE})" >&2
    exit 1
  fi
done
