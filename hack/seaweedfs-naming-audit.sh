#!/bin/sh
# SeaweedFS 4.31 rename — fleet audit (READ-ONLY).
#
# Classifies every SeaweedFS tenant into the three states the chart's naming guard
# (packages/system/seaweedfs/templates/naming-guard.yaml) decides between:
#
#   L      exactly the chart-named generation -> the upgrade adopts it. Nothing to do.
#   S      exactly the release-named generation -> re-bind first (runbook Step 2).
#   MIXED  both generations -> the chart REFUSES; an operator must remove the empty one.
#
# For MIXED it also reports which generation is ORIGINAL, from durable evidence:
#
#   sh.helm.release.v1.<name>-system.v1  — the manifest of revision 1 names either
#       seaweedfs-master (born pre-4.31) or <release>-master (born 4.31). Exact, but
#       revision 1 may have been pruned by Helm's history limit.
#   PersistentVolume creation timestamps — the original generation's PVs predate the
#       duplicate's, which were provisioned later by the bad upgrade. Compared only
#       against each other (see the relative rule below); info.first_deployed is
#       printed as context but decides nothing.
#
# PV timestamps, not PVC ones: runbook Step 2 deletes each release-named claim and
# recreates it under the chart name against the SAME PV, so claim age is not durable
# and inverts for a tenant interrupted mid-re-bind. The PV survives and keeps its
# creationTimestamp.
#
# Direction is decided by a RELATIVE rule, never a clock window: a generation is
# the candidate duplicate only when EVERY one of its bound PVs is strictly newer
# than every bound PV of the other generation — the same precondition runbook
# Step 2a enforces before deleting anything. Overlapping vintages mean an
# interrupted Step 2 re-bind (both generations on original PVs) or interleaved
# provisioning, and the audit refuses to name a candidate. An absolute window
# measured from first_deployed was tried first and is unsound: the shipped
# StorageClasses are WaitForFirstConsumer, so PVs appear at pod-SCHEDULE time,
# and ordinary cold-cluster latency puts even the original generation's PVs
# minutes past first_deployed — which pushed a mid-rebind tenant into the branch
# whose advice deletes the un-re-bound claims.
#
# IMPORTANT — what this does NOT tell you. "Which generation is original" is not
# "the other one is empty". A duplicate that never scheduled (safe to delete) and one
# that served writes and later crashed (holds unique objects) are identical on every
# durable signal — same revision-1 scheme, same first_deployed deltas. Establishing
# emptiness needs the duplicate's volume files and the filer's volume list, which this
# script does not inspect. It narrows the question; it does not answer it.
#
# Usage:  hack/seaweedfs-naming-audit.sh [namespace...]
#         KUBECONFIG=<file> hack/seaweedfs-naming-audit.sh
# Exit:   0        the audit RAN TO COMPLETION; the table is the whole answer
#                  (an empty table then means a genuinely clean fleet).
#         non-zero a kubectl query FAILED, so the audit is INCOMPLETE and the
#                  table must NOT be trusted -- a message naming the failed query
#                  is on stderr. This is fail-CLOSED by design (see run_kubectl):
#                  the output gates a destructive runbook step, so an unreachable
#                  API aborts loudly rather than printing an empty table that
#                  reads identically to "nothing to do".
# Nothing is ever mutated on either path -- every kubectl call is read-only.
#
# POSIX sh, deliberately. hack/seaweedfs-naming-audit.bats sources this file, and
# cozytest.sh sources the converted test into its own /bin/sh — which is dash on
# the CI runner. A bash shebang would not save it: `.` runs the file's contents in
# the caller's shell and the shebang is just a comment. Keeping the script POSIX
# is what makes the tested shell and the executed shell the same one; anything
# bash-only here is untested in CI and unavailable at run time. `pipefail` in
# particular is not POSIX and dash 0.5.12 (Ubuntu) rejects it outright.
set -u

# run_kubectl <what> <kubectl-args...> -- run ONE read-only kubectl query
# fail-CLOSED and print its stdout. This audit is the documented gate in front of
# a destructive runbook step (deleting a tenant's PVCs), so a query that FAILS
# must never be mistaken for a query that found nothing: an enumerating LIST
# returns empty stdout on a timeout or an RBAC denial, byte-identical to the same
# LIST succeeding on a genuinely clean fleet, and the old `2>/dev/null` on each
# call turned an unreachable API into a silent false "nothing to do". Every
# enumeration is routed through here so any failure is loud and terminal instead.
#
# On success prints kubectl's stdout verbatim -- which may legitimately be empty,
# because a LIST that matched nothing is honestly clean; keeping that case
# distinct from failure is the whole point. On a non-zero exit prints the failed
# query to stderr and returns that exit code; kubectl's own stderr stays on fd 2
# and reaches the operator unfiltered. Callers MUST propagate the non-zero status
# (`|| return`/`|| exit`) -- a bare `for x in $(run_kubectl ...)` would swallow
# it, because a for-loop ignores the exit status of its word-list command, and an
# `exit` inside the `$(...)` subshell would only leave that subshell.
run_kubectl() {
  _rk_what="$1"; shift
  if _rk_out=$(kubectl "$@"); then
    printf '%s' "$_rk_out"
    return 0
  else
    _rk_rc=$?
    printf 'seaweedfs-naming-audit: FATAL: %s failed (kubectl %s -> exit %s). Refusing to report a clean fleet on an unreachable API.\n' \
      "$_rk_what" "$*" "$_rk_rc" >&2
    return "$_rk_rc"
  fi
}

# audit_fatal <message> -- the counterpart to run_kubectl for corruption the API
# call itself did NOT report: the query succeeded, but what it returned is
# impossible for a healthy object (a helm.sh/release.v1 Secret with no decodable
# release payload). Same fail-CLOSED contract -- print a FATAL line naming the
# object to stderr and return non-zero; callers MUST propagate it. A silent skip
# here would be the identical false-clean this whole script guards against, just
# one field deeper than an unreachable API.
audit_fatal() {
  printf 'seaweedfs-naming-audit: FATAL: %s Refusing to report a clean fleet on corrupt state.\n' "$1" >&2
  return 1
}

# renamed_volume_prefix <release> -- reconstruct the name 4.31 gives the volume
# component of <release>. Mirrors seaweedfs.fullname + seaweedfs.componentName
# (see packages/system/seaweedfs/templates/_naming.tpl, which the chart's guard
# uses for exactly the same purpose): the fullname gains -seaweedfs when the
# release name does not already contain it, is capped at 63, and componentName
# then cuts it to (62 - len("volume")) = 56 before appending -volume.
renamed_volume_prefix() {
  release="$1"
  case "$release" in
    *seaweedfs*) full="$release" ;;
    *)           full="${release}-seaweedfs" ;;
  esac
  full=$(printf '%s' "$full" | cut -c1-63 | sed 's/-$//')
  printf '%s-volume' "$(printf '%s' "$full" | cut -c1-56 | sed 's/-$//')"
}

# release_json <ns> <release> [rev] -- decode a Helm release secret's payload, or
# "" when that revision's secret does not exist. Helm stores base64(gzip(json)) in
# Secret.data.release, and Kubernetes base64s the data value again, hence the two
# decodes.
#
# This is a by-NAME GET, but it is NOT best-effort: system_releases decides whether
# a release IS SeaweedFS from the result, so a swallowed failure silently drops a
# real tenant -- the false-clean this script exists to prevent. EXISTENCE and
# EXTRACTION are therefore two separate questions, because `--ignore-not-found
# -o jsonpath` cannot tell them apart (it returns empty + exit 0 both for an absent
# Secret AND for a present Secret whose .data.release is missing):
#
#   1. Existence, via `--ignore-not-found -o name`. Empty + exit 0 = the revision
#      was pruned by Helm's history limit -- a legitimate absence; return "" and
#      let rev1_scheme / first_deployed treat it as "pruned". A real error (RBAC,
#      timeout, apiserver down) stays non-zero and run_kubectl makes it fatal.
#   2. The Secret EXISTS, so its payload MUST decode. A helm.sh/release.v1 Secret
#      with no .data.release, a payload that is not base64(base64(gzip(...))), or an
#      empty JSON after decoding is CORRUPT state, not an absence -- and here that
#      is safety-critical, so every such anomaly is audit_fatal, never a silent skip
#      (contrast pv_epoch, where an empty field is a benign "no evidence").
#
# The extraction GET drops --ignore-not-found deliberately: the Secret existed a
# moment ago, so a NotFound now is a mid-audit deletion race, and failing closed on
# it is correct.
release_json() {
  _rj_secret="sh.helm.release.v1.$2.v${3:-1}"
  _rj_exists=$(run_kubectl "check Helm release secret '$_rj_secret' in namespace '$1'" \
    get secret -n "$1" "$_rj_secret" --ignore-not-found -o name) || return $?
  [ -n "$_rj_exists" ] || return 0
  _rj_field=$(run_kubectl "read .data.release of secret '$_rj_secret' in namespace '$1'" \
    get secret -n "$1" "$_rj_secret" -o jsonpath='{.data.release}') || return $?
  [ -n "$_rj_field" ] || { audit_fatal "secret '$_rj_secret' in namespace '$1' exists but has no .data.release payload (corrupt Helm release)."; return 1; }
  _rj_json=$(printf '%s' "$_rj_field" | base64 -d 2>/dev/null | base64 -d 2>/dev/null | gunzip 2>/dev/null) \
    || { audit_fatal "secret '$_rj_secret' in namespace '$1': .data.release is not decodable base64(base64(gzip(json))) (corrupt Helm release)."; return 1; }
  [ -n "$_rj_json" ] || { audit_fatal "secret '$_rj_secret' in namespace '$1': release payload decoded to nothing (corrupt Helm release)."; return 1; }
  printf '%s' "$_rj_json"
}

# revisions <ns> <release> -- retained revision numbers, oldest first.
revisions() {
  _rev_out=$(run_kubectl "list Helm revision secrets for '$2' in namespace '$1'" \
    get secret -n "$1" -l "name=$2,owner=helm" \
    -o jsonpath='{range .items[*]}{.metadata.labels.version}{"\n"}{end}') || return $?
  printf '%s\n' "$_rev_out" | sort -n
}

# system_releases <ns> -- the SeaweedFS <name>-system Helm releases in a namespace.
# Filtering on the `-system` suffix alone is not enough: every Cozystack app has a
# <name>-system release (ingress-nginx-system, bucket-*-system, ...), and since the
# generation scan below matches PVCs by NAME across the whole namespace, an
# unrelated release in a namespace that happens to run SeaweedFS would be reported
# as a SeaweedFS tenant. Confirm the chart.
system_releases() {
  _sr_secrets=$(run_kubectl "list Helm release secrets in namespace '$1'" \
    get secret -n "$1" \
    -o jsonpath='{range .items[?(@.type=="helm.sh/release.v1")]}{.metadata.labels.name}{"\n"}{end}') || return $?
  for rel in $(printf '%s\n' "$_sr_secrets" | grep -E -- '-system$' | sort -u); do
    _sr_revs=$(revisions "$1" "$rel") || return $?
    for rev in $_sr_revs; do
      _sr_json=$(release_json "$1" "$rel" "$rev") || return $?
      # Empty "" here means the revision secret was pruned (release_json's absence
      # path) -- a legitimate skip. A NON-empty payload, however, was fetched and
      # decoded successfully, so it MUST name its chart: a helm.sh/release.v1 release
      # always carries chart.metadata.name. A payload without one ({} , or any valid
      # JSON lacking it) is corrupt/unexpected, and since "no chart name" is
      # indistinguishable downstream from "not SeaweedFS", silently skipping it is
      # the very false-clean this guard exists to stop -- so it is FATAL. A present
      # but different chart name is a real, non-SeaweedFS release and stays a
      # legitimate skip, filtered exactly as before by the cozy-seaweedfs test.
      # ("chart" is the release struct's only such key -- vendored subcharts nest
      # under "dependencies" -- so this match is unambiguous.)
      if [ -n "$_sr_json" ]; then
        _sr_chart=$(printf '%s' "$_sr_json" | sed -n 's/.*"chart":{"metadata":{"name":"\([^"]*\)".*/\1/p' | head -1)
        [ -n "$_sr_chart" ] || { audit_fatal "secret 'sh.helm.release.v1.$rel.v$rev' in namespace '$1' decoded to a payload with no chart name (corrupt Helm release)."; return 1; }
        if [ "$_sr_chart" = cozy-seaweedfs ]; then printf '%s\n' "$rel"; fi
      fi
      break
    done
  done
}

# first_deployed <ns> <release> -- epoch seconds of the release's first install,
# from any retained revision (the field is identical on all of them).
first_deployed() {
  _fd_revs=$(revisions "$1" "$2") || return $?
  for rev in $_fd_revs; do
    _fd_json=$(release_json "$1" "$2" "$rev") || return $?
    ts=$(printf '%s' "$_fd_json" | sed -n 's/.*"first_deployed":"\([^"]*\)".*/\1/p' | head -1)
    if [ -n "$ts" ]; then date -u -d "$(printf '%s' "$ts" | cut -c1-19)" +%s 2>/dev/null; return; fi
  done
}

# rev1_scheme <ns> <release> -- "legacy" | "renamed" | "" (revision 1 pruned).
rev1_scheme() {
  m=$(release_json "$1" "$2" 1) || return $?
  [ -n "$m" ] || return 0
  if printf '%s' "$m" | grep -q 'name: seaweedfs-master'; then printf 'legacy'
  elif printf '%s' "$m" | grep -qE "name: $2(-seaweedfs)?-master"; then printf 'renamed'
  fi
}

# pv_epoch <ns> <pvc> -- creation epoch of the PV the claim is BOUND to, or "" when
# there is no usable PV age. Two by-NAME GETs.
#
# A real API error must abort (an epoch silently missing from a range can invert
# the strict-newer comparison and name the WRONG deletion candidate), so both GETs
# go through run_kubectl. But UNLIKE release_json, an EMPTY field here is NOT
# corruption and is NOT fatal -- because the consequence differs. When release_json
# returns empty for a present Secret the tenant is dropped from the report: a
# false-clean. When pv_epoch returns "" the claim merely contributes no epoch,
# which marks its whole generation INCOMPLETE (see audit_ns) and forces the safe
# "direction cannot be established -> classify by hand" branch. That degradation is
# conservative by construction: it never yields a false-clean and never names a
# candidate. So the two empty-field cases are deliberately kept benign:
#   * PVC .spec.volumeName empty -- a Pending / unbound claim, a routine state;
#   * PV .metadata.creationTimestamp empty -- shouldn't happen (the apiserver always
#     stamps it), but if it ever did the only effect is one missing epoch, i.e. the
#     same safe incomplete fallback, so a hard stop is not warranted here.
# `--ignore-not-found` keeps a genuinely-absent PVC/PV (NotFound) on that same
# benign path rather than turning it into a run_kubectl failure, and the final
# `|| return 0` keeps an unparseable timestamp there too; only a real run_kubectl
# error propagates.
pv_epoch() {
  pv=$(run_kubectl "get PVC '$2' in namespace '$1'" \
    get pvc -n "$1" "$2" --ignore-not-found -o jsonpath='{.spec.volumeName}') || return $?
  [ -n "$pv" ] || return 0
  t=$(run_kubectl "get PV '$pv' (bound by PVC '$2' in namespace '$1')" \
    get pv "$pv" --ignore-not-found -o jsonpath='{.metadata.creationTimestamp}') || return $?
  [ -n "$t" ] || return 0
  date -u -d "$(printf '%s' "$t" | cut -c1-19)" +%s 2>/dev/null || return 0
}

# classify_mixed_direction <lmin> <lmax> <rmin> <rmax> -- direction of a MIXED
# tenant, from the bound-PV creation-epoch ranges of the chart-named (l*) and
# release-named (r*) generations. Prints one of:
#
#   legacy-original   every release-named PV strictly newer -> it is the candidate
#                     duplicate (runbook Step 3)
#   renamed-original  every chart-named PV strictly newer -> S-damaged, the
#                     chart-named set is the candidate duplicate (Step 2a, then 2)
#   overlap           vintages interleave or touch -> no candidate. An interrupted
#                     Step 2 re-bind puts BOTH generations on original-vintage PVs.
#
# Purely relative — no clock, no window, no first_deployed. Ties (second-resolution
# timestamps) count as overlap: refusing a candidate is recoverable, naming the
# wrong one is not.
classify_mixed_direction() {
  if [ "$3" -gt "$2" ]; then printf 'legacy-original'
  elif [ "$1" -gt "$4" ]; then printf 'renamed-original'
  else printf 'overlap'
  fi
}

audit_ns() {
  ns="$1"
  _rels=$(system_releases "$ns") || return $?
  for rel in $_rels; do
    prefix=$(renamed_volume_prefix "$rel")
    legacy_pvcs=""; renamed_pvcs=""
    _pvcs=$(run_kubectl "list PVCs in namespace '$ns'" get pvc -n "$ns" -o name) || return $?
    for pvc in $(printf '%s\n' "$_pvcs" | sed 's|persistentvolumeclaim/||'); do
      case "$pvc" in
        "data1-${prefix}"*)      renamed_pvcs="$renamed_pvcs $pvc" ;;
        data1-seaweedfs-volume*) legacy_pvcs="$legacy_pvcs $pvc" ;;
      esac
    done
    legacy_sts=""; renamed_sts=""
    _sts_list=$(run_kubectl "list SeaweedFS StatefulSets in namespace '$ns'" \
      get sts -n "$ns" -l app.kubernetes.io/name=seaweedfs -o name) || return $?
    for sts in $(printf '%s\n' "$_sts_list" | sed 's|statefulset.apps/||'); do
      case "$sts" in
        "${prefix}"*)      renamed_sts="$renamed_sts $sts" ;;
        seaweedfs-volume*) legacy_sts="$legacy_sts $sts" ;;
      esac
    done
    has_legacy=0; has_renamed=0
    [ -n "$legacy_pvcs$legacy_sts" ] && has_legacy=1
    [ -n "$renamed_pvcs$renamed_sts" ] && has_renamed=1
    [ "$has_legacy" = 0 ] && [ "$has_renamed" = 0 ] && continue

    if [ "$has_legacy" = 1 ] && [ "$has_renamed" = 0 ]; then
      printf '%-24s %-14s %-8s %s\n' "$ns" "$rel" "L" "chart-named only; the upgrade adopts it, nothing to do"
      continue
    fi
    if [ "$has_renamed" = 1 ] && [ "$has_legacy" = 0 ]; then
      printf '%-24s %-14s %-8s %s\n' "$ns" "$rel" "S" "release-named only; re-bind the volumes (Step 2) BEFORE upgrading"
      continue
    fi

    # Both generations. Report which is ORIGINAL from durable evidence.
    fd=$(first_deployed "$ns" "$rel") || return $?
    scheme=$(rev1_scheme "$ns" "$rel") || return $?
    printf '%-24s %-14s %-8s %s\n' "$ns" "$rel" "MIXED" "both generations; the chart REFUSES until one is removed"
    if [ -n "$scheme" ]; then
      printf '%-24s %-14s %-8s   revision 1 was installed with the %s names => the %s generation is ORIGINAL\n' \
        "" "" "" "$scheme" "$scheme"
    fi
    # The direction rule is "EVERY PV of one generation strictly newer than every
    # PV of the other". That holds only if we saw EVERY bound PV: a claim whose PV
    # age we could not read (unbound, or PV gone) leaves the range unbounded on one
    # side, and a silently-narrowed range can flip OVERLAP into a confident (wrong)
    # candidate. So a generation with any unreadable claim is INCOMPLETE
    # and forces the safe "cannot establish" branch -- distinct from a real API
    # error, which pv_epoch has already turned into a hard failure above.
    lmin=""; lmax=""; rmin=""; rmax=""; l_incomplete=0; r_incomplete=0
    for p in $legacy_pvcs; do
      e=$(pv_epoch "$ns" "$p") || return $?
      if [ -z "$e" ]; then l_incomplete=1; continue; fi
      { [ -z "$lmin" ] || [ "$e" -lt "$lmin" ]; } && lmin=$e
      { [ -z "$lmax" ] || [ "$e" -gt "$lmax" ]; } && lmax=$e
    done
    for p in $renamed_pvcs; do
      e=$(pv_epoch "$ns" "$p") || return $?
      if [ -z "$e" ]; then r_incomplete=1; continue; fi
      { [ -z "$rmin" ] || [ "$e" -lt "$rmin" ]; } && rmin=$e
      { [ -z "$rmax" ] || [ "$e" -gt "$rmax" ]; } && rmax=$e
    done
    if [ -n "$fd" ] && [ -n "$lmin" ] && [ -n "$rmin" ]; then
      printf '%-24s %-14s %-8s   oldest PV vs first_deployed: chart-named +%ss, release-named +%ss (context only, not the rule)\n' \
        "" "" "" "$((lmin - fd))" "$((rmin - fd))"
    fi
    if [ -n "$lmin" ] && [ -n "$rmin" ] && [ "$l_incomplete" = 0 ] && [ "$r_incomplete" = 0 ]; then
      case $(classify_mixed_direction "$lmin" "$lmax" "$rmin" "$rmax") in
        legacy-original)
          printf '%-24s %-14s %-8s   every release-named PV is strictly newer => chart-named is ORIGINAL, the release-named set is the candidate duplicate (Step 3)\n' "" "" "" ;;
        renamed-original)
          printf '%-24s %-14s %-8s   every chart-named PV is strictly newer => release-named is ORIGINAL, the chart-named set is the candidate duplicate (Step 2a, then Step 2)\n' "" "" "" ;;
        overlap)
          printf '%-24s %-14s %-8s   PV vintages OVERLAP => no candidate. An interrupted Step 2 re-bind looks exactly like this (both generations on original PVs). Finish Step 2 if one is in progress; otherwise escalate. Do NOT run Step 2a.\n' "" "" "" ;;
      esac
    else
      printf '%-24s %-14s %-8s   direction cannot be established from PV ages: a generation has no bound PVs (Pending/unbound claims, or StatefulSets only) or a bound PV age could not be read. Resolve those claims or classify by hand. Do NOT run Step 2a.\n' "" "" ""
    fi
    printf '%-24s %-14s %-8s   CANDIDATE ONLY: "original" does not mean the other set is EMPTY. A duplicate that\n' "" "" ""
    printf '%-24s %-14s %-8s   served writes and later crashed looks identical here. Verify emptiness before deleting.\n' "" "" ""
  done
}

main() {
  printf '%-24s %-14s %-8s %s\n' NAMESPACE RELEASE CLASS NOTE
  printf '%-24s %-14s %-8s %s\n' --------- ------- ----- ----
  if [ "$#" -gt 0 ]; then
    for ns in "$@"; do audit_ns "$ns" || exit $?; done
  else
    _ns_list=$(run_kubectl 'list namespaces (whole-cluster mode)' get ns -o name) || exit $?
    for ns in $(printf '%s\n' "$_ns_list" | sed 's|namespace/||'); do audit_ns "$ns" || exit $?; done
  fi
}

# Sourced by hack/seaweedfs-naming-audit.bats to exercise the classifier directly.
[ "${SEAWEEDFS_AUDIT_LIB:-0}" = "1" ] || main "$@"
