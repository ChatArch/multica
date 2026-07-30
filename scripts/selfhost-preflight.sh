#!/usr/bin/env bash
# Report self-host port configuration that would otherwise be silently ignored.
#
# The backend host port resolves along one documented chain:
#
#   BACKEND_PORT -> API_PORT -> SERVER_PORT -> PORT -> 8080
#
# and within a single variable the env file beats the shell environment, because
# make `include`s the file after the environment is already in place. So exactly
# one input ever wins. Anything else the user set is inert, and two ways of
# setting a port used to produce nothing at all with no explanation: a
# higher-priority alias shadowing a lower one, and the env file shadowing the
# environment.
#
# This resolves the winner first and then reports only the inputs that the winner
# actually shadows — never a second "wins" claim, and never silence when a
# different alias took precedence. Not fatal, so it warns and continues.
#
# The Makefile passes the pristine values in via SHELL_ENV_* / ENV_FILE_*,
# because after its include the original origins are unrecoverable.

set -euo pipefail

env_file=${ENV_FILE:-.env}
BACKEND_PORT_CHAIN="BACKEND_PORT API_PORT SERVER_PORT PORT"
DEFAULT_BACKEND_PORT=8080

value_of() {
  eval "printf '%s' \"\${$1:-}\""
}

# The env file wins over the shell environment for the same variable.
effective_value() {
  local var=$1 file_value
  file_value=$(value_of "ENV_FILE_${var}")
  if [ -n "$file_value" ]; then
    printf '%s' "$file_value"
    return
  fi
  value_of "SHELL_ENV_${var}"
}

effective_source() {
  local var=$1
  if [ -n "$(value_of "ENV_FILE_${var}")" ]; then
    printf '%s' "$env_file"
  elif [ -n "$(value_of "SHELL_ENV_${var}")" ]; then
    printf 'environment'
  fi
}

# The single winning input.
winner_var=""
winner_value=""
for var in $BACKEND_PORT_CHAIN; do
  candidate=$(effective_value "$var")
  if [ -n "$candidate" ]; then
    winner_var=$var
    winner_value=$candidate
    break
  fi
done
winner_source=""
if [ -n "$winner_var" ]; then
  winner_source=$(effective_source "$winner_var")
else
  winner_value=$DEFAULT_BACKEND_PORT
fi

# Inputs the winner shadows. An input that happens to carry the winning value is
# not reported: it is redundant, but the user still gets the port they asked for.
unused=""
note_unused() {
  unused="${unused}        $1
"
}

is_winning_input() {
  [ "$1" = "$winner_var" ] && [ "$2" = "$winner_source" ]
}

for var in $BACKEND_PORT_CHAIN; do
  file_value=$(value_of "ENV_FILE_${var}")
  if [ -n "$file_value" ] && [ "$file_value" != "$winner_value" ] &&
    ! is_winning_input "$var" "$env_file"; then
    note_unused "${var}=${file_value} (${env_file})"
  fi

  shell_value=$(value_of "SHELL_ENV_${var}")
  if [ -n "$shell_value" ] && [ "$shell_value" != "$winner_value" ] &&
    ! is_winning_input "$var" "environment"; then
    note_unused "${var}=${shell_value} (environment)"
  fi
done

warned=0
warn() {
  if [ "$warned" -eq 0 ]; then
    echo ""
    warned=1
  fi
  echo "$@"
}

if [ -n "$unused" ]; then
  if [ -n "$winner_var" ]; then
    warn "Note: the backend host port resolves to ${winner_value} from ${winner_var} (${winner_source})."
  else
    warn "Note: the backend host port resolves to the ${winner_value} default."
  fi
  warn "      Order: BACKEND_PORT, API_PORT, SERVER_PORT, PORT, then ${DEFAULT_BACKEND_PORT}."
  warn "      Set but unused:"
  printf '%s' "$unused"
  warn "      Keep only the one you mean."
fi

# FRONTEND_PORT has no aliases, so the env file shadowing the environment is its
# only way to be ignored.
frontend_file=$(value_of ENV_FILE_FRONTEND_PORT)
frontend_shell=$(value_of SHELL_ENV_FRONTEND_PORT)
if [ -n "$frontend_shell" ] && [ -n "$frontend_file" ] &&
  [ "$frontend_shell" != "$frontend_file" ]; then
  warn "Note: FRONTEND_PORT=${frontend_shell} from your environment is ignored;"
  warn "      ${env_file} sets FRONTEND_PORT=${frontend_file} and the file wins."
  warn "      Edit ${env_file} to change the port."
fi

if [ "$warned" -eq 1 ]; then
  echo ""
fi
