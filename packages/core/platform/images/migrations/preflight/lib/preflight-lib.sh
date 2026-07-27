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

# kubectl_t <args...> - run kubectl with a hard wall-clock bound so a wedged API
# server or pod cannot hang the pre-upgrade hook indefinitely.
#
# It bounds the call with coreutils `timeout` (present in the migrations image)
# rather than kubectl's own --request-timeout. That flag CANNOT be used here:
# the kubectl pinned in the migrations image mis-handles --request-timeout and
# falls back to the localhost:8080 default endpoint instead of the in-cluster
# ServiceAccount config, which makes every call fail on a perfectly healthy
# cluster (verified on a live cluster). `timeout` wraps kubectl as a separate
# process and passes it no extra flags, so kubectl runs in its normal,
# in-cluster-config-detecting form; if the call hangs, timeout kills it and
# exits non-zero, which each check treats as a query failure.
#
# Where `timeout` is unavailable (host unit tests) it runs kubectl directly; the
# fake kubectl there returns immediately, so no bound is needed.
PREFLIGHT_KUBECTL_TIMEOUT="${PREFLIGHT_KUBECTL_TIMEOUT:-30}"
kubectl_t() {
  if command -v timeout >/dev/null 2>&1; then
    timeout "$PREFLIGHT_KUBECTL_TIMEOUT" kubectl "$@"
  else
    kubectl "$@"
  fi
}
