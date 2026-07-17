#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The SeaweedFS naming guard exists in two copies:
#
#   packages/system/seaweedfs/templates/naming-guard.yaml   (ENFORCING — the
#     <name>-system HelmRelease pulls this chart from a platform-managed
#     ExternalArtifact, so a platform upgrade re-renders it directly and nothing
#     else stands between the upgrade and the tenant's workloads)
#   packages/extra/seaweedfs/templates/seaweedfs.yaml       (sibling — warns on
#     the SeaweedFS application itself, where the operator looks first)
#
# They cannot be shared: the two packages are separate charts and neither depends
# on cozy-lib, so there is no library either could include the classifier from.
# They were kept in sync by a comment saying "keep the two classifications in
# sync" — and this whole branch exists because the guard lived in the wrong
# chart. Silent drift between the copies is the same failure class, so pin it.
#
# Only the DETECTION block is compared: the two `fail` messages differ by design
# ("SeaweedFS release <name>" vs "SeaweedFS <name>", matching each chart's voice)
# and the branch structure is asserted separately. It is the detection — which
# objects establish which generation — that must never diverge.
#
# The block starts at the reconstruction, not at the flag declarations: the line
# above it is deliberately different per chart (system/ IS the <name>-system
# release; extra/ is <name> and derives the child), and that is the ONLY sanctioned
# difference. Both are asserted separately below.
#
# Run with: hack/cozytest.sh hack/seaweedfs-guard-parity.bats
# -----------------------------------------------------------------------------

SYS="$PWD/packages/system/seaweedfs/templates/naming-guard.yaml"
EXTRA="$PWD/packages/extra/seaweedfs/templates/seaweedfs.yaml"

# detection_block <file> -- the generation-detection lines, from the first flag
# declaration through the two derived generation booleans.
detection_block() {
  sed -n '/\$renamedVol := include/,/\$systemGen := or/p' "$1"
}

@test "both charts detect naming generations with byte-identical logic" {
  a=$(mktemp); b=$(mktemp)
  detection_block "$SYS"   > "$a"
  detection_block "$EXTRA" > "$b"
  # Non-empty: a sed range that matched nothing would make this test vacuous.
  [ -s "$a" ]
  [ -s "$b" ]
  diff -u "$a" "$b"
  rm -f "$a" "$b"
}

@test "each chart feeds the reconstruction the <name>-system release name" {
  # system/seaweedfs IS that release; extra/seaweedfs is <name> and must derive it.
  # Getting this wrong silently reconstructs the wrong prefix, so neither
  # generation matches and the guard renders through.
  grep -qF -- '$sysRelease := .Release.Name' "$SYS"
  grep -qF -- '$sysRelease := printf "%s-system" .Release.Name' "$EXTRA"
}

@test "both charts reconstruct the renamed prefix rather than prefix-matching alone" {
  # An instance legitimately named `seaweedfs-volume` renders release-named objects
  # (seaweedfs-volume-system-volume, data1-seaweedfs-volume-system-volume-0) that
  # ALSO satisfy the chart-named prefixes. Release-named must be tested FIRST,
  # against a reconstructed prefix, or live storage reads as legacy.
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- '$renamedVol := include "seaweedfs.renamedVolumePrefix" $sysRelease' "$f"
    # release-named branch precedes the chart-named fallback in both scans
    pv=$(grep -n 'hasPrefix (printf "data1-%s" $renamedVol)' "$f" | cut -d: -f1)
    lv=$(grep -n 'hasPrefix "data1-seaweedfs-volume"' "$f" | cut -d: -f1)
    [ -n "$pv" ] && [ -n "$lv" ] && [ "$pv" -lt "$lv" ]
    ps=$(grep -n 'hasPrefix $renamedVol .metadata.name' "$f" | cut -d: -f1)
    ls=$(grep -n 'hasPrefix "seaweedfs-volume" .metadata.name' "$f" | cut -d: -f1)
    [ -n "$ps" ] && [ -n "$ls" ] && [ "$ps" -lt "$ls" ]
  done
}

@test "both charts derive the generation flags from PVC and StatefulSet evidence" {
  # The OR is load-bearing: a tenant whose PVCs are not provisioned yet is only
  # visible through its label-matched StatefulSet.
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- '$legacyGen := or $legacyPVC $legacySTS' "$f"
    grep -qF -- '$systemGen := or $systemPVC $systemSTS' "$f"
  done
}

@test "both charts refuse when both generations are present" {
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- 'if and $legacyGen $systemGen' "$f"
    grep -qF -- 'has BOTH naming generations present' "$f"
  done
}

@test "both charts refuse a release-named-only tenant (class S)" {
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- 'else if $systemGen' "$f"
    grep -qF -- 'keeps its data on volumes named after the Helm release' "$f"
  done
}

@test "neither chart classifies on mutable claim timestamps or liveness" {
  # The premise "PVCs are never recreated in place" is false: the runbook's own
  # Step 2 re-bind deletes and recreates each claim, so a tenant interrupted
  # part-way through reads as the exact inverse of the truth — and the step the
  # old classification pointed at deletes the claim Step 2 just re-bound.
  # readyReplicas is likewise only a snapshot, not proof a duplicate never
  # served. Neither may come back as a discriminator.
  for f in "$SYS" "$EXTRA"; do
    ! grep -qF -- 'creationTimestamp' "$f"
    ! grep -qF -- 'readyReplicas' "$f"
    ! grep -qF -- '$systemOldest' "$f"
    ! grep -qF -- '$legacyOldest' "$f"
  done
}

@test "both charts gate the guard behind the same cluster-view canary" {
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- '$canary := lookup "v1" "Namespace" "" .Release.Namespace' "$f"
    grep -qF -- 'refusing to upgrade blind' "$f"
  done
}
