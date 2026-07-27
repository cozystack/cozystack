# shellcheck shell=sh
# Shared helpers for preflight checks. Sourced, not executed.
#
# Each check under preflight/checks/ sources this and sets PF_CHECK to its own
# id before logging, so every line the runner aggregates is attributable to a
# specific check.

# pf_log <message...> - emit a check-attributed line on stdout.
pf_log() {
  echo "[preflight:${PF_CHECK:-?}] $*"
}
