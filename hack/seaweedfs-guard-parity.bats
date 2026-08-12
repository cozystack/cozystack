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

# template_code <file> -- the file with its {{/* ... */}} spans removed.
#
# A discriminator is forbidden as template LOGIC, and both charts carry long
# comment blocks arguing why these particular signals were rejected. Grepping the
# raw file cannot tell the argument from the thing argued against, so it reports
# a chart for documenting the rule it obeys.
#
# The span and not the line. Dropping every line that opens a comment is one
# character shorter and silently loses `{{- $x := .creationTimestamp }} {{/* why
# */}}`: the discriminator leaves with the comment, the checks below find
# nothing, and the balance count stays even because that line opens and closes.
# Neither the balance nor the canary sees it -- the only symptom is a chart that
# classifies on a forbidden signal and a test that says it does not.
template_code() {
  awk '
    {
      line = $0; out = ""
      while (1) {
        if (!inc) {
          if (match(line, /\{\{-? *\/\*/)) {
            out = out substr(line, 1, RSTART - 1)
            line = substr(line, RSTART + RLENGTH); inc = 1
          } else { out = out line; break }
        } else {
          if (match(line, /\*\/ *-?\}\}/)) { line = substr(line, RSTART + RLENGTH); inc = 0 }
          else break
        }
      }
      if (out ~ /[^[:space:]]/) print out
    }
  ' "$1"
}

@test "the comment strip removes the span, not the line that opens it" {
  # Pins the difference between the two spellings, because on today's charts
  # they agree: no line carries both a discriminator and a comment opener, so
  # the weaker version passes every check below. It is the shape that would go
  # wrong silently -- a chart classifying on a forbidden signal and this suite
  # reporting that it does not -- so it gets a fixture rather than trust.
  tmp="$(mktemp)"
  printf '%s\n' \
    '{{- $renamedVol := include "x" . }}' \
    '{{- $bad := .metadata.creationTimestamp }} {{/* why this is fine */}}' \
    '{{- /* a whole-line block' \
    '   mentioning readyReplicas */}}' > "$tmp"

  code="$(template_code "$tmp")"
  # A comment-only block still goes.
  if printf '%s\n' "$code" | grep -q 'readyReplicas'; then
    echo "FAIL: a comment-only block survived the strip"
    false
  fi
  # The code sharing a line with an opener does not.
  printf '%s\n' "$code" | grep -q 'creationTimestamp'
  printf '%s\n' "$code" | grep -q 'renamedVol'
  rm -f "$tmp"
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
    # The strip trusts every `{{/*` to be an opener. One inside a quoted string
    # -- these charts carry long `fail (printf ...)` messages -- would swallow
    # the rest of the file and leave the checks below reading nothing. Balance
    # is the precondition, so assert it rather than the output: a canary token
    # can only prove that whatever precedes IT survived, and the block most
    # likely to do the swallowing is the last one in the file.
    # `grep -o | wc -l` and not `grep -c`: grep -c counts matching LINES, so a
    # line carrying two openers and one closer reads as balanced while the
    # strip is left mid-comment, swallowing the rest of the file. Counting
    # occurrences is what the comparison below actually means. Unreachable in
    # either chart today -- no line holds more than one delimiter -- so nothing
    # here pins it; it is a correction to what the count means, not a fix for
    # an observed failure.
    #
    # No `|| true` is needed. `wc -l` closes the pipeline and always exits 0,
    # so a chart with no comment blocks at all reports 0 = 0 and reaches the
    # comparison rather than dying on the assignment. `grep`'s own exit 1 would
    # only reach the status under `pipefail`, which neither runner sets.
    opens=$(grep -o '{{-\? *\/\*' "$f" | wc -l)
    closes=$(grep -o '\*\/ *-\?}}' "$f" | wc -l)
    if [ "$opens" -ne "$closes" ]; then
      echo "FAIL: $f has $opens comment openers and $closes closers, so the strip below cannot be trusted"
      false
    fi
    code=$(template_code "$f")
    printf '%s\n' "$code" | grep -qF -- '$renamedVol'
    for signal in 'creationTimestamp' 'readyReplicas' '$systemOldest' '$legacyOldest'; do
      if printf '%s\n' "$code" | grep -qF -- "$signal"; then
        echo "FAIL: $f classifies on $signal, which cannot establish which generation holds the data"
        false
      fi
    done
  done
}

@test "both charts gate the guard behind the same cluster-view canary" {
  for f in "$SYS" "$EXTRA"; do
    grep -qF -- '$canary := lookup "v1" "Namespace" "" .Release.Namespace' "$f"
    grep -qF -- 'refusing to upgrade blind' "$f"
  done
}
