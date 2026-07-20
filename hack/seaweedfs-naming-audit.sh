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
# Exit:   0 always (audit only). Nothing is mutated.
#
# POSIX sh, deliberately. hack/seaweedfs-naming-audit.bats sources this file, and
# cozytest.sh sources the converted test into its own /bin/sh — which is dash on
# the CI runner. A bash shebang would not save it: `.` runs the file's contents in
# the caller's shell and the shebang is just a comment. Keeping the script POSIX
# is what makes the tested shell and the executed shell the same one; anything
# bash-only here is untested in CI and unavailable at run time. `pipefail` in
# particular is not POSIX and dash 0.5.12 (Ubuntu) rejects it outright.
set -u

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

# release_json <ns> <release> [rev] -- decode a Helm release secret.
# Helm stores base64(gzip(json)) in Secret.data.release, and Kubernetes base64s
# the data value again, hence the two decodes.
release_json() {
  kubectl get secret -n "$1" "sh.helm.release.v1.$2.v${3:-1}" -o jsonpath='{.data.release}' 2>/dev/null \
    | base64 -d 2>/dev/null | base64 -d 2>/dev/null | gunzip 2>/dev/null
}

# revisions <ns> <release> -- retained revision numbers, oldest first.
revisions() {
  kubectl get secret -n "$1" -l "name=$2,owner=helm" \
    -o jsonpath='{range .items[*]}{.metadata.labels.version}{"\n"}{end}' 2>/dev/null | sort -n
}

# system_releases <ns> -- the SeaweedFS <name>-system Helm releases in a namespace.
# Filtering on the `-system` suffix alone is not enough: every Cozystack app has a
# <name>-system release (ingress-nginx-system, bucket-*-system, ...), and since the
# generation scan below matches PVCs by NAME across the whole namespace, an
# unrelated release in a namespace that happens to run SeaweedFS would be reported
# as a SeaweedFS tenant. Confirm the chart.
system_releases() {
  for rel in $(kubectl get secret -n "$1" \
                 -o jsonpath='{range .items[?(@.type=="helm.sh/release.v1")]}{.metadata.labels.name}{"\n"}{end}' 2>/dev/null \
               | grep -E -- '-system$' | sort -u); do
    for rev in $(revisions "$1" "$rel"); do
      chart=$(release_json "$1" "$rel" "$rev" | sed -n 's/.*"name":"\(cozy-seaweedfs\)".*/\1/p' | head -1)
      if [ -n "$chart" ]; then printf '%s\n' "$rel"; fi
      break
    done
  done
}

# first_deployed <ns> <release> -- epoch seconds of the release's first install,
# from any retained revision (the field is identical on all of them).
first_deployed() {
  for rev in $(revisions "$1" "$2"); do
    ts=$(release_json "$1" "$2" "$rev" | sed -n 's/.*"first_deployed":"\([^"]*\)".*/\1/p' | head -1)
    if [ -n "$ts" ]; then date -u -d "$(printf '%s' "$ts" | cut -c1-19)" +%s 2>/dev/null; return; fi
  done
}

# rev1_scheme <ns> <release> -- "legacy" | "renamed" | "" (revision 1 pruned).
rev1_scheme() {
  m=$(release_json "$1" "$2" 1)
  [ -n "$m" ] || return 0
  if printf '%s' "$m" | grep -q 'name: seaweedfs-master'; then printf 'legacy'
  elif printf '%s' "$m" | grep -qE "name: $2(-seaweedfs)?-master"; then printf 'renamed'
  fi
}

# pv_epoch <ns> <pvc> -- creation epoch of the PV the claim is BOUND to.
pv_epoch() {
  pv=$(kubectl get pvc -n "$1" "$2" -o jsonpath='{.spec.volumeName}' 2>/dev/null)
  [ -n "$pv" ] || return 0
  t=$(kubectl get pv "$pv" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null)
  [ -n "$t" ] || return 0
  date -u -d "$(printf '%s' "$t" | cut -c1-19)" +%s 2>/dev/null
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
  for rel in $(system_releases "$ns"); do
    prefix=$(renamed_volume_prefix "$rel")
    legacy_pvcs=""; renamed_pvcs=""
    for pvc in $(kubectl get pvc -n "$ns" -o name 2>/dev/null | sed 's|persistentvolumeclaim/||'); do
      case "$pvc" in
        "data1-${prefix}"*)      renamed_pvcs="$renamed_pvcs $pvc" ;;
        data1-seaweedfs-volume*) legacy_pvcs="$legacy_pvcs $pvc" ;;
      esac
    done
    legacy_sts=""; renamed_sts=""
    for sts in $(kubectl get sts -n "$ns" -l app.kubernetes.io/name=seaweedfs -o name 2>/dev/null | sed 's|statefulset.apps/||'); do
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
    fd=$(first_deployed "$ns" "$rel")
    scheme=$(rev1_scheme "$ns" "$rel")
    printf '%-24s %-14s %-8s %s\n' "$ns" "$rel" "MIXED" "both generations; the chart REFUSES until one is removed"
    if [ -n "$scheme" ]; then
      printf '%-24s %-14s %-8s   revision 1 was installed with the %s names => the %s generation is ORIGINAL\n' \
        "" "" "" "$scheme" "$scheme"
    fi
    lmin=""; lmax=""; rmin=""; rmax=""
    for p in $legacy_pvcs; do
      e=$(pv_epoch "$ns" "$p"); [ -n "$e" ] || continue
      { [ -z "$lmin" ] || [ "$e" -lt "$lmin" ]; } && lmin=$e
      { [ -z "$lmax" ] || [ "$e" -gt "$lmax" ]; } && lmax=$e
    done
    for p in $renamed_pvcs; do
      e=$(pv_epoch "$ns" "$p"); [ -n "$e" ] || continue
      { [ -z "$rmin" ] || [ "$e" -lt "$rmin" ]; } && rmin=$e
      { [ -z "$rmax" ] || [ "$e" -gt "$rmax" ]; } && rmax=$e
    done
    if [ -n "$fd" ] && [ -n "$lmin" ] && [ -n "$rmin" ]; then
      printf '%-24s %-14s %-8s   oldest PV vs first_deployed: chart-named +%ss, release-named +%ss (context only, not the rule)\n' \
        "" "" "" "$((lmin - fd))" "$((rmin - fd))"
    fi
    if [ -n "$lmin" ] && [ -n "$rmin" ]; then
      case $(classify_mixed_direction "$lmin" "$lmax" "$rmin" "$rmax") in
        legacy-original)
          printf '%-24s %-14s %-8s   every release-named PV is strictly newer => chart-named is ORIGINAL, the release-named set is the candidate duplicate (Step 3)\n' "" "" "" ;;
        renamed-original)
          printf '%-24s %-14s %-8s   every chart-named PV is strictly newer => release-named is ORIGINAL, the chart-named set is the candidate duplicate (Step 2a, then Step 2)\n' "" "" "" ;;
        overlap)
          printf '%-24s %-14s %-8s   PV vintages OVERLAP => no candidate. An interrupted Step 2 re-bind looks exactly like this (both generations on original PVs). Finish Step 2 if one is in progress; otherwise escalate. Do NOT run Step 2a.\n' "" "" "" ;;
      esac
    else
      printf '%-24s %-14s %-8s   a generation has no bound PVs (Pending claims, or StatefulSets only) => direction cannot be established from PV ages. Resolve the Pending claims or classify by hand.\n' "" "" ""
    fi
    printf '%-24s %-14s %-8s   CANDIDATE ONLY: "original" does not mean the other set is EMPTY. A duplicate that\n' "" "" ""
    printf '%-24s %-14s %-8s   served writes and later crashed looks identical here. Verify emptiness before deleting.\n' "" "" ""
  done
}

main() {
  printf '%-24s %-14s %-8s %s\n' NAMESPACE RELEASE CLASS NOTE
  printf '%-24s %-14s %-8s %s\n' --------- ------- ----- ----
  if [ "$#" -gt 0 ]; then
    for ns in "$@"; do audit_ns "$ns"; done
  else
    for ns in $(kubectl get ns -o name 2>/dev/null | sed 's|namespace/||'); do audit_ns "$ns"; done
  fi
}

# Sourced by hack/seaweedfs-naming-audit.bats to exercise the classifier directly.
[ "${SEAWEEDFS_AUDIT_LIB:-0}" = "1" ] || main "$@"
