#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

require_config() {
  local config=$1
  local expected=$2

  if ! grep -Fq "$expected" <<<"$config"; then
    echo "Missing expected docker compose config value:"
    echo "  $expected"
    exit 1
  fi
}

require_env() {
  local output=$1
  local expected=$2

  if ! grep -Fxq "$expected" <<<"$output"; then
    echo "Missing expected derived env value:"
    echo "  $expected"
    echo "Observed:"
    echo "$output"
    exit 1
  fi
}

TMP_PATHS=()
cleanup() {
  if [ ${#TMP_PATHS[@]} -gt 0 ]; then
    rm -rf "${TMP_PATHS[@]}"
  fi
}
trap cleanup EXIT

tmp_env="$(mktemp)"
TMP_PATHS+=("$tmp_env")
sed 's/^FRONTEND_PORT=.*/FRONTEND_PORT=3100/' .env.example >"$tmp_env"
printf '\nBACKEND_PORT=9100\n' >>"$tmp_env"

config="$(
  docker compose \
    --env-file "$tmp_env" \
    -f docker-compose.selfhost.yml \
    config
)"

require_config "$config" 'published: "3100"'
require_config "$config" 'published: "9100"'
require_config "$config" 'FRONTEND_ORIGIN: http://localhost:3100'
require_config "$config" 'GOOGLE_REDIRECT_URI: http://localhost:3100/auth/callback'
require_config "$config" 'MULTICA_APP_URL: http://localhost:3100'

for script in scripts/dev.sh scripts/check.sh; do
  if ! grep -Fq '. scripts/local-env.sh' "$script"; then
    echo "$script must source scripts/local-env.sh for shared local env derivation."
    exit 1
  fi
done

local_env="$(
  env -i PATH="$PATH" bash -c '
    set -euo pipefail
    env_file=$1
    set -a
    # shellcheck disable=SC1090
    . "$env_file"
    set +a
    # shellcheck disable=SC1091
    . scripts/local-env.sh
    printf "%s\n" \
      "PORT=${PORT}" \
      "FRONTEND_PORT=${FRONTEND_PORT}" \
      "FRONTEND_ORIGIN=${FRONTEND_ORIGIN}" \
      "MULTICA_APP_URL=${MULTICA_APP_URL}" \
      "GOOGLE_REDIRECT_URI=${GOOGLE_REDIRECT_URI}" \
      "MULTICA_SERVER_URL=${MULTICA_SERVER_URL}" \
      "LOCAL_UPLOAD_BASE_URL=${LOCAL_UPLOAD_BASE_URL}" \
      "PLAYWRIGHT_BASE_URL=${PLAYWRIGHT_BASE_URL}"
  ' _ "$tmp_env"
)"

require_env "$local_env" 'PORT=9100'
require_env "$local_env" 'FRONTEND_PORT=3100'
require_env "$local_env" 'FRONTEND_ORIGIN=http://localhost:3100'
require_env "$local_env" 'MULTICA_APP_URL=http://localhost:3100'
require_env "$local_env" 'GOOGLE_REDIRECT_URI=http://localhost:3100/auth/callback'
require_env "$local_env" 'MULTICA_SERVER_URL=ws://localhost:9100/ws'
require_env "$local_env" 'LOCAL_UPLOAD_BASE_URL=http://localhost:9100'
require_env "$local_env" 'PLAYWRIGHT_BASE_URL=http://localhost:3100'

# ---------------------------------------------------------------------------
# Host port consistency (regression for #6145)
#
# The health check and the success message must use the port Compose actually
# published, not a port re-derived from the environment. Make and Compose
# disagree on precedence — an assignment in an included makefile (.env) beats a
# real environment variable, while for Compose the environment beats .env — and
# .env.example ships BACKEND_PORT uncommented, so `BACKEND_PORT=9100 make
# selfhost` used to probe 8080 while the stack listened on 9100.
# ---------------------------------------------------------------------------

# Compose accepts PORT as a fallback for BACKEND_PORT, so setting either one in
# .env moves the published host port.
port_only_env="$(mktemp)"
TMP_PATHS+=("$port_only_env")
sed -e 's/^PORT=.*/PORT=9100/' -e 's/^BACKEND_PORT=.*/# BACKEND_PORT=8080/' .env.example >"$port_only_env"

port_only_config="$(
  docker compose \
    --env-file "$port_only_env" \
    -f docker-compose.selfhost.yml \
    config
)"
require_config "$port_only_config" 'published: "9100"'
# The container port must stay fixed regardless of the host port.
require_config "$port_only_config" 'PORT: "8080"'
require_config "$port_only_config" 'target: 8080'

# The recipes must delegate to the shared script instead of re-deriving the port.
for target_line in 'bash scripts/selfhost-wait.sh official' 'bash scripts/selfhost-wait.sh build'; do
  if ! grep -Fq "$target_line" Makefile; then
    echo "Makefile must call the shared wait script: $target_line"
    exit 1
  fi
done
if grep -n 'localhost:$${PORT' Makefile; then
  echo "The self-host recipes must not re-derive the backend host port from \$PORT."
  echo "Use scripts/selfhost-wait.sh, which reads the published port from Compose."
  exit 1
fi

# Stubbed docker/curl so the wait script can be exercised without a real stack.
stub_dir="$(mktemp -d)"
TMP_PATHS+=("$stub_dir")

cat >"$stub_dir/docker" <<'STUB'
#!/usr/bin/env bash
# Minimal `docker compose` stub: answers `port <service> <container port>` from
# STUB_PUBLISHED_*, reports a supported Compose version, succeeds otherwise.
set -euo pipefail
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  if [ "${args[i]}" = "port" ]; then
    if [ -n "${STUB_PORT_FAIL:-}" ]; then
      exit 1
    fi
    case "${args[i + 1]:-}" in
    backend) printf '127.0.0.1:%s\n' "${STUB_PUBLISHED_BACKEND:-8080}" ;;
    frontend) printf '127.0.0.1:%s\n' "${STUB_PUBLISHED_FRONTEND:-3000}" ;;
    *) exit 1 ;;
    esac
    exit 0
  fi
done
if [ "${args[1]:-}" = "version" ]; then
  echo "2.30.0"
fi
exit 0
STUB

cat >"$stub_dir/curl" <<'STUB'
#!/usr/bin/env bash
# Records probed URLs and always reports a healthy backend.
set -euo pipefail
for arg in "$@"; do
  case "$arg" in
  http*) printf '%s\n' "$arg" >>"${STUB_CURL_LOG:-/dev/null}" ;;
  esac
done
exit 0
STUB

chmod +x "$stub_dir/docker" "$stub_dir/curl"

curl_log="$(mktemp)"
TMP_PATHS+=("$curl_log")

# Runs scripts/selfhost-wait.sh with the stubs. Extra args are env assignments.
run_wait() {
  local mode=$1
  shift
  : >"$curl_log"
  env PATH="$stub_dir:$PATH" STUB_CURL_LOG="$curl_log" "$@" \
    bash scripts/selfhost-wait.sh "$mode"
}

require_output() {
  local label=$1 output=$2 expected=$3

  if ! grep -Fq "$expected" <<<"$output"; then
    echo "[$label] missing expected line:"
    echo "  $expected"
    echo "Observed:"
    echo "$output"
    exit 1
  fi
}

# The reported bug: .env pins BACKEND_PORT=8080 for the recipe while Compose
# published 9100 from the environment. Compose is the authority.
wait_output="$(run_wait official STUB_PUBLISHED_BACKEND=9100 BACKEND_PORT=8080 PORT=8080)"
require_output 'env-shadowed backend port' "$wait_output" 'Backend:  http://localhost:9100'
require_output 'env-shadowed backend port' "$wait_output" '✓ Multica is running!'
require_env "$(cat "$curl_log")" 'http://localhost:9100/health'

# Same inversion on the frontend host port, which only affects the printed URL.
wait_output="$(run_wait official STUB_PUBLISHED_FRONTEND=3100 FRONTEND_PORT=3000)"
require_output 'env-shadowed frontend port' "$wait_output" 'Frontend: http://localhost:3100'

# A command-line override (`make selfhost PORT=8080`) must not win either.
wait_output="$(run_wait official STUB_PUBLISHED_BACKEND=9100 PORT=8080)"
require_output 'command-line PORT override' "$wait_output" 'Backend:  http://localhost:9100'

# Defaults stay 8080/3000.
wait_output="$(run_wait official)"
require_output 'defaults' "$wait_output" 'Backend:  http://localhost:8080'
require_output 'defaults' "$wait_output" 'Frontend: http://localhost:3000'
require_env "$(cat "$curl_log")" 'http://localhost:8080/health'

# When Compose cannot report the port (stack failed to come up) fall back to the
# same chain docker-compose.selfhost.yml uses instead of breaking.
wait_output="$(run_wait official STUB_PORT_FAIL=1 BACKEND_PORT=9100)"
require_output 'fallback via BACKEND_PORT' "$wait_output" 'Backend:  http://localhost:9100'
wait_output="$(run_wait official STUB_PORT_FAIL=1 PORT=9100)"
require_output 'fallback via PORT' "$wait_output" 'Backend:  http://localhost:9100'

# Build mode reports the locally built images.
wait_output="$(run_wait build STUB_PUBLISHED_BACKEND=9100)"
require_output 'build mode' "$wait_output" 'Local tags: multica-backend:dev and multica-web:dev.'
require_output 'build mode' "$wait_output" 'Backend:  http://localhost:9100'

# End to end through the real recipe, in a throwaway copy so the repo keeps its
# own .env untouched.
recipe_dir="$(mktemp -d)"
TMP_PATHS+=("$recipe_dir")
cp Makefile .env.example docker-compose.selfhost.yml docker-compose.selfhost.build.yml "$recipe_dir/"
mkdir -p "$recipe_dir/scripts"
cp scripts/selfhost-wait.sh "$recipe_dir/scripts/"
cp .env.example "$recipe_dir/.env"

recipe_output="$(
  cd "$recipe_dir" &&
    env PATH="$stub_dir:$PATH" \
      STUB_PUBLISHED_BACKEND=9100 \
      STUB_PUBLISHED_FRONTEND=3100 \
      BACKEND_PORT=9100 \
      FRONTEND_PORT=3100 \
      make selfhost 2>&1
)"
require_output 'make selfhost recipe' "$recipe_output" 'Backend:  http://localhost:9100'
require_output 'make selfhost recipe' "$recipe_output" 'Frontend: http://localhost:3100'

echo "self-host env derivation ok"
