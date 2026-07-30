#!/usr/bin/env bash
# Report self-host port configuration that would otherwise be silently ignored.
#
# There is one backend port variable to edit — PORT — plus optional aliases
# (BACKEND_PORT, API_PORT, SERVER_PORT) that override it. Two ways to ask for a
# port and get nothing used to pass unnoticed:
#
#   1. An alias set to a different value than PORT. The alias wins everywhere
#      (Makefile, scripts/local-env.sh, docker-compose.selfhost.yml), so an
#      edited PORT does nothing.
#   2. A value from the shell environment (`PORT=9000 make selfhost`). Make
#      `include`s the env file, and an assignment in an included makefile
#      overrides the environment, so the env file wins.
#
# Neither is fatal, so this warns and continues. The Makefile passes the pristine
# values in via SHELL_ENV_* / ENV_FILE_*, because after its include the original
# origins are unrecoverable.

set -euo pipefail

env_file=${ENV_FILE:-.env}
warned=0

warn() {
  if [ "$warned" -eq 0 ]; then
    echo ""
    warned=1
  fi
  echo "$@"
}

value_of() {
  eval "printf '%s' \"\${$1:-}\""
}

# 1. An alias in the env file silently overriding an edited PORT in the same
# file. Aliases arriving from the shell environment are handled by check 2.
env_file_port=$(value_of ENV_FILE_PORT)
if [ -n "$env_file_port" ]; then
  for alias_name in BACKEND_PORT API_PORT SERVER_PORT; do
    alias_value=$(value_of "ENV_FILE_${alias_name}")
    if [ -n "$alias_value" ] && [ "$alias_value" != "$env_file_port" ]; then
      warn "Note: ${env_file} sets both PORT=${env_file_port} and ${alias_name}=${alias_value}."
      warn "      ${alias_name} wins for the backend, so PORT=${env_file_port} is ignored."
      warn "      Remove one of them to make the intended port unambiguous."
    fi
  done
fi

# 2. A shell environment value the env file overrides.
for var_name in PORT BACKEND_PORT FRONTEND_PORT; do
  shell_value=$(value_of "SHELL_ENV_${var_name}")
  file_value=$(value_of "ENV_FILE_${var_name}")
  if [ -n "$shell_value" ] && [ -n "$file_value" ] && [ "$shell_value" != "$file_value" ]; then
    warn "Note: ${var_name}=${shell_value} from your environment is ignored;"
    warn "      ${env_file} sets ${var_name}=${file_value} and the file wins."
    warn "      Edit ${env_file} to change the port."
  fi
done

if [ "$warned" -eq 1 ]; then
  echo ""
fi
