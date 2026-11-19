# shellcheck shell=bash
# Sourced by the chainsaw etcd Tests after `cd` to the repo root. Provides:
#   etcd_probe_resolve — recover a throwaway probe Pod's result marker when
#                        `kubectl run --attach` lost the container's stream
# Sourcing has no side effects: it defines a function and nothing else. The
# function itself assigns two `_probe_`-prefixed variables without `local`,
# which is not POSIX and so unavailable to the `sh` Chainsaw runs these steps
# under; the prefix is what keeps them from colliding with a caller's names.
#
# Why this exists. The metrics contract in
# hack/e2e-chainsaw/etcd/chainsaw-test.yaml proves the member Pods serve
# Prometheus metrics over plaintext http by running a one-shot curl Pod and
# reading a `code=<status>` marker off its output. `kubectl run --attach`
# establishes the attach connection only after the Pod reaches Running, and a
# container that exits before that connection is up writes its stdout into the
# void — the caller gets kubectl's own chatter ("If you don't see a command
# prompt, try pressing enter.") and no marker. curl against a Pod IP completes
# in milliseconds, so losing the race is routine, not exotic.
#
# The failure that follows is a false negative: the step reports the metrics
# endpoint as broken on a cluster that was serving 200 throughout. It is also
# non-deterministic in the way that reads as a flake — whether attach wins
# depends on Pod startup timing (CNI interface setup and image pull have each
# been observed adding seconds), so the same suite passes on the next run
# against identical code.
#
# The recovery is that the container's stdout is not actually gone: kubelet
# wrote it to the Pod's log. So a lost stream is re-read rather than believed.
# hack/e2e-chainsaw/_lib/talos-image-cache.sh solves the same race inline for
# the image-cache probe (root cause from the same class of bug); that inline
# copy is what identified this call site as still carrying the race. This is
# the extracted, unit-tested form — see hack/etcd-probe_test.bats. It is not
# yet shared: talos-image-cache.sh still carries its own copy, and folding it
# onto this helper would need a name that is not etcd-specific.
#
# Three conditions make the re-read safe rather than a way to mask real
# failures. The marker encodes the outcome, so a `code=503` recovered from the
# log stays a failure. An attach stream that already carried a marker is never
# second-guessed. And the caller must delete any previous Pod of the same name
# *and wait for it to be gone* before running the probe, then not pass `--rm`:
# without the blocking pre-delete the re-read could find a previous
# incarnation's marker, and `--rm` would destroy the log this recovery reads.
#
# Usage:
#   out=$(etcd_probe_resolve "$attach_output" kubectl -n "$ns" logs "$pod")
#
# Returns the attach output when it carries a marker; otherwise the log re-read
# when that carries one; otherwise the attach output unchanged, so whatever
# kubectl said about the failure survives into the caller's error message. A
# failing re-read (evicted or already-collected Pod) is tolerated rather than
# propagated, so it cannot kill a `set -e` caller before it reports the result.
etcd_probe_resolve() {
  _probe_attach_out="$1"
  shift
  if printf '%s' "$_probe_attach_out" | grep -q 'code='; then
    printf '%s' "$_probe_attach_out"
    return 0
  fi
  _probe_log_out="$("$@" 2>/dev/null || true)"
  if printf '%s' "$_probe_log_out" | grep -q 'code='; then
    printf '%s' "$_probe_log_out"
    return 0
  fi
  printf '%s' "$_probe_attach_out"
}
