#!/bin/sh
# e2e-capture-previous-logs.sh - dump the PREVIOUS container instance's logs for
# every container that has restarted.
#
# DIAGNOSTIC ONLY. This runs on an already-failed test, never mutates the
# cluster, and never changes the test's pass/fail outcome. Every failure is
# swallowed, and every kubectl call is individually time-boxed when `timeout` is
# on PATH -- when it is not, the reads run unbounded rather than not at all, a
# line says so, and the caller's outer backstop is what bounds them.
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
# collector, which has no `previous` option in chainsaw v0.2.15, so this
# closes that gap for every suite at once
# from the global catch rather than per-suite. The crust-gather snapshot does
# collect previous.log per container, but that is a whole archive to download
# and `crust-gather serve`; this puts the decisive lines directly in the failed
# job's log, and keeps them even when the snapshot is truncated.
#
# Selection is discovered at runtime, never hardcoded: any container (including
# init containers -- an interrupted bootstrap lives there) whose restartCount is
# greater than zero. Containers that never restarted are skipped, so the common
# healthy case costs one line saying nothing restarted, rather than a page of
# "previous terminated container not found" noise. That one line is written, not
# omitted: an absent capture-notes.txt is indistinguishable from a capture that
# never ran.
#
# Ordering, where the cap binds: the failing test's namespace first, and within
# each namespace group the most recent restart first (prevlog_sort_recent). The
# restarts alive on a cluster at any moment span days, and install-time restarts
# have a head start on every app suite that runs afterwards, so without a time
# key the cap was routinely spent on containers that had already stopped
# restarting before the failing test began. Nothing is dropped for being old --
# an install-time restart is the explanation when the install is what broke.
#
# Usage: e2e-capture-previous-logs.sh <output-dir> [preferred-namespace]
#
# Environment:
#   COZY_PREVLOG_MAX   max containers to dump (default 12); the overflow count
#                      goes to the job log and to <output-dir>/capture-notes.txt,
#                      never silently dropped. Raising it past ~13
#                      trades one bounded report for another: the callers wrap
#                      this script in a 300s backstop and the worst case is
#                      ~30s for the pod list plus 20s per container, so beyond
#                      that the backstop starts cutting the capture instead of
#                      the cap reporting it. Widen the callers' backstop too.
#   COZY_PREVLOG_TAIL  lines per container (default 200). The bound each capture
#                      was written under is named in capture-notes.txt beside the
#                      logs, not inside them: --tail drops the OLDEST lines and
#                      nothing in the output says so, but a log read in full is a
#                      complete artifact and a trailing prose line breaks every
#                      parser that could read it before. -1 asks for
#                      the whole log and suppresses that note, since no bound was
#                      applied. A malformed value, a zero, or a leading zero falls
#                      back to 200 and says so in capture-notes.txt: --tail=0 asks
#                      kubectl for nothing, which this script would otherwise
#                      report as the container having logged nothing, and a leading
#                      zero is octal to `$(( ))` and decimal to `[`.

# --------------------------------------------------------------------------- #
# Pure, side-effect-free helpers.                                             #
# Each takes text on stdin / in args and emits text -- no kubectl, no globals  #
# -- so hack/capture-previous-logs.bats can source this file (with            #
# E2E_CAPTURE_PREVLOGS_LIB set, see the guard below) and unit-test the         #
# restart filtering, namespace prioritisation, recency ordering and capping     #
# against mock input                                                           #
# without a cluster. Keep them above the guard and free of any runtime state.  #
# --------------------------------------------------------------------------- #

# prevlog_filter_restarted: stdin = `ns|pod|container|kind|restartCount|finishedAt`
# rows (one container per line, as emitted by the kubectl go-template in main).
# finishedAt is when the PREVIOUS instance terminated, and may be empty -- see
# prevlog_sort_recent.
# Emits only the rows whose restartCount parses as an integer greater than
# zero -- i.e. the containers that actually HAVE a previous instance to read.
# A row whose count is empty, non-numeric or zero is dropped: asking for
# `--previous` there is guaranteed to fail and would only add noise.
prevlog_filter_restarted() {
  while IFS='|' read -r ns pod container kind restarts finished; do
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
    printf '%s|%s|%s|%s|%s|%s\n' "$ns" "$pod" "$container" "$kind" "$restarts" "$finished"
  done
}

# prevlog_sort_recent: stdin = rows, stdout = the same rows, newest previous
# instance first, rows carrying no timestamp last, ties left in input order.
#
# Why this exists. prevlog_prioritize below keeps the cap from being spent on
# containers unrelated to the failing test, and until now "unrelated" meant only
# "in another namespace". Time was not part of it, so a container that restarted
# once during cluster bring-up competed on equal footing with one that
# crash-looped during the test -- and won whenever the list happened to reach it
# first. Measured on a live stand, the restarts present at any moment span days
# (a nine-day spread, measured on a development cluster), and in CI the
# install-time restarts have that head start: they exist before the first app
# suite runs, so every failed test afterwards pays for them. A restart that
# finished before the test began cannot explain the test's failure.
#
# Ordering, not filtering, and deliberately so: an install-time restart IS the
# explanation when the install is what broke, and this capture has no reference
# point for "when the test began" that would make a cutoff honest. Sorting costs
# nothing when the cap does not bind and picks the right dozen when it does.
#
# Lexicographic sort over RFC3339 UTC is chronological: kubectl renders these as
# fixed-width Z-suffixed strings, so no date parsing is needed. Reverse puts the
# newest first and, because the empty string sorts below every timestamp, leaves
# the rows whose lastState has already been dropped by the kubelet at the end.
# -s keeps equal timestamps in input order, which a node reboot produces in
# bulk; without it the order of a dozen simultaneous restarts would vary between
# runs. Rows with no timestamp at all therefore pass through untouched, which is
# also what keeps this a no-op for callers that pass 5-field rows.
prevlog_sort_recent() {
  # LC_ALL=C because the key is a fixed-format ASCII timestamp: collation buys
  # nothing here and a locale that reorders punctuation could only surprise.
  #
  # Falls back to the input order rather than to nothing. The caller compares
  # row counts to report the cap, so a sort that died would otherwise be
  # indistinguishable from a cap overflow -- it would report "0 of N captured,
  # N more not captured" and send the reader looking for a cap that never
  # fired. Ordering is an optimisation over which dozen rows to keep; losing it
  # must not lose the rows. `sort -s` and the per-key `r` are both extensions
  # (GNU, BSD and busybox all carry them, which is why this is a fallback and
  # not a reimplementation), so this is the branch a stricter POSIX sort takes.
  # Anything in the timestamp column that is not shaped like one is treated as
  # undated. Two reasons, and the second is why this is not redundant with the
  # template guard above it: `<no value>` sorts ABOVE every digit, so a single
  # such row would take the front of the queue from the newest real restart;
  # and the header prints this field verbatim, so a non-timestamp there becomes
  # a claim about a time nothing observed. Normalising to empty puts the row
  # last and prints nothing, which are both already handled paths.
  #
  # Only when the field exists: setting $6 on a five-field row would append a
  # separator and change its shape. The pattern is spelled out character by
  # character rather than with an interval like {4}, which older mawk does not
  # support and Ubuntu's default awk is mawk.
  _psr_rows=$(awk -F'|' -v OFS='|' \
    '{ if (NF >= 6 && $6 !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T/) $6 = ""; print }')
  [ -n "$_psr_rows" ] || return 0
  if _psr_out=$(printf '%s\n' "$_psr_rows" | LC_ALL=C sort -s -t'|' -k6,6r 2>/dev/null); then
    printf '%s\n' "$_psr_out"
  else
    echo "[capture-previous-logs] sort unavailable for recency ordering; keeping discovery order" >&2
    printf '%s\n' "$_psr_rows"
  fi
}

# prevlog_prioritize: stdin = filtered rows, $1 = the failing test's namespace.
# Emits the rows whose namespace matches $1 first, then everything else, each
# group ordered newest-restart-first by prevlog_sort_recent. On a broadly
# degraded cluster the container cap below would otherwise be spent on unrelated
# cozy-* restarts while the pod the test actually failed on -- the whole reason
# we are here -- falls off the end. Namespace stays the primary key and recency
# only breaks ties within a group: a stale restart in the failing test's own
# namespace is still likelier to be relevant than a fresh one three namespaces
# away. An empty $1 orders the whole input by recency.
prevlog_prioritize() {
  _pp_ns="$1"
  if [ -z "$_pp_ns" ]; then
    prevlog_sort_recent
    return 0
  fi
  # Buffer once, emit twice: stdin is consumable only once, and the two passes
  # need the same rows.
  # Compared, not matched: the namespace arrives from the caller and would otherwise
  # be a basic regular expression, so a `.` in it would match namespaces it does not
  # name. Field-scoped, so a pod merely named like the prefix is not promoted.
  #
  # Smaller residual, not none: `awk -v` expands backslash escapes in the value it
  # assigns, so a namespace containing `\t` would arrive as a real tab. Namespaces
  # are DNS labels, so it cannot happen here -- but the class is narrowed rather
  # than closed, and a comment claiming otherwise is the kind of thing a later
  # reader trusts instead of rechecking.
  _pp_rows=$(cat)
  printf '%s\n' "$_pp_rows" | awk -F'|' -v p="$_pp_ns" '$1 == p' | prevlog_sort_recent || true
  printf '%s\n' "$_pp_rows" | awk -F'|' -v p="$_pp_ns" 'NF && $1 != p' | prevlog_sort_recent || true
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

# prevlog_append_note <file> <text>
#
# Append a `[capture-previous-logs]` line, starting a new line first when the file
# does not already end in one. Mirrors cozyreport_append_note in hack/cozyreport.sh,
# and for the same reason: these reads are `timeout -k`-killable mid-stream, so a
# partial final line is the normal shape of a truncated capture, not an edge case.
# Appending blind glues the marker onto that line -- the last log line, which is
# the decisive one for a truncated read, is corrupted, a grep for the marker at
# start-of-line misses it, and in reverse a container whose own final write ended
# mid-sentence produces a line that reads as if the container emitted the marker
# itself.
prevlog_append_note() {
  _pan_file=$1
  _pan_text=$2
  if [ -s "$_pan_file" ] && [ -n "$(tail -c 1 "$_pan_file" 2>/dev/null)" ]; then
    printf '\n' >> "$_pan_file" || true
  fi
  printf '%s\n' "$_pan_text" >> "$_pan_file" || true
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

# The per-read budget, named once so the message that reports a timeout cannot
# quote a number the read never used. The grace counts towards the ceiling, so
# 18 + 2 is the 20s the worst-case arithmetic in the header, hack/cozytest.sh and
# .chainsaw.yaml is written against.
PREVLOG_READ_TIMEOUT=18
# The cluster-wide list gets its own, larger bound: one call against every
# namespace, where the per-container reads below are one container each.
PREVLOG_LIST_TIMEOUT=28
PREVLOG_READ_GRACE=2

# The wall-clock wrapper, resolved once. Empty when `timeout` is absent: the reads
# then run unbounded and a note says so, rather than every call exiting 127 and the
# run reporting that kubectl failed when kubectl never ran.
if command -v timeout >/dev/null 2>&1; then
  PREVLOG_BOUND="timeout -k $PREVLOG_READ_GRACE"
  PREVLOG_LIST_BOUND="timeout -k $PREVLOG_READ_GRACE $PREVLOG_LIST_TIMEOUT"
else
  PREVLOG_BOUND=""
  PREVLOG_LIST_BOUND=""
fi

# prevlog_cutoff_desc <exit-status> <seconds> <bound>: how a 124/137 should be
# described. The bound is passed in rather than read from PREVLOG_BOUND: the list
# read is wrapped by a different one, and two variables set in a single if/else
# agree only until something sets one of them alone.
#
# 124 is `timeout` and only `timeout`: the deadline passed and nothing else
# produces it. 137 is 128+SIGKILL, which our own `-k` grace produces and so does
# anything else that SIGKILLs the read -- the OOM killer on a loaded runner, a
# teardown signalling the process group. Both belong in the cut-off branch, since
# either way the capture ends before the log did; but "timed out after 30s" on a
# 137 states a cause this script never observed, and a read killed at second two
# reads as one that ran the full thirty. Same distinction cozyreport.sh draws, and
# for the same reason: these files are read by someone deciding what to look at
# next.
prevlog_cutoff_desc() {
  if [ -z "$3" ]; then
    printf '%s' "a signal from outside this script, which ran its reads unbounded"
  elif [ "${1:-}" = "137" ]; then
    printf '%s' "a SIGKILL -- the kill grace of its own ${2}s timeout, or something else killing the read, which 137 does not tell apart"
  else
    printf '%s' "its own ${2}s timeout"
  fi
}

# Create the output directory before the first log line, not just before the
# first capture: every line below explains why the capture is smaller than the
# cluster, and until the directory exists there is nowhere to write it.
mkdir -p "$OUT" 2>/dev/null || true
NOTES="$OUT/capture-notes.txt"
# Appended, not truncated: a caller may aim two runs at one directory, and the
# earlier run's reasons are as load-bearing as the later one's. Separate them, or
# a "no container has restarted" from attempt one sits next to a "reached the
# cap" from attempt two and the file stops being able to explain anything.
if [ -s "$NOTES" ]; then
  printf -- '--- new capture run ---\n' >> "$NOTES" 2>/dev/null || true
fi

# printf, not echo: under /bin/sh (dash on the CI image) echo expands backslash
# escapes, so a kubectl message containing a literal \n would split across lines
# and could forge a second "[capture-previous-logs]" verdict line.
#
# Every line goes to the artifact as well as to the job log. The job log is
# where a triager looking at a red run is already standing, but it is also the
# thing that expires, gets truncated by the runner, and is unreachable to anyone
# reading the uploaded report weeks later. That reader sees N dumps in a
# directory and no way to tell N from "all of them" -- the cap that dropped 21
# containers, the pod list that never returned, the container whose previous
# instance the kubelet had already collected, all invisible. Absent data that
# looks like absent problems is the failure mode this capture exists to prevent,
# so the reasons ship next to the logs they explain.
log() {
  printf '%s\n' "[capture-previous-logs] $*"
  printf '%s\n' "[capture-previous-logs] $*" >> "$NOTES" 2>/dev/null || true
}

# The output directory arrives as an argument, so it can be unusable for reasons
# that have nothing to do with the cluster: a parent that is a file, a path that
# does not exist and cannot be created, a directory this process may not write.
# Probed once here rather than left to each `> "$file"`, because a redirection
# that cannot open its target fails BEFORE kubectl runs and hands the shell's own
# status to the branch below -- which then reports "no previous instance
# retrieved (kubectl exit 2)", once per container: a local condition stated as a
# fact about the cluster, which is the mistake this script exists to stop making.
#
# Tested with `-d`/`-w` rather than by creating a probe file: a run killed between
# writing that file and removing it would ship a stray dotfile inside the
# uploaded artifact. A full filesystem slips through this check, but that is the
# case where the note explaining it could not be written either.
if [ ! -d "$OUT" ] || [ ! -w "$OUT" ]; then
  log "cannot write to $OUT, so no previous-instance log can be kept; this is a condition of the machine writing the report and says nothing about whether anything crash-looped"
  exit 0
fi

# Sanitise the cap here as well as in prevlog_cap, so the overflow line below
# names the limit that was actually applied rather than echoing back a typo.
# The nine-digit bound matters as much as the digits-only one, and for the reason
# the sibling's cozyreport_is_count spells out: a value `test` and `head` cannot
# compute with is not merely useless, it takes the rows with it. Under dash a
# 30-digit cap makes `[ "$_pc_max" -eq 0 ]` fail with "Illegal number" and `head`
# reject the count, so prevlog_cap emits nothing -- and the caller then reports
# that zero containers were kept because the cap was reached. The cap that broke
# collection gets named as the reason collection found nothing, which is a
# statement about the cluster produced by a typo.
case "$MAX" in
  '' | *[!0-9]*)
    log "ignoring malformed COZY_PREVLOG_MAX='$MAX'; using 12"
    MAX=12
    ;;
  *)
    if [ "${#MAX}" -gt 9 ]; then
      log "ignoring COZY_PREVLOG_MAX='$MAX' (too large for this shell to compute with); using 12"
      MAX=12
    fi
    ;;
esac

# Same treatment for the tail, and 0 is the value that makes it necessary rather
# than tidy. `--tail=0` returns no lines at all, kubectl still exits 0, and the
# empty-output branch below then deletes the capture and logs "previous instance
# ... produced no output" -- a positive claim about the cluster manufactured
# entirely by a typo, which also throws away the only remaining copy of a log the
# kubelet has already collected. A leading zero is rejected because `$(( ))`
# reads it as octal while `[` reads it as decimal, so one string means two
# numbers. -1 is kept: that is the documented
# spelling for "the whole log", and kubectl takes it.
case "$TAIL" in
  -1) ;;
  '' | *[!0-9]*)
    log "ignoring malformed COZY_PREVLOG_TAIL='$TAIL'; using 200"
    TAIL=200
    ;;
  0 | 0*)
    log "ignoring COZY_PREVLOG_TAIL='$TAIL' (a zero or leading-zero tail requests nothing, or is read as octal); using 200"
    TAIL=200
    ;;
  *)
    # Same nine-digit bound as the cap above. kubectl rejects an oversized --tail
    # outright, which would turn every capture into a refusal attributed to the
    # cluster rather than to the value.
    if [ "${#TAIL}" -gt 9 ]; then
      log "ignoring COZY_PREVLOG_TAIL='$TAIL' (too large for this shell to compute with); using 200"
      TAIL=200
    fi
    ;;
esac

if [ -z "$PREVLOG_BOUND" ]; then
  log "timeout is not on PATH, so the reads below ran with no wall-clock ceiling; this is a missing dependency on the machine producing the report, not a statement about the cluster"
fi

if ! command -v kubectl >/dev/null 2>&1; then
  # Leaving silently would put an empty directory in the artifact, which is the
  # one thing this script's notes exist to prevent: it reads as "looked, found
  # no crash-loops" rather than "never looked". One caller pre-gates on kubectl,
  # the other does not.
  log "kubectl is not on PATH; no previous-instance logs were captured"
  exit 0
fi

# The finishedAt guard is three deep, and the innermost one is the load-bearing
# one: `finishedAt` is a metav1.Time behind `json:",omitempty"`, which cannot
# omit a struct, and its zero value marshals to `null`. The kubelet does
# synthesize a terminated state with no time -- ContainerStatusUnknown on a pod
# deleted mid-run, which also bumps restartCount and so clears the filter above.
# kubectl renders that leaf as the literal string `<no value>`, and `<` sorts
# ABOVE every digit, so under the reverse sort that row would land FIRST and
# displace the most recent real restart: the exact inversion this ordering
# exists to prevent. It would also print `previous instance ended <no value>`,
# a claim about a time nothing observed. Guarding the leaf collapses both
# shapes -- null, and the key absent -- into the empty-timestamp path that
# sorts last. Covered by the `<no value>` cases in hack/capture-previous-logs.bats.
#
# One cluster-wide list call. Both initContainerStatuses and containerStatuses
# are walked: an interrupted bootstrap that leaves the datadir half-written
# lives in an init container, and that is exactly the instance whose log gets
# overwritten by the retry. `range` over an absent field yields nothing on
# kubectl's unstructured objects, so a pod with no statuses yet is skipped
# rather than erroring the template.
# kubectl's own reason for not answering is the finding, so it is captured rather
# than sent to /dev/null: "could not list pods (kubectl exit 1)" names no cause,
# and this is the read every later line depends on.
LIST_ERR=$(mktemp "${TMPDIR:-/tmp}/prevlog-list.XXXXXX" 2>/dev/null) || LIST_ERR=""
# shellcheck disable=SC2016  # Go template syntax; $ns/$pod must not be expanded by the shell.
# shellcheck disable=SC2086  # empty PREVLOG_LIST_BOUND must vanish, not become ""
if ROWS=$($PREVLOG_LIST_BOUND kubectl get pods --all-namespaces -o go-template='
{{- range .items -}}
{{- $ns := .metadata.namespace -}}
{{- $pod := .metadata.name -}}
{{- range .status.initContainerStatuses -}}
{{ $ns }}|{{ $pod }}|{{ .name }}|init|{{ .restartCount }}|{{ if .lastState }}{{ if .lastState.terminated }}{{ if .lastState.terminated.finishedAt }}{{ .lastState.terminated.finishedAt }}{{ end }}{{ end }}{{ end }}
{{ end -}}
{{- range .status.containerStatuses -}}
{{ $ns }}|{{ $pod }}|{{ .name }}|container|{{ .restartCount }}|{{ if .lastState }}{{ if .lastState.terminated }}{{ if .lastState.terminated.finishedAt }}{{ .lastState.terminated.finishedAt }}{{ end }}{{ end }}{{ end }}
{{ end -}}
{{- end -}}' 2>"${LIST_ERR:-/dev/null}"); then
  LIST_RC=0
else
  LIST_RC=$?
  ROWS=''
fi
# Last line, not the first: a failing cluster-wide list is preceded by one klog
# discovery-retry line per attempt, each long enough that trimming it to 200
# characters would cut off the cause it ends with.
LIST_REASON=$([ -n "${LIST_ERR:-}" ] && tail -n 1 "$LIST_ERR" 2>/dev/null | cut -c1-200)
[ -z "${LIST_ERR:-}" ] || rm -f "$LIST_ERR"

# An unreachable or degraded API is the state a failed e2e is usually in, and it
# yields exactly the same empty ROWS as a healthy cluster where nothing
# restarted. Distinguish them: reporting "no container has restarted" off a list
# call that never returned asserts a cluster fact this script never observed,
# and would talk a triager out of the crash-loop hypothesis.
if [ "$LIST_RC" -ne 0 ]; then
  if [ "$LIST_RC" -eq 124 ] || [ "$LIST_RC" -eq 137 ]; then
    log "could not list pods: the read was cut off by $(prevlog_cutoff_desc "$LIST_RC" "$PREVLOG_LIST_TIMEOUT" "$PREVLOG_LIST_BOUND"); cannot determine whether anything crash-looped"
  else
    if [ -z "${LIST_ERR:-}" ]; then
      log "could not list pods (kubectl exit $LIST_RC): its message could not be captured (no writable temp dir here), so whether it said anything is unknown; cannot determine whether anything crash-looped"
    else
      log "could not list pods (kubectl exit $LIST_RC): ${LIST_REASON:-no message from kubectl}; cannot determine whether anything crash-looped"
    fi
  fi
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

log "capturing previous-instance logs for $KEPT of $TOTAL restarted container(s) -> $OUT"
if [ "$KEPT" -lt "$TOTAL" ]; then
  # Never truncate silently: a reader who sees 12 dumps must know whether that
  # was all of them. Raise COZY_PREVLOG_MAX to widen.
  log "reached COZY_PREVLOG_MAX=$MAX cap; $((TOTAL - KEPT)) more restarted container(s) NOT captured"
fi

# Once per run, not once per container. The bound is the same for every log this
# capture writes, so repeating it per file put up to COZY_PREVLOG_MAX copies of one
# sentence into capture-notes.txt -- the file a reader holding only the artifact
# opens first, and the one place where the lines that explain a short capture have
# to be findable. Burying them under twelve identical tail notes is a cost paid by
# the reader for information they already had after the first. The sibling in
# hack/cozyreport.sh states the same fact once per pod directory for the same
# reason.
#
# Stated before the loop rather than after it: the loop body runs in a subshell, so
# nothing it sets survives, and a capture killed by the outer backstop mid-walk
# would lose an after-the-loop note entirely -- on exactly the run where knowing
# the logs are tail-bounded matters most.
#
# Suppressed on -1, which asks kubectl for the whole log: "holds at most the last
# -1 lines" describes a bound that was never applied.
if [ "$KEPT" -gt 0 ] && [ "$TAIL" != "-1" ]; then
  log "every previous-instance log in this directory holds at most the last $TAIL lines (COZY_PREVLOG_TAIL=$TAIL); anything earlier was never requested"
fi

printf '%s\n' "$SELECTED" | while IFS='|' read -r ns pod container kind restarts finished; do
  [ -n "$ns" ] || continue
  file="$OUT/$(prevlog_logfile_name "$ns" "$pod" "$container")"
  # --timestamps so these lines can be interleaved with the events and the
  # current-instance logs the suite's own podLogs collector emits.
  # kubectl's stderr is the ONLY thing that separates the ways this call can
  # come up empty, so it is captured and then quoted into the log line rather
  # than discarded. It goes to a scratch path outside $OUT: the outer backstop
  # can SIGKILL this script mid-read, and a stray .err file left in the snapshot
  # tree would ship as zero-byte noise in the uploaded artifact.
  # mktemp, not "$$": a predictable name under a world-writable directory is a
  # symlink an unprivileged process can plant ahead of the redirect, and if the
  # redirect itself fails the read never runs and gets reported below as kubectl
  # refusing -- a claim about the cluster caused by the local filesystem.
  err=$(mktemp "${TMPDIR:-/tmp}/prevlog-stderr.XXXXXX" 2>/dev/null) || err=""
  # -k: plain `timeout` sends TERM and then waits forever on a process that
  # ignores it, so the 20s here would not actually bound anything and the outer
  # backstop would SIGKILL the whole script instead -- and that backstop's line
  # reaches the job log only, so capture-notes.txt would end silently short on
  # exactly the run these notes were added for.
  #
  # 18 + 2 rather than 20 + 5: the grace counts towards the ceiling, and the worst
  # case this script is allowed is fixed by the callers' 300s backstop. Keeping the
  # pair at 20s keeps the 30 + 12 x 20 = 270s arithmetic the header, hack/cozytest.sh
  # and .chainsaw.yaml all state, instead of quietly moving it to 330 and getting
  # the whole script SIGKILLed on the run that needed it.
  # shellcheck disable=SC2086  # empty PREVLOG_BOUND must vanish, not become ""
  $PREVLOG_BOUND ${PREVLOG_BOUND:+$PREVLOG_READ_TIMEOUT} kubectl logs -n "$ns" "$pod" -c "$container" --previous \
      --timestamps --tail="$TAIL" >"$file" 2>"${err:-/dev/null}"
  rc=$?
  # LAST line, trimmed. A read against a cold discovery cache is preceded by one
  # klog "couldn't get current server API group list" line per retry, and kubectl's
  # own actionable message comes after them -- so the first line names the retry,
  # not the cause. Single-pod reads are not exempt: the fixture in
  # hack/cozyreport.bats models exactly that shape for one pod, and a multi-hour
  # failed run produces cold discovery routinely. This matters more since this
  # value now reaches the artifact, both in the in-file marker and in
  # capture-notes.txt, rather than only the job log. Every other stderr quote in
  # these three scripts already takes the last line.
  reason=$([ -n "$err" ] && tail -n 1 "$err" 2>/dev/null | cut -c1-200)
  # kubectl said something and still returned a log: a deprecation notice, a
  # partial-result warning. It has no place in the log file, which is read by
  # machine, but dropping it loses evidence -- and the whole point of this script
  # is that nothing is lost in silence. Copied whole, not reduced: with no klog
  # preamble to skip, every line here is a warning of its own.
  # NOT gated on the log being non-empty. A read that exits 0, writes nothing and
  # still emits a warning is the one shape where dropping the message costs most:
  # the branch below then reports "produced no output", which is a positive claim
  # about the container made while kubectl was speaking. The refusal path keeps its
  # own handling further down; this copy is about preserving what was said, whatever
  # the read returned.
  if [ -n "$err" ] && [ -s "$err" ] && { [ -s "$file" ] || [ "$rc" -eq 0 ]; }; then
    if [ -s "$file" ]; then
      log "$(basename "$file"): kubectl returned this log and also said:"
    else
      log "$(basename "$file"): kubectl returned no log, exited 0, and said:"
    fi
    while IFS= read -r _warn_line; do log "  $_warn_line"; done < "$err"
  fi
  # Remembered before the scratch file is removed: the branches below still need to
  # know whether kubectl spoke, and by then there is nothing left to ask.
  err_had_output=""
  if [ -n "$err" ] && [ -s "$err" ]; then err_had_output=1; fi
  [ -z "$err" ] || rm -f "$err"
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
    # 124 and 137 both mean the read hit its own timeout: `timeout -k` reports 137
    # when the command ignored SIGTERM and had to be killed afterwards.
    if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
      log "reading the previous instance for $ns/$pod [$container] was cut off by $(prevlog_cutoff_desc "$rc" "$PREVLOG_READ_TIMEOUT" "$PREVLOG_BOUND")"
    else
      if [ -z "$err" ]; then
        log "no previous instance retrieved for $ns/$pod [$container] (kubectl exit $rc): its message could not be captured (no writable temp dir here), so whether it said anything is unknown"
      else
        log "no previous instance retrieved for $ns/$pod [$container] (kubectl exit $rc): ${reason:-no message from kubectl}"
      fi
    fi
    continue
  fi
  if [ ! -s "$file" ]; then
    # kubectl succeeded but the previous instance wrote nothing. That is itself a
    # finding -- a container OOM-killed or failing exec before its first write
    # looks exactly like this -- so name it rather than leaving a silent gap that
    # reads as "never attempted".
    rm -f "$file"
    # "Produced no output" is a claim about the container, so it may only be made
    # when kubectl itself said nothing. When it did speak, the message is already
    # in capture-notes.txt above and the two lines have to agree rather than one
    # asserting silence the other just contradicted.
    if [ -n "$err_had_output" ]; then
      log "previous instance for $ns/$pod [$container] returned no log, though kubectl exited 0 and wrote the message above"
    else
      log "previous instance for $ns/$pod [$container] produced no output"
    fi
    continue
  fi
  if [ "$rc" -ne 0 ]; then
    # The read was cut short, but whatever landed before the cut is still the
    # evidence we came for. Keep it and mark it truncated; deleting a partial
    # capture would throw away the only copy of a log that is already gone from
    # the cluster.
    # Same three-way split as the in-file marker below, not a bare exit status.
    # 124 and 137 are this script's own clock; anything else is kubectl failing on
    # its own terms. Reporting "kubectl exit 124" here while the marker inside the
    # file names the timeout puts two sentences about ONE read in two files that
    # disagree, and this is the one a reader meets first.
    if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
      log "previous-instance log for $ns/$pod [$container] is TRUNCATED: the read was cut off by $(prevlog_cutoff_desc "$rc" "$PREVLOG_READ_TIMEOUT" "$PREVLOG_BOUND")"
    else
      log "previous-instance log for $ns/$pod [$container] is TRUNCATED: kubectl exited $rc part way through${reason:+: $reason}"
    fi
  fi
  # Why this file is not the whole log, stated inside it. Which reason applies
  # depends on how the read ended: `--tail` drops the OLDEST lines, a killed read
  # drops the NEWEST, and a file missing its end is a different object from one
  # missing its beginning.
  if [ "$rc" -ne 0 ]; then
    # 124 and 137 are our own timeout firing; anything else is kubectl failing on
    # its own terms, and it can also leave partial output -- a connection reset
    # mid-stream writes some lines and then exits non-zero without any clock having
    # run out. Naming the timeout for both would put a cause in the artifact that
    # was never observed, which is the same defect as the tail note claiming a
    # clean window: a confident explanation that happens to be false. The adjacent
    # log() line above draws the same distinction, and both have to: they describe
    # one read in two places a reader compares.
    if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
      prevlog_append_note "$file" \
        "[capture-previous-logs] TRUNCATED: this log ends here because the read was cut off by $(prevlog_cutoff_desc "$rc" "$PREVLOG_READ_TIMEOUT" "$PREVLOG_BOUND"), not because the container stopped writing"
    else
      prevlog_append_note "$file" \
        "[capture-previous-logs] TRUNCATED: this log ends here because kubectl exited $rc part way through, not because the container stopped writing${reason:+: $reason}"
    fi
  fi

  # The `--tail` bound applies to this file as it does to every other one here,
  # and it is stated ONCE for the whole capture above the loop rather than once per
  # container. It still has to be stated for a capture cut short at its END, since
  # such a file lost its OLDEST lines to `--tail` as well and naming only the end
  # would tell the reader the file starts where the container did -- but that is
  # what the run-level line covers, and it covers it without putting twelve copies
  # of one sentence into the file a reader opens first.
  #
  # Recorded beside the log rather than inside it, wherever it is recorded: a log
  # read in full is a complete artifact, these platforms log JSON per line, and a
  # trailing prose line breaks every parser that could read the file before.
  # Echo as well as archive. The archive is for later; the failing job's log is
  # where whoever is triaging the red run is already looking.
  #
  # When the previous instance ended is in the header because it is the first
  # thing that decides whether this dump is worth reading: an instance that
  # ended during cluster bring-up cannot explain a test that started afterwards,
  # and until a reader knows that they have to read 200 lines to find out. It is
  # also what prevlog_sort_recent ordered on, so the header shows the ordering
  # rather than leaving it implicit. Omitted rather than guessed when the kubelet
  # has already dropped the lastState -- "ended unknown" and "ended at 00:00"
  # are not the same claim.
  #
  # "last -1 lines" would be nonsense; -1 means the whole log was requested.
  if [ -n "$finished" ]; then
    _ended=", previous instance ended $finished"
  else
    _ended=""
  fi
  if [ "$TAIL" = "-1" ]; then
    echo "----- previous logs: $ns/$pod [$kind $container] (restarts=$restarts$_ended, whole log) -----"
  else
    echo "----- previous logs: $ns/$pod [$kind $container] (restarts=$restarts$_ended, last $TAIL lines) -----"
  fi
  cat "$file"
  echo "----- end $ns/$pod [$container] -----"
done

exit 0
