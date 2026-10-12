#!/bin/sh
# Emit a human-readable summary of "what is broken" to a single file.
# Reads the live cluster (not the report dir) so it can use kubectl JSONPath.
# Usage: cozyreport-summary.sh > summary.txt
#
# Environment:
#   COZY_REPORT_PODS_MAX  how many not-Ready pods this summary lists before it
#                         reports the rest as an overflow (default 40, shared with
#                         the collector so the two never start from different
#                         numbers; the counts can still differ when the collector
#                         stops on its time budget, and its
#                         COLLECTION-TRUNCATED.txt explains that). A malformed value
#                         falls back to the default and says so in the section.
#                         Note it does not bound the HelmRelease listing, which has
#                         its own cap at the same default.
#   COZY_REPORT_PREFER_NS namespace prefix listed first (default `tenant-`; empty
#                         disables the ordering). Reached through the selection
#                         helpers sourced from hack/cozyreport.sh rather than read
#                         by name here, so it appears nowhere in this file's CODE
#                         (only in this block) -- which is exactly why it went
#                         undocumented:
#                         a knob arriving by `.` is invisible to a guard that greps
#                         for its name. It changes which pods this summary lists
#                         first, and the collector uses the same value, so the two
#                         orderings stay in step.
set -eu

# Share the pod-selection helper with cozyreport.sh rather than restating it:
# the summary and the collected evidence disagreeing about which pods are broken
# is worse than either being wrong on its own. COZYREPORT_LIB makes the source
# return before it starts a report of its own.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC2034  # read by the sourcing guard in cozyreport.sh
COZYREPORT_LIB=1
# shellcheck source=hack/cozyreport.sh
. "$SCRIPT_DIR/cozyreport.sh"

# The wall-clock wrapper, taken from cozyreport.sh rather than resolved again so
# the two cannot hold different values. Empty when `timeout` is absent; the reads
# then run unbounded and the header says so.
BOUND=$COZYREPORT_BOUND

# How many rows any one section lists before it reports the rest as an overflow.
# Shares the collector's default so the two never START from different numbers.
# The counts can still differ, and legitimately: the collector also stops on its
# time budget, so it may hold fewer pods than this lists. Its own
# COLLECTION-TRUNCATED.txt says so; see the note on the pod listing below.
SUMMARY_CAP=$COZYREPORT_PODS_DEFAULT

# summary_read <what> <kubectl args...>
#
# One bounded read into READ_OUT. On failure it prints why the section is empty,
# because a killed read otherwise renders the section byte-identical to a healthy
# cluster -- and an empty "## HelmReleases not Ready" is the most misleading line
# this file can carry. CRD probes go through summary_crd, where a non-zero status
# has two meanings.
summary_read() {
  _sr_what=$1
  shift
  # kubectl's own reason is kept rather than sent to /dev/null, for the reason the
  # pod list below already keeps its own: "did not return (exit 1)" names a symptom
  # and no cause, and the difference between an apiserver that is too slow and one
  # answering "forbidden" is the whole finding. Discarding it here while the pod-list
  # read below keeps its own would have two reads in one file answering the same
  # question differently.
  _sr_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-summary.XXXXXX" 2>/dev/null) || _sr_err=""
  # The status is taken in an explicit `else`, not after the `if`: a compound `if`
  # whose condition fails and which has no else leaves `$?` at 0, so reading it
  # afterwards reports "exit 0" for every failure.
  # shellcheck disable=SC2086  # empty BOUND must vanish, not become ""
  if READ_OUT=$($BOUND "$@" 2>"${_sr_err:-/dev/null}"); then
    [ -z "$_sr_err" ] || rm -f "$_sr_err"
    return 0
  else
    _sr_rc=$?
  fi
  # The rows kubectl printed before it died are kept: they are the only ones it
  # named, and before this refactor they flowed down the pipe and were processed.
  # Discarding them turns a partial answer into no answer and then says nothing
  # was readable, which is false in the most useful direction.
  if [ -n "$READ_OUT" ]; then
    echo "  the $_sr_what read did not return (exit $_sr_rc); the rows below are the ones it printed before it stopped, and there may be others it never reached"
  else
    echo "  the $_sr_what read did not return (exit $_sr_rc), so this section is empty because nothing could be read, NOT because there was nothing to report"
  fi
  # LAST line, not the first, matching the pod list: a failing read is preceded by
  # one klog discovery-retry line per attempt and kubectl's own message comes after
  # them. A read killed by its own timeout is killed before kubectl writes anything,
  # so the file is empty and there is nothing to quote -- which is itself the
  # difference between "too slow to answer" and "answered, and the answer was no".
  if [ -n "$_sr_err" ] && [ -s "$_sr_err" ]; then
    # printf, not echo: kubectl's text is untrusted and dash's echo expands
    # backslash escapes, so a message carrying one could forge a line in a file a
    # triager reads as fact.
    printf '  kubectl said: %s\n' "$(tail -n 1 "$_sr_err" | cut -c1-200)"
  fi
  [ -z "$_sr_err" ] || rm -f "$_sr_err"
  return 0
}

# summary_crd <crd>: true when the CRD is present, false otherwise. Absence is the
# only outcome that answers false in silence; a killed read, a refusal with a
# message, a refusal without one, and a message that could not be captured each
# say which of them happened before answering false.
#
# `if $BOUND kubectl get crd X` collapses those last two into one, and a slow
# aggregated discovery layer -- the state that made the bound necessary -- then
# skips the whole section as though the CRD were not installed.
summary_crd() {
  # stderr kept for the same reason summary_read keeps it, but reported only on a
  # killed read: a CRD that is simply absent also exits non-zero, with kubectl
  # writing a NotFound to stderr, and quoting that would turn "this cluster does not
  # run cert-manager" into a line that reads like a failure. The killed case is the
  # one where the cause is not already implied by the outcome.
  _sc_err=$(mktemp "${TMPDIR:-/tmp}/cozyreport-summary.XXXXXX" 2>/dev/null) || _sc_err=""
  # shellcheck disable=SC2086
  if $BOUND kubectl get crd "$1" >/dev/null 2>"${_sc_err:-/dev/null}"; then
    [ -z "$_sc_err" ] || rm -f "$_sc_err"
    return 0
  else
    _sc_rc=$?
  fi
  if cozyreport_timed_out "$_sc_rc"; then
    echo "  the $1 presence check did not return (exit $_sc_rc), so this section was skipped without knowing whether the CRD is installed"
  elif [ -z "$_sc_err" ]; then
    # No scratch file means no writable temp dir here, so kubectl's message went to
    # /dev/null and is gone. Silence at this point would be the report's own local
    # condition rendered as a cluster fact, and rendered as the most common one:
    # every section this gate opens simply disappears, exactly as it does on a
    # cluster that does not run the operator behind the CRD.
    echo "  the $1 presence check failed (exit $_sc_rc) and its message could not be captured (no writable temp dir on the machine producing this report), so this section was skipped without knowing whether the CRD is installed"
  elif [ -s "$_sc_err" ]; then
    # Not a timeout and not a plain NotFound: an RBAC refusal on the CRD read skips
    # the section exactly like an absent CRD would, and only kubectl's message
    # separates "not installed" from "not allowed to look".
    if ! grep -qi 'not found' "$_sc_err"; then
      printf '  the %s presence check failed, so this section was skipped without knowing whether the CRD is installed; kubectl said: %s\n' \
        "$1" "$(tail -n 1 "$_sc_err" | cut -c1-200)"
    fi
  else
    # Exited non-zero having said nothing. Not the same as NotFound, which kubectl
    # is explicit about, so it cannot be filed under "the CRD is absent".
    echo "  the $1 presence check failed (exit $_sc_rc) without a message, so this section was skipped without knowing whether the CRD is installed"
  fi
  [ -z "$_sc_err" ] || rm -f "$_sc_err"
  return 1
}

# Printed before the first section, not inside one: every read in this file goes
# through the same empty $BOUND, so a notice sitting under "## Pods not Ready"
# leaves the reader of "## HelmReleases not Ready" -- which is above it and is the
# first section anyone opens -- with no indication at all. The collector states the
# same fact for its whole tree in COLLECTION-UNBOUNDED.txt rather than per section.
if [ -z "$BOUND" ]; then
  echo "(timeout is not on this host, so every read below is unbounded; this is a local dependency, not a cluster symptom)"
  echo
fi

echo "# Cozystack E2E Diagnostic Summary"
echo "Generated: $(date -Iseconds)"
echo

echo "## HelmReleases not Ready"
echo
if summary_crd helmreleases.helm.toolkit.fluxcd.io; then
  summary_read "HelmRelease" kubectl get hr -A --no-headers
  # The whole STATUS field, not $5. `kubectl get hr` prints NAMESPACE NAME AGE
  # READY STATUS, and STATUS is the Ready condition's *message* -- see the printer
  # columns on the CRD this repo ships, internal/fluxinstall/manifests/fluxcd.yaml
  # ("jsonPath: .status.conditions[?(@.type==\"Ready\")].message"). It is a whole
  # sentence, so $5 rendered every failed release as "Helm" or "Reconciliation",
  # dropping the part that says what broke, in the first section of the first file
  # a triager opens. Take everything from the fifth field on.
  # Rebuilt field by field rather than with a `{4}` interval expression: mawk
  # 1.3.4, the awk on the CI image, mis-applies an exact interval to a
  # variable-width group and strips one repetition instead of four. `sub()` still
  # returns 1, so there is no error and no signal -- the row simply arrives with
  # most of itself still in the message. Verified on ubuntu:24.04; gawk,
  # one-true-awk and busybox awk all handle it, which is how it would have reached
  # CI unnoticed.
  HR_ROWS=$(printf '%s\n' "$READ_OUT" | awk 'NF && $4 != "True" {
    msg = ""
    for (i = 5; i <= NF; i++) msg = msg (i > 5 ? " " : "") $i
    printf "  %s/%s — %s\n", $1, $2, msg
  }')
  HR_TOTAL=$(printf '%s\n' "$HR_ROWS" | grep -c . || true)
  if [ "$HR_TOTAL" -gt 0 ]; then
    printf '%s\n' "$HR_ROWS" | head -n "$SUMMARY_CAP"
  fi
  # The cap is reported for the same reason the pod cap is: an install that fails
  # early leaves well over 40 HelmReleases not Ready, and a listing that stops at 40
  # without saying so reads as the complete set.
  if [ "$HR_TOTAL" -gt "$SUMMARY_CAP" ]; then
    echo "  ... $((HR_TOTAL - SUMMARY_CAP)) more not-Ready HelmRelease(s) not listed here; the full set is in the report's kubernetes/helmreleases.txt"
  fi
fi
echo

echo "## Pods not Ready"
echo
# AGE is read as $NF, not $6: a pod that has restarted prints RESTARTS as
# `7 (4m12s ago)`, three awk fields, which shifts every later column.
# A restarted pod is the normal case in a failed run, so $6 printed `age=(4m12s`
# for exactly the rows worth reading. AGE is last whichever form RESTARTS takes.
#
# The row cap is reported rather than applied in silence: this section is a
# reader's first answer to "what is broken", and a cap that hides the rest reads
# like there is no rest. The ordering is shared with the collector, so the rows
# this cap keeps are the rows whose evidence the collector reached for first --
# the same rows, not necessarily the same count, since the collector can also stop
# on its time budget, and then it holds fewer pods than are listed here. Its own
# COLLECTION-TRUNCATED.txt says so.
#
# The pod list is read on its own, keeping its exit status, before anything is
# selected from it. Piping it straight into the selection discards that status,
# and then a list the apiserver refused renders byte-identical to a cluster where
# every pod is Ready -- in the file a triager opens first, and on exactly the run
# where the summary must not be the thing that says nothing is wrong.
POD_LIST_ERR=$(mktemp "${TMPDIR:-/tmp}/cozyreport-summary.XXXXXX" 2>/dev/null) || POD_LIST_ERR=""
# Bounded like the collector's copy of this read. The summary runs immediately
# before the tarball is written, so a hang here costs the whole artifact rather
# than this one section, and `|| true` on the caller does not survive a hang.
#
# `timeout` is used when it is there and skipped when it is not, rather than being
# required: without the fallback every read here exits 127 and this section would
# report that the cluster did not answer, on a host where the only thing missing is
# a local coreutils binary. That is the same false claim the collector's
# COLLECTION-UNBOUNDED.txt exists to avoid, and the two must not disagree inside
# one tarball. It also keeps working where the old unbounded pipeline did.
# Unquoted on purpose: an empty BOUND must expand to no argument at all.
# shellcheck disable=SC2086
if POD_LIST=$($BOUND kubectl get pod -A --no-headers 2>"${POD_LIST_ERR:-/dev/null}"); then
  POD_LIST_RC=0
else
  POD_LIST_RC=$?
fi
if [ "$POD_LIST_RC" -ne 0 ]; then
  # Two different failures, and the sentence has to match which one happened. A
  # read that produced nothing leaves an empty section; a read that printed rows
  # and then failed leaves the rows, and calling that section "empty" while the
  # rows are listed three lines below puts two contradictory statements about the
  # same read in one file. The partial rows are kept, as everywhere else in this
  # tree, because they are the only evidence there is -- they are just not all of
  # it, and the difference has to be said rather than implied.
  if [ -n "$POD_LIST" ]; then
    echo "  the cluster-wide pod list did not return (kubectl exit $POD_LIST_RC); the pods listed below are the ones it printed before it stopped, and there may be others it never reached"
  else
    echo "  the cluster-wide pod list did not return (kubectl exit $POD_LIST_RC), so this section is empty because nothing could be read, NOT because every pod was Ready"
  fi
  if [ -n "$POD_LIST_ERR" ] && [ -s "$POD_LIST_ERR" ]; then
    # Last line: a failing cluster-wide list is preceded by one klog discovery
    # retry line per attempt and kubectl's own message comes after them.
    printf '  kubectl said: %s\n' "$(tail -n 1 "$POD_LIST_ERR" | cut -c1-200)"
  fi
fi
[ -z "$POD_LIST_ERR" ] || rm -f "$POD_LIST_ERR"
POD_ROWS=$(printf '%s\n' "$POD_LIST" | cozyreport_pods_not_ready | cozyreport_pods_prioritize)
POD_TOTAL=$(printf '%s\n' "$POD_ROWS" | grep -c . || true)
POD_CAP=${COZY_REPORT_PODS_MAX:-$COZYREPORT_PODS_DEFAULT}
if ! cozyreport_is_count "$POD_CAP"; then
  echo "  (ignored a malformed COZY_REPORT_PODS_MAX='${COZY_REPORT_PODS_MAX:-}'; listing at most $COZYREPORT_PODS_DEFAULT)"
  POD_CAP=$COZYREPORT_PODS_DEFAULT
fi
# `NF` guard: an empty selection arrives as one empty line, and an unguarded action
# would print a row of blanks -- a nameless broken pod on every green run.
POD_LISTING=$(printf '%s\n' "$POD_ROWS" \
  | awk 'NF {printf "  %s/%s — %s ready=%s (restarts=%s, age=%s)\n", $1, $2, $4, $3, $5, $NF}')
# A zero cap is branched on rather than written as `A && B || C`, which would send
# `head -n 0` the value this guard keeps away from it (GNU head returns nothing,
# BSD/macOS head errors out).
if [ "$POD_CAP" -gt 0 ] && [ -n "$POD_LISTING" ]; then
  printf '%s\n' "$POD_LISTING" | head -n "$POD_CAP"
fi
if [ "${POD_TOTAL:-0}" -gt "$POD_CAP" ]; then
  echo "  ... $((POD_TOTAL - POD_CAP)) more not-Ready pod(s) not listed here; the full set is in kubernetes/pods.txt"
fi
echo

echo "## ImagePullBackOff / ErrImagePull"
echo
# Reuses the list already read above rather than calling the apiserver a second
# time for the same rows: one fewer cluster-wide read on the path that runs
# immediately before the tarball is written, and the two sections cannot disagree
# about what the cluster looked like.
#
# Which means it inherits that read's failure too. Without this guard the section
# is byte-identical between a healthy cluster and one whose pod list was refused,
# and a triager who jumps straight to this heading reads "nothing is failing to
# pull" off a list that never arrived.
if [ "$POD_LIST_RC" -ne 0 ]; then
  echo "  not known: this section is derived from the cluster-wide pod list above, which did not return"
else
  printf '%s\n' "$POD_LIST" \
    | awk 'NF && $4 ~ /ImagePullBackOff|ErrImagePull/ {printf "  %s/%s — %s\n", $1, $2, $4}'
fi
echo

echo "## Recent OOMKilled events (last 20)"
echo
summary_read "OOMKilling event" kubectl get events -A --field-selector reason=OOMKilling --sort-by=.lastTimestamp
printf '%s\n' "$READ_OUT" | awk 'NF' | tail -20
echo

echo "## Recent Warning events (last 30)"
echo
summary_read "Warning event" kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp
printf '%s\n' "$READ_OUT" | awk 'NF' | tail -30
echo

echo "## cert-manager: Certificates not Ready"
echo
if summary_crd certificates.cert-manager.io; then
  summary_read "Certificate" kubectl get certificates.cert-manager.io -A --no-headers
  printf '%s\n' "$READ_OUT" | awk 'NF && $3 != "True" {printf "  %s/%s — Ready=%s\n", $1, $2, $3}'
fi
echo

echo "## Flux Sources not Ready"
echo
for kind in helmrepositories.source.toolkit.fluxcd.io ocirepositories.source.toolkit.fluxcd.io gitrepositories.source.toolkit.fluxcd.io externalartifacts.source.toolkit.fluxcd.io; do
  summary_crd "$kind" || continue
  short=${kind%%.*}
  # custom-columns, not jsonpath. The jsonpath this replaces nested one filter
  # inside another -- `.items[?(...conditions[?(@.type=="Ready")]...)]` -- which
  # kubectl rejects outright with "unterminated filter", so this section printed
  # nothing on every report ever taken and read as "all Flux sources are Ready".
  # A form that sends stderr to /dev/null and drops the status hides that
  # completely: the section renders empty and reads as "all sources Ready".
  #
  # Not the printer columns either: READY sits at a different index per kind, and
  # STATUS carries spaces, so column counting is wrong for at least one of the four.
  # custom-columns takes a single filter, the same shape the node section below uses.
  summary_read "$short" kubectl get "$kind" -A --no-headers \
    -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status'
  printf '%s\n' "$READ_OUT" | awk -v k="$short" 'NF && $3 != "True" {printf "  %s %s/%s — Ready=%s\n", k, $1, $2, $3}'
done
echo

echo "## Storage: PVCs not Bound, PVs not Bound"
echo
summary_read "PVC" kubectl get pvc -A --no-headers
printf '%s\n' "$READ_OUT" | awk 'NF && $3 != "Bound" {printf "  PVC %s/%s — %s\n", $1, $2, $3}'
summary_read "PV" kubectl get pv --no-headers
printf '%s\n' "$READ_OUT" | awk 'NF && $5 != "Bound" {printf "  PV %s — %s\n", $1, $5}'
echo

echo "## Node Conditions"
summary_read "node condition" kubectl get nodes -o 'custom-columns=NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,DISK:.status.conditions[?(@.type=="DiskPressure")].status,MEM:.status.conditions[?(@.type=="MemoryPressure")].status'
[ -z "$READ_OUT" ] || printf '%s\n' "$READ_OUT"
