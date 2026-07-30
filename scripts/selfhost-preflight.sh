#!/usr/bin/env bash
# Report self-host port configuration that would otherwise be silently ignored.
#
# The backend host port resolves along one documented chain:
#
#   BACKEND_PORT -> API_PORT -> SERVER_PORT -> PORT -> 8080
#
# Two rules decide which single input survives:
#
#   * Within one variable the env file beats the shell environment, because make
#     `include`s the file after the environment is already in place. This holds
#     for an explicit empty assignment too: `BACKEND_PORT=` in the file overrides
#     `BACKEND_PORT=9000` from the environment, leaving the variable empty.
#   * Across variables, the first non-empty value in chain order wins. An empty
#     value never wins; resolution continues down the chain.
#
# So existence and value are different things and both matter: `BACKEND_PORT=`
# is a real input that shadows the environment while contributing no port. That
# is why the Makefile passes ENV_FILE_<VAR>_IS_SET alongside ENV_FILE_<VAR> —
# inferring "unset" from an empty value made this script report the shadowed
# environment value as the winner, the exact opposite of what starts up.
#
# Everything the winner shadows is listed. An input carrying the winning value is
# not: redundant, but the user still gets the port they asked for. Not fatal, so
# this warns and continues.

set -euo pipefail

env_file=${ENV_FILE:-.env}
BACKEND_PORT_CHAIN="BACKEND_PORT API_PORT SERVER_PORT PORT"
DEFAULT_BACKEND_PORT=8080
DEFAULT_FRONTEND_PORT=3000

value_of() {
  eval "printf '%s' \"\${$1:-}\""
}

file_defines() {
  [ -n "$(value_of "ENV_FILE_$1_IS_SET")" ]
}

# What the toolchain ends up using for one variable, and where it came from.
# A variable the file defines takes the file's value even when that is empty.
effective_value() {
  if file_defines "$1"; then
    value_of "ENV_FILE_$1"
  else
    value_of "SHELL_ENV_$1"
  fi
}

effective_source() {
  if file_defines "$1"; then
    printf '%s' "$env_file"
  elif [ -n "$(value_of "SHELL_ENV_$1")" ]; then
    printf 'environment'
  fi
}

# The single winning input: first non-empty effective value along the chain.
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

unused=""
note_unused() {
  unused="${unused}        $1
"
}

is_winning_input() {
  [ -n "$winner_var" ] && [ "$1" = "$winner_var" ] && [ "$2" = "$winner_source" ]
}

# Only non-empty inputs are reportable: an empty assignment asks for no port, so
# there is nothing the user wanted that got dropped.
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
    warn "Note: the backend host port resolves to the ${DEFAULT_BACKEND_PORT} default."
  fi
  warn "      Order: BACKEND_PORT, API_PORT, SERVER_PORT, PORT, then ${DEFAULT_BACKEND_PORT}."
  warn "      Set but unused:"
  printf '%s' "$unused"
  warn "      Keep only the one you mean."
fi

# FRONTEND_PORT has no aliases, so the env file shadowing the environment — by a
# value or by an empty assignment that falls back to the default — is its only
# way to be ignored.
frontend_effective=$(effective_value FRONTEND_PORT)
frontend_shell=$(value_of SHELL_ENV_FRONTEND_PORT)
if [ -z "$frontend_effective" ]; then
  frontend_effective=$DEFAULT_FRONTEND_PORT
  frontend_description="the ${DEFAULT_FRONTEND_PORT} default"
else
  frontend_description="${frontend_effective} ($(effective_source FRONTEND_PORT))"
fi
if [ -n "$frontend_shell" ] && [ "$frontend_shell" != "$frontend_effective" ]; then
  warn "Note: FRONTEND_PORT=${frontend_shell} from your environment is ignored;"
  warn "      the frontend host port resolves to ${frontend_description}."
  warn "      Edit ${env_file} to change the port."
fi

if [ "$warned" -eq 1 ]; then
  echo ""
fi
