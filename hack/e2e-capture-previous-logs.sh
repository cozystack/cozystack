#!/bin/sh
# e2e-capture-previous-logs.sh - dump the PREVIOUS container instance's logs for
# every container that has restarted.
#
# DIAGNOSTIC ONLY. This runs on an already-failed test, never mutates the
# cluster, and never changes the test's pass/fail outcome. Every kubectl call is
# individually time-boxed and every failure is swallowed.
#
# Why this exists: when a pod crash-loops, the decisive evidence is almost
# always in the immediately preceding terminated instance -- the interrupted
# bootstrap, the assertion that killed the process, the config error that made
# the entrypoint exit. `kubectl logs <pod> -c <container>` only ever shows the
# CURRENT instance, which for a crash-looping container is the Nth restart: a
# fresh, uninformative replay of the same startup path. That instance is
# reachable only via `--previous`, and once the kubelet garbage-collects that
# dead container the evidence is gone for good.
#
# The Chainsaw suites capture current logs through the built-in `podLogs`
# collector, which has no `previous` option (chainsaw v0.2.15 PodLogs exposes
# only container/tail/selector), so this closes that gap for every suite at once
# from the global catch rather than per-suite. The crust-gather snapshot does
# collect previous.log per container, but that is a whole archive to download
# and `crust-gather serve`; this puts the decisive lines directly in the failed
# job's log, and keeps them even when the snapshot is truncated.
#
# Selection is discovered at runtime, never hardcoded: any container (including
# init containers -- an interrupted bootstrap lives there) whose restartCount is
# greater than zero. Containers that never restarted are skipped, so the common
# healthy case emits nothing at all rather than a page of "previous terminated
# container not found" noise.
#
# Usage: e2e-capture-previous-logs.sh <output-dir> [preferred-namespace]
#
# Environment:
#   COZY_PREVLOG_MAX   max containers to dump (default 12); the overflow count
#                      is logged, never silently dropped. Raising it past ~13
#                      trades one bounded report for another: the callers wrap
#                      this script in a 300s backstop and the worst case is
#                      ~30s for the pod list plus 20s per container, so beyond
#                      that the backstop starts cutting the capture instead of
#                      the cap reporting it. Widen the callers' backstop too.
#   COZY_PREVLOG_TAIL  lines per container (default 200).

# --------------------------------------------------------------------------- #
# Pure, side-effect-free helpers.                                             #
# Each takes text on stdin / in args and emits text -- no kubectl, no globals  #
# -- so hack/capture-previous-logs.bats can source this file (with            #
# E2E_CAPTURE_PREVLOGS_LIB set, see the guard below) and unit-test the         #
# restart filtering, namespace prioritisation and capping against mock input   #
# without a cluster. Keep them above the guard and free of any runtime state.  #
# --------------------------------------------------------------------------- #

# prevlog_filter_restarted: stdin = `ns|pod|container|kind|restartCount` rows
# (one container per line, as emitted by the kubectl go-template in main).
# Emits only the rows whose restartCount parses as an integer greater than
# zero -- i.e. the containers that actually HAVE a previous instance to read.
# A row whose count is empty, non-numeric or zero is dropped: asking for
# `--previous` there is guaranteed to fail and would only add noise.
prevlog_filter_restarted() {
  while IFS='|' read -r ns pod container kind restarts; do
    [ -n "$ns" ] && [ -n "$pod" ] && [ -n "$container" ] || continue
    # Reject anything that is not a bare non-negative integer before comparing.
    # This is not load-bearing for control flow -- `[ "<none>" -gt 0 ]` exits 2,
    # so the `|| continue` below would drop the row regardless. What it buys is
    # silence: without it every malformed row also prints an "Illegal number"
    # line to stderr, straight into the failing job's log.
    case "$restarts" in
      '' | *[!0-9]*) continue ;;
    esac
    [ "$restarts" -gt 0 ] || continue
    printf '%s|%s|%s|%s|%s\n' "$ns" "$pod" "$container" "$kind" "$restarts"
  done
}

# prevlog_prioritize: stdin = filtered rows, $1 = the failing test's namespace.
# Emits the rows whose namespace matches $1 first, then everything else, each
# group keeping its input order. On a broadly degraded cluster the container cap
# below would otherwise be spent on unrelated cozy-* restarts while the pod the
# test actually failed on -- the whole reason we are here -- falls off the end.
# An empty $1 is a no-op passthrough.
prevlog_prioritize() {
  _pp_ns="$1"
  if [ -z "$_pp_ns" ]; then
    cat
    return 0
  fi
  # Buffer once, emit twice: stdin is consumable only once, and the two passes
  # need the same rows.
  _pp_rows=$(cat)
  printf '%s\n' "$_pp_rows" | grep -- "^${_pp_ns}|" || true
  printf '%s\n' "$_pp_rows" | grep -v -- "^${_pp_ns}|" || true
}

# prevlog_cap: stdin = prioritised rows, $1 = max rows to keep. Emits at most $1
# rows. The caller compares the input and output counts to report the overflow;
# this helper stays pure so it can be tested without a cluster.
prevlog_cap() {
  _pc_max="$1"
  case "$_pc_max" in
    '' | *[!0-9]*) _pc_max=12 ;;
  esac
  # A zero cap emits nothing; the caller still reports the full overflow count,
  # so the drop stays visible. Handle it explicitly rather than via `head -n 0`:
  # GNU head treats that as an empty result, but BSD/macOS head rejects it
  # ("illegal line count -- 0"), which would abort a local run.
  if [ "$_pc_max" -eq 0 ]; then
    cat >/dev/null
  else
    head -n "$_pc_max"
  fi
}

# prevlog_logfile_name: build the per-container artifact filename for a row's
# fields. Namespaces, pod names and container names are all DNS labels, so the
# only separator that needs normalising is the `|` we introduced.
prevlog_logfile_name() {
  printf '%s_%s_%s.log\n' "$1" "$2" "$3"
}

# Sourcing guard: hack/capture-previous-logs.bats sets E2E_CAPTURE_PREVLOGS_LIB
# and sources this file purely to reach the helpers above; return before
# touching $1 or running any capture so the unit test never needs a cluster.
# The executing callers (the cozytest.sh failure hook and the Chainsaw global
# catch) never set it, so the guard is a no-op there.
if [ -n "${E2E_CAPTURE_PREVLOGS_LIB:-}" ]; then
  return 0 2>/dev/null
fi

OUT="${1:?Usage: e2e-capture-previous-logs.sh <output-dir> [preferred-namespace]}"
PREFER_NS="${2:-}"

MAX="${COZY_PREVLOG_MAX:-12}"
TAIL="${COZY_PREVLOG_TAIL:-200}"

# printf, not echo: under /bin/sh (dash on the CI image) echo expands backslash
# escapes, so a kubectl message containing a literal \n would split across lines
# and could forge a second "[capture-previous-logs]" verdict line.
log() { printf '%s\n' "[capture-previous-logs] $*"; }

# Sanitise the cap here as well as in prevlog_cap, so the overflow line below
# names the limit that was actually applied rather than echoing back a typo.
case "$MAX" in
  '' | *[!0-9]*)
    log "ignoring malformed COZY_PREVLOG_MAX='$MAX'; using 12"
    MAX=12
    ;;
esac

command -v kubectl >/dev/null 2>&1 || exit 0

# One cluster-wide list call. Both initContainerStatuses and containerStatuses
# are walked: an interrupted bootstrap that leaves the datadir half-written
# lives in an init container, and that is exactly the instance whose log gets
# overwritten by the retry. `range` over an absent field yields nothing on
# kubectl's unstructured objects, so a pod with no statuses yet is skipped
# rather than erroring the template.
# shellcheck disable=SC2016  # Go template syntax; $ns/$pod must not be expanded by the shell.
if ROWS=$(timeout 30 kubectl get pods --all-namespaces -o go-template='
{{- range .items -}}
{{- $ns := .metadata.namespace -}}
{{- $pod := .metadata.name -}}
{{- range .status.initContainerStatuses -}}
{{ $ns }}|{{ $pod }}|{{ .name }}|init|{{ .restartCount }}
{{ end -}}
{{- range .status.containerStatuses -}}
{{ $ns }}|{{ $pod }}|{{ .name }}|container|{{ .restartCount }}
{{ end -}}
{{- end -}}' 2>/dev/null); then
  LIST_RC=0
else
  LIST_RC=$?
  ROWS=''
fi

# An unreachable or degraded API is the state a failed e2e is usually in, and it
# yields exactly the same empty ROWS as a healthy cluster where nothing
# restarted. Distinguish them: reporting "no container has restarted" off a list
# call that never returned asserts a cluster fact this script never observed,
# and would talk a triager out of the crash-loop hypothesis.
if [ "$LIST_RC" -ne 0 ]; then
  log "could not list pods (kubectl exit $LIST_RC); cannot determine whether anything crash-looped"
  exit 0
fi

RESTARTED=$(printf '%s\n' "$ROWS" | prevlog_filter_restarted)
TOTAL=$(printf '%s\n' "$RESTARTED" | grep -c . || true)

if [ "${TOTAL:-0}" -eq 0 ]; then
  # Nothing restarted anywhere: the failure is not a crash-loop, so there is no
  # previous instance to read. Say so once instead of emitting an empty section
  # that reads like a broken capture.
  log "no container has restarted; no previous-instance logs to capture"
  exit 0
fi

SELECTED=$(printf '%s\n' "$RESTARTED" | prevlog_prioritize "$PREFER_NS" | prevlog_cap "$MAX")
KEPT=$(printf '%s\n' "$SELECTED" | grep -c . || true)

mkdir -p "$OUT" 2>/dev/null || true
log "capturing previous-instance logs for $KEPT of $TOTAL restarted container(s) -> $OUT"
if [ "$KEPT" -lt "$TOTAL" ]; then
  # Never truncate silently: a reader who sees 12 dumps must know whether that
  # was all of them. Raise COZY_PREVLOG_MAX to widen.
  log "reached COZY_PREVLOG_MAX=$MAX cap; $((TOTAL - KEPT)) more restarted container(s) NOT captured"
fi

printf '%s\n' "$SELECTED" | while IFS='|' read -r ns pod container kind restarts; do
  [ -n "$ns" ] || continue
  file="$OUT/$(prevlog_logfile_name "$ns" "$pod" "$container")"
  # --timestamps so these lines can be interleaved with the events and the
  # current-instance logs the suite's own podLogs collector emits.
  # kubectl's stderr is the ONLY thing that separates the ways this call can
  # come up empty, so it is captured and then quoted into the log line rather
  # than discarded. It goes to a scratch path outside $OUT: the outer backstop
  # can SIGKILL this script mid-read, and a stray .err file left in the snapshot
  # tree would ship as zero-byte noise in the uploaded artifact.
  err="${TMPDIR:-/tmp}/prevlog-stderr.$$"
  timeout 20 kubectl logs -n "$ns" "$pod" -c "$container" --previous \
      --timestamps --tail="$TAIL" >"$file" 2>"$err"
  rc=$?
  # First line only, trimmed: kubectl's first line carries the reason and the
  # rest is usually usage noise that would bury the surrounding output.
  reason=$(head -n 1 "$err" 2>/dev/null | cut -c1-200)
  rm -f "$err"
  if [ "$rc" -ne 0 ] && [ ! -s "$file" ]; then
    # Nothing landed. Report WHY instead of assuming one cause: exit 124 is our
    # own read timeout (a slow or degraded apiserver), while any other non-zero
    # is kubectl's own refusal. kubectl exits 1 for the kubelet having
    # garbage-collected the dead container, for an RBAC denial and for an
    # unreachable node alike, so the exit code alone separates nothing -- the
    # message is what distinguishes them, and a blanket "kubelet GC" line would
    # send a triager after the wrong problem. One line either way: this must
    # never fail the catch, and the restartCount gate keeps it bounded to
    # containers that really did restart.
    rm -f "$file"
    if [ "$rc" -eq 124 ]; then
      log "timed out after 20s reading previous instance for $ns/$pod [$container]"
    else
      log "no previous instance retrieved for $ns/$pod [$container] (kubectl exit $rc): ${reason:-no message from kubectl}"
    fi
    continue
  fi
  if [ ! -s "$file" ]; then
    # kubectl succeeded but the previous instance wrote nothing. That is itself a
    # finding -- a container OOM-killed or failing exec before its first write
    # looks exactly like this -- so name it rather than leaving a silent gap that
    # reads as "never attempted".
    rm -f "$file"
    log "previous instance for $ns/$pod [$container] produced no output"
    continue
  fi
  if [ "$rc" -ne 0 ]; then
    # The read was cut short, but whatever landed before the cut is still the
    # evidence we came for. Keep it and mark it truncated; deleting a partial
    # capture would throw away the only copy of a log that is already gone from
    # the cluster.
    log "previous-instance log for $ns/$pod [$container] is TRUNCATED (kubectl exit $rc)${reason:+: $reason}"
  fi
  # Echo as well as archive. The archive is for later; the failing job's log is
  # where whoever is triaging the red run is already looking.
  echo "----- previous logs: $ns/$pod [$kind $container] (restarts=$restarts, last $TAIL lines) -----"
  cat "$file"
  echo "----- end $ns/$pod [$container] -----"
done

exit 0
