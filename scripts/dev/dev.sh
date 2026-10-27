#!/usr/bin/env bash
# Hybrid development entry point. See README.md for the local source workflow.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

compose=(docker compose -f docker-compose.dev.yml)
command_name="${1:-}"
command_option="${2:-}"
dev_log_dir="$repo_root/Logs/dev"
dev_names=()
dev_pids=()
dev_shutting_down=0

load_dotenv() {
  [[ -f "$repo_root/.env" ]] || return 0

  # Import .env for the Python and frontend processes without overriding values
  # explicitly exported by the caller. The Go process also loads this file
  # itself, but exporting it here gives all three services one configuration.
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*# ]] && continue
    line="${line#export }"
    [[ "$line" == *=* ]] || continue

    local key="${line%%=*}"
    local value="${line#*=}"
    key="${key#"${key%%[![:space:]]*}"}"
    key="${key%"${key##*[![:space:]]}"}"
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    [[ -v "$key" ]] && continue

    if [[ "$value" == \"*\" && "$value" == *\" ]]; then
      value="${value:1:${#value}-2}"
    elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
      value="${value:1:${#value}-2}"
    fi
    export "$key=$value"
  done < "$repo_root/.env"
}

require_all_dependencies() {
  local missing=0
  for executable in docker go npm; do
    if ! command -v "$executable" >/dev/null 2>&1; then
      echo "Required command is missing: $executable" >&2
      missing=1
    fi
  done
  if [[ ! -x "$repo_root/.venv/bin/python" ]]; then
    echo "Python virtual environment is missing." >&2
    echo "Run: python3 -m venv .venv && .venv/bin/pip install -r pdf_parser/requirements.txt" >&2
    missing=1
  fi
  if [[ ! -x "$repo_root/web/node_modules/.bin/vite" ]]; then
    echo "Frontend dependencies are missing." >&2
    echo "Run: cd web && npm ci" >&2
    missing=1
  fi
  (( missing == 0 ))
}

require_free_port() {
  local service_name="$1"
  local address="$2"
  local port="${address##*:}"
  if [[ ! "$port" =~ ^[0-9]+$ ]]; then
    echo "Cannot determine $service_name port from: $address" >&2
    return 2
  fi
  if ss -H -ltn "sport = :$port" | grep -q .; then
    echo "$service_name cannot start: port $port is already in use ($address)." >&2
    echo "Stop the existing process or override its address before running make dev." >&2
    return 1
  fi
}

require_development_ports() {
  require_free_port "API" "${HTTP_ADDR:-127.0.0.1:8080}"
  require_free_port "parser" "${PARSER_GRPC_ADDR:-127.0.0.1:50051}"
  require_free_port "frontend" "${VITE_HOST:-127.0.0.1}:${VITE_PORT:-5173}"
}

print_service_urls() {
  local label="$1"
  local address="$2"
  local host="${address%:*}"
  local port="${address##*:}"
  host="${host#[}"
  host="${host%]}"
  if [[ "$host" == "0.0.0.0" || "$host" == "::" || "$host" == "*" ]]; then
    echo "  $label: http://127.0.0.1:$port"
    local ip
    ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}')"
    if [[ -n "$ip" && "$ip" != "127.0.0.1" ]]; then
      echo "         http://$ip:$port"
    fi
    return
  fi
  echo "  $label: http://$host:$port"
}

stop_components() {
  (( dev_shutting_down == 0 )) || return 0
  dev_shutting_down=1
  trap - INT TERM EXIT
  echo
  echo "Stopping API, parser, and frontend..."
  local pid
  for pid in "${dev_pids[@]}"; do
    kill -TERM -- "-$pid" 2>/dev/null || true
  done
  for pid in "${dev_pids[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
  echo "Source services stopped. PostgreSQL and Redis are still running; use 'make dev-stop' to stop them."
}

run_all() {
  require_all_dependencies
  load_dotenv
  require_development_ports
  mkdir -p "$dev_log_dir"

  echo "Starting PostgreSQL and Redis..."
  "${compose[@]}" up -d --wait

  dev_names=()
  dev_pids=()
  dev_shutting_down=0

  start_component() {
    local name="$1"
    shift
    local log_file="$dev_log_dir/$name.log"
    : > "$log_file"
    printf '[%s] log file: %s\n' "$name" "$log_file"
    setsid "$@" > >(
      tee -a "$log_file" | sed -u "s/^/[$name] /"
    ) 2>&1 &
    dev_names+=("$name")
    dev_pids+=("$!")
  }

  trap 'stop_components; exit 130' INT TERM
  trap stop_components EXIT

  start_component api "$repo_root/scripts/dev/dev.sh" app
  start_component parser "$repo_root/scripts/dev/dev.sh" parser
  start_component web "$repo_root/scripts/dev/dev.sh" frontend

  echo
  echo "Development stack is ready:"
  print_service_urls "Web" "${VITE_HOST:-127.0.0.1}:${VITE_PORT:-5173}"
  print_service_urls "API" "${HTTP_ADDR:-127.0.0.1:8080}"
  echo "  Logs: $dev_log_dir/{api,parser,web}.log"
  if [[ "${VITE_HOST:-127.0.0.1}" == "0.0.0.0" ]]; then
    echo "  Note: the dev UI is exposed to other devices; it has no authentication."
  fi
  echo "Press Ctrl+C to stop the three source services."

  while :; do
    local index pid status
    for index in "${!dev_pids[@]}"; do
      pid="${dev_pids[$index]}"
      if ! kill -0 "$pid" 2>/dev/null; then
        status=0
        wait "$pid" || status=$?
        echo "[dev] ${dev_names[$index]} exited with status $status; stopping the remaining services." >&2
        return "$status"
      fi
    done
    sleep 1
  done
}

follow_source_logs() {
  mkdir -p "$dev_log_dir"
  touch "$dev_log_dir/api.log" "$dev_log_dir/parser.log" "$dev_log_dir/web.log"
  if [[ -n "$command_option" ]]; then
    case "$command_option" in
      api|parser|web)
        exec tail -n 100 -F "$dev_log_dir/$command_option.log"
        ;;
      *)
        echo "Unknown source service: $command_option (expected api, parser, or web)" >&2
        return 2
        ;;
    esac
  fi
  exec tail -n 100 -F \
    "$dev_log_dir/api.log" \
    "$dev_log_dir/parser.log" \
    "$dev_log_dir/web.log"
}

case "$command_name" in
  all)
    run_all
    ;;
  start)
    "${compose[@]}" up -d
    ;;
  stop)
    "${compose[@]}" down
    ;;
  restart)
    "${compose[@]}" restart
    ;;
  status)
    "${compose[@]}" ps
    ;;
  logs)
    "${compose[@]}" logs -f
    ;;
  source-logs)
    follow_source_logs
    ;;
  app)
    if [[ "${DEV_AUTO_RELOAD:-true}" != "false" ]] && command -v air >/dev/null 2>&1; then
      exec air -c .air.toml
    fi
    if [[ "${DEV_AUTO_RELOAD:-true}" == "false" ]]; then
      echo "Automatic reload disabled by DEV_AUTO_RELOAD=false." >&2
    else
      echo "Air is not installed; running without automatic reload." >&2
      echo "Install it with: go install github.com/air-verse/air@latest" >&2
    fi
    exec go run ./cmd/server
    ;;
  parser)
    if [[ ! -x .venv/bin/python ]]; then
      echo "Python virtual environment is missing." >&2
      echo "Run: python3 -m venv .venv && .venv/bin/pip install -r pdf_parser/requirements.txt" >&2
      exit 1
    fi
    load_dotenv
    export PYTHONUNBUFFERED=1
    cd pdf_parser
    exec ../.venv/bin/python -m papers_parser.server
    ;;
  frontend)
    cd web
    exec npm run dev -- --host "${VITE_HOST:-127.0.0.1}" --port "${VITE_PORT:-5173}"
    ;;
  *)
    cat >&2 <<'USAGE'
Usage: scripts/dev/dev.sh {all|start|stop|restart|status|logs|source-logs|app|parser|frontend}

start / stop / restart / status / logs operate Docker infrastructure only.
all starts infrastructure and supervises all three host source processes.
source-logs follows persisted logs and accepts an optional api, parser, or web argument.
app, parser, and frontend run source code on the host; app uses Air when available.
USAGE
    exit 2
    ;;
esac
