#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/seaweedfs-naming-audit.sh.
#
# The chart's naming guard refuses whenever both naming generations exist, so the
# audit IS the classifier — it is what an operator acts on, and acting on it
# deletes PVCs. Two earlier revisions of this classification shipped as an
# untested shell snippet inside the runbook, and both were wrong in ways that
# routed a live tenant into the step that strands its data:
#
#   - a selector `^data1-(.*seaweedfs.*)-volume` also matched the CHART-named
#     claims, so the release-named age range spanned both generations, every
#     tenant read as "ranges overlap / mid-rebind", and a genuine S-damaged tenant
#     was routed AWAY from Step 2a (which would have been correct) into Step 2,
#     which deletes its data claim;
#   - matching claims by name with `grep seaweedfs` cannot see a long instance
#     name, whose claims the chart truncates past `seaweedfs`, so such a tenant
#     read as "L — nothing to do" while the chart refused it.
#
# Moving the classifier into a file with tests is the point. These drive it
# against a fake kubectl, mocking only the cluster boundary.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line; there is no bats `run`/`$status`/`setup`.
#
# Run with: hack/cozytest.sh hack/seaweedfs-naming-audit.bats
# -----------------------------------------------------------------------------

SEAWEEDFS_AUDIT_LIB=1
export SEAWEEDFS_AUDIT_LIB
# shellcheck source=seaweedfs-naming-audit.sh
. "$PWD/hack/seaweedfs-naming-audit.sh"

@test "reconstructs the renamed volume prefix for a default instance" {
  [ "$(renamed_volume_prefix seaweedfs-system)" = "seaweedfs-system-volume" ]
}

@test "reconstructs the renamed volume prefix for a non-default instance" {
  # The release name does not contain the chart name, so 4.31 appends it.
  [ "$(renamed_volume_prefix foo-system)" = "foo-system-seaweedfs-volume" ]
}

@test "reconstructs the truncated prefix for a long instance name" {
  # componentName cuts the fullname to 62-len("volume")=56 before appending, so
  # `seaweedfs` falls off the tail — the case a `grep seaweedfs` name match cannot
  # see, and the reason this is reconstructed rather than pattern-matched.
  got=$(renamed_volume_prefix archive-of-quarterly-financial-statements-x1-system)
  [ "$got" = "archive-of-quarterly-financial-statements-x1-system-seaw-volume" ]
  # 56 chars of fullname + "-volume"
  [ "${#got}" -eq 63 ]
}

@test "reconstructs a distinct prefix for an instance named seaweedfs-volume" {
  # The pathological case: this instance's RELEASE-named objects
  # (seaweedfs-volume-system-volume, data1-seaweedfs-volume-system-volume-0) also
  # satisfy the CHART-named prefixes. The reconstruction must return the
  # release-named prefix so the release-named branch can be tested first.
  got=$(renamed_volume_prefix seaweedfs-volume-system)
  [ "$got" = "seaweedfs-volume-system-volume" ]
  # It must NOT collide with the chart-named prefix.
  [ "$got" != "seaweedfs-volume" ]
}

@test "the reconstructed prefix never equals the chart-named prefix" {
  # If it did, both generations would match one branch and the guard/audit could
  # not separate them at all.
  for r in seaweedfs-system foo-system seaweedfs-volume-system a-system; do
    [ "$(renamed_volume_prefix "$r")" != "seaweedfs-volume" ]
  done
}

@test "clean duplicate: release-named PVs all strictly newer => legacy is original" {
  # A tenant that passed through the 4.31 rename: legacy PVs at install time,
  # duplicate PVs provisioned by the bad upgrade much later.
  [ "$(classify_mixed_direction 1000 1010 5000 5020)" = "legacy-original" ]
}

@test "S-damaged: chart-named PVs all strictly newer => release-named is original" {
  # Installed fresh on 1.5.x (release-named PVs first); an unguarded 1.6 upgrade
  # then created empty chart-named claims beside them.
  [ "$(classify_mixed_direction 5000 5020 1000 1010)" = "renamed-original" ]
}

@test "interrupted Step 2 re-bind: overlapping vintages => no candidate" {
  # Step 2 re-binds release-named claims onto their ORIGINAL PVs under chart
  # names, one claim at a time. Interrupted part-way, BOTH generations sit on
  # original-vintage PVs, so the ranges interleave. The old absolute-window
  # classifier fell through to a coin flip here and its advice deleted the
  # un-re-bound claims; the relative rule must refuse instead.
  [ "$(classify_mixed_direction 1000 1010 1005 1015)" = "overlap" ]
}

@test "a tie is overlap, not a candidate" {
  # Second-resolution timestamps: a duplicate provisioned within the same second
  # as the newest original PV is not STRICTLY newer. Refusing is recoverable;
  # naming the wrong candidate is not.
  [ "$(classify_mixed_direction 1000 1010 1010 1020)" = "overlap" ]
}

@test "direction needs no clock: vintages far from any anchor still classify" {
  # The shipped StorageClasses are WaitForFirstConsumer, so PVs appear at
  # pod-SCHEDULE time — on a cold cluster minutes after first_deployed. The rule
  # must not care: only the two generations' ranges relative to EACH OTHER count.
  # (The previous classifier anchored a 120s window on first_deployed and
  # misclassified exactly this case.)
  [ "$(classify_mixed_direction 100000 100600 200000 200600)" = "legacy-original" ]
}

@test "the audit's reconstruction agrees with the chart helper it mirrors" {
  # hack/seaweedfs-naming-audit.sh and
  # packages/system/seaweedfs/templates/_naming.tpl reimplement the same two
  # upstream helpers in two languages. If they drift, the audit classifies a
  # tenant differently from the render that refuses it, and the operator is
  # working from a different picture than the chart. Render the chart helper
  # through helm and compare it to this script's output, release by release.
  chart=$(mktemp -d)
  printf 'apiVersion: v2\nname: probe\nversion: 0.0.0\n' > "$chart/Chart.yaml"
  mkdir -p "$chart/templates"
  cp packages/system/seaweedfs/templates/_naming.tpl "$chart/templates/"
  cat > "$chart/templates/out.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: probe
data:
{{- range $r := list "seaweedfs-system" "foo-system" "seaweedfs-volume-system" "archive-of-quarterly-financial-statements-x1-system" "a-system" }}
  {{ $r }}: {{ include "seaweedfs.renamedVolumePrefix" $r }}
{{- end }}
EOF
  helm template probe "$chart" 2>/dev/null | sed -n 's/^  \([a-z0-9-]*-system\): \(.*\)$/\1=\2/p' > "$chart/rendered"
  [ -s "$chart/rendered" ]
  [ "$(wc -l < "$chart/rendered")" -eq 5 ]
  while IFS='=' read -r rel expected; do
    [ -n "$rel" ] || continue
    got=$(renamed_volume_prefix "$rel")
    echo "chart: $rel -> $expected ; audit: $got"
    [ "$got" = "$expected" ]
  done < "$chart/rendered"
  rm -rf "$chart"
}

# -----------------------------------------------------------------------------
# Fail-closed tests (issue #3431).
#
# The classification tests above mock nothing below the classifier. These drive
# the KUBECTL layer, where the fail-open bug lived: a kubectl call that failed
# used to return empty stdout, byte-identical to a genuinely clean fleet, so the
# audit printed an empty table and exited 0 -- the exact false "nothing to do" the
# operator is told to trust before deleting PVCs. They shim `kubectl` on PATH with
# a fake and assert the audit fails LOUDLY (non-zero exit + a FATAL naming the
# query) on any real error, while still treating a genuinely-absent object as
# clean. Two families:
#
#   * enumerating LISTs (get ns / secret / pvc / sts) -- a failure is always fatal;
#   * by-NAME GETs (a release secret in system_releases, a PVC/PV in pv_epoch) --
#     `--ignore-not-found` splits a real error (fatal) from a legitimate absence:
#     an absent release revision is a clean skip; an absent PV degrades to the
#     safe "cannot establish direction" note and must NOT become a deletion
#     candidate (the range-narrowing flip Codex reproduced).
#
# The two golden tests guard the reverse: on a healthy cluster the output stays
# byte-for-byte what it was on origin/main, including the MIXED path that exercises
# every pv_epoch GET.
#
# The fake is a real executable on PATH, so it exercises the actual exit-status
# handling in run_kubectl and the propagation up through every caller -- a for
# loop over `$(...)` swallows the status, so this is where a regression would hide.

# _release_blob [chart-name] -- the value kubectl returns for a Helm release
# secret's `.data.release`: base64(base64(gzip(json))). release_json base64-decodes
# twice and gunzips it, and system_releases reads the chart name from
# chart.metadata.name (as real Helm payloads carry it). Defaults to a cozy-seaweedfs
# release so the audit confirms the tenant; pass another chart name for a non-
# SeaweedFS release, or the literal EMPTY for a chartless {} payload.
_release_blob() {
  _rb_name=${1:-cozy-seaweedfs}
  if [ "$_rb_name" = EMPTY ]; then
    _rb_json='{}'
  else
    _rb_json='{"name":"seaweedfs-system","chart":{"metadata":{"name":"'"$_rb_name"'"}}}'
  fi
  printf '%s' "$_rb_json" | gzip | base64 | base64 | tr -d '\n'
}

# _write_fake_kubectl <dir> <fail> <absent-pv> <blob> <pvcs> -- drop a fake kubectl
# into <dir>.
#   <fail>       one of ns | secretlist | secretget | secretget_absent |
#                secret_missing_field | secret_bad_payload | pvclist | pvcget |
#                sts | pvget | "" -- the single call to make behave badly. Real-
#                error targets exit non-zero with a stderr message; secretget_absent
#                mirrors an absent Secret (--ignore-not-found: exit 0, empty -o
#                name); secret_missing_field / secret_bad_payload keep the Secret
#                present but return an empty / undecodable .data.release payload.
#   <absent-pv>  a PV name (pv-legacy|pv-legacy2|pv-renamed) to report as absent
#                (exit 0, empty), exercising the "bound claim, PV gone" path.
#   <pvcs>       newline-separated `get pvc -o name` LIST output.
# It otherwise walks one confirmed seaweedfs-system tenant. The by-name PVC->PV and
# PV->timestamp maps are fixed here; timestamps are chosen so the two legacy PVs
# straddle the single renamed PV (true answer OVERLAP), which is what makes the
# range-narrowing flip observable. Kept POSIX and free of a column-0 `}` so
# cozytest.sh's awk converter passes the heredoc through untouched.
_write_fake_kubectl() {
  # Grouped redirect (one open of the file). The closing brace is indented, so
  # cozytest.sh's awk -- which only rewrites a `}` in column 0 -- leaves it and the
  # heredoc alone.
  {
    printf '#!/bin/sh\n'
    printf "FAIL='%s'\n" "$2"
    printf "ABSENT_PV='%s'\n" "$3"
    printf "BLOB='%s'\n" "$4"
    printf "PVCS='%s'\n" "$5"
    cat <<'FAKE'
verb=${1:-}; res=${2:-}; args="$*"
fail() { echo "fake kubectl: $1 (real error)" >&2; exit 1; }
if [ "$verb $res" = "get ns" ]; then
  [ "$FAIL" = ns ] && fail "get ns"
  printf 'namespace/tenant-test\n'; exit 0
fi
if [ "$verb $res" = "get secret" ]; then
  case "$args" in
    *sh.helm.release.v1*)
      # release_json now asks two questions: existence (-o name) then payload
      # (jsonpath .data.release). Mirror that split so absence, real error, and
      # corrupt-payload are all reachable independently.
      case "$args" in
        *"-o name"*)
          [ "$FAIL" = secretget ] && fail "release secret existence GET"
          [ "$FAIL" = secretget_absent ] && exit 0
          printf 'secret/sh.helm.release.v1.seaweedfs-system.v1\n'; exit 0 ;;
        *)
          [ "$FAIL" = secret_missing_field ] && exit 0
          if [ "$FAIL" = secret_bad_payload ]; then printf '@@@not-base64@@@\n'; exit 0; fi
          printf '%s\n' "$BLOB"; exit 0 ;;
      esac ;;
    *owner=helm*) printf '1\n'; exit 0 ;;
    *) [ "$FAIL" = secretlist ] && fail "namespace secret LIST"
       printf 'seaweedfs-system\n'; exit 0 ;;
  esac
fi
if [ "$verb $res" = "get pvc" ]; then
  case "$args" in
    *"-o name"*)
      [ "$FAIL" = pvclist ] && fail "pvc LIST"
      [ -n "$PVCS" ] && printf '%s\n' "$PVCS"
      exit 0 ;;
    *data1-seaweedfs-system-volume-0*) [ "$FAIL" = pvcget ] && fail "pvc GET"; printf 'pv-renamed\n'; exit 0 ;;
    *data1-seaweedfs-volume-1*)        [ "$FAIL" = pvcget ] && fail "pvc GET"; printf 'pv-legacy2\n'; exit 0 ;;
    *data1-seaweedfs-volume-0*)        [ "$FAIL" = pvcget ] && fail "pvc GET"; printf 'pv-legacy\n'; exit 0 ;;
  esac
  exit 0
fi
if [ "$verb $res" = "get sts" ]; then
  [ "$FAIL" = sts ] && fail "sts LIST"
  exit 0
fi
if [ "$verb $res" = "get pv" ]; then
  [ "$FAIL" = pvget ] && fail "get pv"
  case "$args" in
    *pv-legacy2*) [ "$ABSENT_PV" = pv-legacy2 ] && exit 0; printf '2099-01-01T00:00:00Z\n'; exit 0 ;;
    *pv-legacy*)  [ "$ABSENT_PV" = pv-legacy ]  && exit 0; printf '2020-01-01T00:00:00Z\n'; exit 0 ;;
    *pv-renamed*) [ "$ABSENT_PV" = pv-renamed ] && exit 0; printf '2020-06-01T00:00:00Z\n'; exit 0 ;;
  esac
  exit 0
fi
exit 0
FAKE
  } > "$1/kubectl"
  chmod +x "$1/kubectl"
}

# _pvcs_mixed -- the `get pvc -o name` LIST for a MIXED tenant: two legacy claims
# and one release-named claim, whose PV vintages truly overlap.
_pvcs_mixed() {
  printf '%s\n' \
    persistentvolumeclaim/data1-seaweedfs-volume-0 \
    persistentvolumeclaim/data1-seaweedfs-volume-1 \
    persistentvolumeclaim/data1-seaweedfs-system-volume-0
}

# _expected_L / _expected_mixed_overlap -- golden output built with the SAME printf
# contract the script uses, so a drift in row count, text, or padding fails the diff.
_expected_L() {
  printf '%-24s %-14s %-8s %s\n' NAMESPACE RELEASE CLASS NOTE
  printf '%-24s %-14s %-8s %s\n' --------- ------- ----- ----
  printf '%-24s %-14s %-8s %s\n' tenant-test seaweedfs-system L 'chart-named only; the upgrade adopts it, nothing to do'
}
_expected_mixed_overlap() {
  printf '%-24s %-14s %-8s %s\n' NAMESPACE RELEASE CLASS NOTE
  printf '%-24s %-14s %-8s %s\n' --------- ------- ----- ----
  printf '%-24s %-14s %-8s %s\n' tenant-test seaweedfs-system MIXED 'both generations; the chart REFUSES until one is removed'
  printf '%-24s %-14s %-8s   %s\n' '' '' '' 'PV vintages OVERLAP => no candidate. An interrupted Step 2 re-bind looks exactly like this (both generations on original PVs). Finish Step 2 if one is in progress; otherwise escalate. Do NOT run Step 2a.'
  printf '%-24s %-14s %-8s   %s\n' '' '' '' 'CANDIDATE ONLY: "original" does not mean the other set is EMPTY. A duplicate that'
  printf '%-24s %-14s %-8s   %s\n' '' '' '' 'served writes and later crashed looks identical here. Verify emptiness before deleting.'
}

@test "fails closed when 'kubectl get ns' fails (whole-cluster mode)" {
  # No namespace args -> main enumerates namespaces itself. If that LIST fails,
  # the OLD code audited zero namespaces and still printed an empty clean table.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" ns "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'list namespaces'
}

@test "fails closed when the namespace-wide secret LIST fails" {
  # system_releases enumerates Helm releases with a namespace-wide secret LIST --
  # the exact call that timed out in the field and reported a false clean.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" secretlist "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'list Helm release secrets'
}

@test "fails closed when the by-name release-secret existence GET hits a real error" {
  # Codex Finding 1: system_releases uses release_json to decide whether a release
  # IS SeaweedFS. Both LISTs succeed, then a transient/forbidden by-name Secret GET
  # used to leave `chart` empty -> the tenant was silently skipped -> exit 0, empty
  # table. A real error here must now be fatal, not a false clean.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" secretget "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'Helm release secret'
}

@test "an absent release-secret revision is a clean skip, not a failure" {
  # The one legitimately-empty case for release_json: the revision secret does not
  # exist (pruned by Helm history limit). The existence check (--ignore-not-found -o
  # name) returns empty + exit 0, so the release is simply not confirmed as
  # SeaweedFS. No error, no crash -- the audit completes and reports nothing here.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" secretget_absent "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -eq 0 ]
  [ "$(printf '%s\n' "$out" | grep -c 'FATAL')" -eq 0 ]
}

@test "fails closed when a present release secret has no .data.release payload" {
  # Codex re-review round 2: the existence check passes (Secret EXISTS), but its
  # .data.release field is empty/missing. That is CORRUPT state, not an absence --
  # and because the earlier fix used --ignore-not-found -o jsonpath, which returns
  # empty+0 for BOTH, it used to read as a clean skip. It must be a loud stop.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" secret_missing_field "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'no .data.release payload'
}

@test "fails closed when a present release secret has an undecodable payload" {
  # Existence passes, .data.release is non-empty but is not base64(base64(gzip(...))),
  # so every decode fails. The old `|| return 0` converted that decode failure into
  # a clean 0 return -> silent skip. A corrupt payload must be fatal.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" secret_bad_payload "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'not decodable'
}

@test "fails closed when a present release secret decodes to a chartless payload" {
  # Codex round 3: the payload fetches and DECODES cleanly (valid JSON {}), so the
  # decode guards all pass -- but it carries no chart name. system_releases then
  # extracted chart='' and silently skipped the tenant -> exit 0, clean table. A
  # decoded helm.sh/release.v1 release without a chart name is corrupt: fatal.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" "" "" "$(_release_blob EMPTY)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'no chart name'
}

@test "a present release secret for a non-SeaweedFS chart is a silent legitimate skip" {
  # The counterpart the guard must NOT break: a real, well-formed release whose
  # chart is simply not cozy-seaweedfs. It has a chart name (so it is not corrupt),
  # but the wrong one, so it is filtered out exactly as before -- no row, no error.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" "" "" "$(_release_blob cozy-postgres)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -eq 0 ]
  [ "$(printf '%s\n' "$out" | grep -c 'FATAL')" -eq 0 ]
  # Not confirmed as SeaweedFS -> no classification row for the release.
  [ "$(printf '%s\n' "$out" | grep -c 'seaweedfs-system')" -eq 0 ]
}

@test "fails closed when 'kubectl get pvc' LIST fails" {
  # The secret enumeration succeeds and confirms a seaweedfs-system tenant, so the
  # audit reaches the per-namespace PVC LIST; a failure there must abort, not read
  # as "this tenant has no claims".
  d=$(mktemp -d)
  _write_fake_kubectl "$d" pvclist "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'list PVCs'
}

@test "fails closed when 'kubectl get sts' LIST fails" {
  # PVC LIST succeeds (empty), so the audit reaches the StatefulSet LIST; a
  # failure there must abort rather than read as "no StatefulSets".
  d=$(mktemp -d)
  _write_fake_kubectl "$d" sts "" "$(_release_blob)" ""
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q 'StatefulSets'
}

@test "fails closed when the by-name PV GET hits a real error" {
  # Codex Finding 2, error half: pv_epoch reads each bound PV's age by name. A real
  # error (RBAC/timeout) must abort, not silently drop that PV from the range.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" pvget "" "$(_release_blob)" "$(_pvcs_mixed)"
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$out" | grep -q 'FATAL'
  printf '%s\n' "$out" | grep -q "get PV '"
}

@test "an absent PV degrades to the safe fallback, never a wrong candidate" {
  # Codex Finding 2, absence half. True vintages: legacy PVs 2020 + 2099 straddle
  # the single renamed PV 2020-06 => OVERLAP. Report the newest legacy PV (2099) as
  # ABSENT: the observed legacy range collapses to {2020} and, unguarded, "every
  # release-named PV is strictly newer" would fire -- naming the release-named set a
  # deletion candidate on incomplete evidence. The generation must instead read as
  # incomplete and fall to "cannot establish direction".
  d=$(mktemp -d)
  _write_fake_kubectl "$d" "" pv-legacy2 "$(_release_blob)" "$(_pvcs_mixed)"
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  rm -rf "$d"
  echo "rc=$rc"; echo "$out"
  [ "$rc" -eq 0 ]
  [ "$(printf '%s\n' "$out" | grep -c 'FATAL')" -eq 0 ]
  printf '%s\n' "$out" | grep -q 'direction cannot be established from PV ages'
  # The whole point: incomplete evidence must NOT be reported as a deletion candidate.
  [ "$(printf '%s\n' "$out" | grep -c 'candidate duplicate')" -eq 0 ]
}

@test "success path (L) is byte-identical to the expected fixture" {
  # A single chart-named claim, no release-named one, no StatefulSets => L, adopt in
  # place. Full-output compare, so an extra row / warning / duplicate line fails.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" "" "" "$(_release_blob)" "persistentvolumeclaim/data1-seaweedfs-volume-0"
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  printf '%s\n' "$out" > "$d/got"
  _expected_L > "$d/want"
  echo "rc=$rc"; echo "--- got ---"; cat "$d/got"; echo "--- want ---"; cat "$d/want"
  [ "$rc" -eq 0 ]
  diff "$d/want" "$d/got"
  rm -rf "$d"
}

@test "success path (MIXED/overlap) is byte-identical and exercises pv_epoch" {
  # Both generations present with all PVs readable => the MIXED path runs every
  # pv_epoch GET and lands on OVERLAP. Full-output compare against the golden.
  d=$(mktemp -d)
  _write_fake_kubectl "$d" "" "" "$(_release_blob)" "$(_pvcs_mixed)"
  rc=0
  out=$(PATH="$d:$PATH" SEAWEEDFS_AUDIT_LIB=0 sh "$PWD/hack/seaweedfs-naming-audit.sh" tenant-test 2>&1) || rc=$?
  printf '%s\n' "$out" > "$d/got"
  _expected_mixed_overlap > "$d/want"
  echo "rc=$rc"; echo "--- got ---"; cat "$d/got"; echo "--- want ---"; cat "$d/want"
  [ "$rc" -eq 0 ]
  diff "$d/want" "$d/got"
  rm -rf "$d"
}
