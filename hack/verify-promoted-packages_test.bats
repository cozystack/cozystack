#!/usr/bin/env bats
# Behavioural tests for the pre-publication packages artifact verification.
# Run with: hack/cozytest.sh hack/verify-promoted-packages_test.bats

_test_workspace() {
  if [ -n "${BATS_TEST_TMPDIR:-}" ]; then
    printf '%s\n' "$BATS_TEST_TMPDIR"
  else
    printf '%s\n' "${tmp:?cozytest workspace is missing}"
  fi
}

_make_verify_fixture() {
  t="$1"
  mkdir -p "$t/bin" "$t/release/core/installer" "$t/release/system/dashboard"
  mkdir -p "$t/artifact/core/installer" "$t/artifact/system/dashboard"
  mkdir -p "$t/rc-artifact/core/installer" "$t/rc-artifact/system/dashboard"
  cat > "$t/bin/flux" <<'EOF'
#!/bin/sh
set -eu
[ "$1" = "pull" ]
[ "$2" = "artifact" ]
shift 2
ref="$1"
printf '%s\n' "$@" >> "$MOCK_FLUX_LOG"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    out="$2"
    shift 2
  else
    shift
  fi
done
[ -n "$out" ]
case "$ref" in
  *@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)
    src="$MOCK_ARTIFACT"
    ;;
  *@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)
    src="$MOCK_RC_ARTIFACT"
    ;;
  *)
    echo "unexpected artifact ref: $ref" >&2
    exit 1
    ;;
esac
cp -R "$src"/. "$out"/
EOF
  chmod +x "$t/bin/flux"
  cat > "$t/release/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
EOF
  cat > "$t/artifact/core/installer/values.yaml" <<'EOF'
cozystackOperator:
  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages
  platformSourceRef: digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
  image='ghcr.io/cozystack/cozystack/cozystack-ui:v9.9.9@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
  printf 'console:\n  image: %s\n' "$image" > "$t/release/system/dashboard/values.yaml"
  printf 'console:\n  image: %s\n' "$image" > "$t/artifact/system/dashboard/values.yaml"
  cp "$t/artifact/core/installer/values.yaml" "$t/rc-artifact/core/installer/values.yaml"
  printf 'console:\n  image: %s\n' \
    'ghcr.io/cozystack/cozystack/cozystack-ui:v9.9.9-rc.3@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
    > "$t/rc-artifact/system/dashboard/values.yaml"
  # Flux's established packages archive omits symlinks. The verifier must
  # compare against that archive view rather than demanding this node back.
  ln -s values.yaml "$t/release/system/dashboard/values-link.yaml"
}

@test "accepts an identical candidate with only the self-reference changed" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "verification exited $rc" >&2
    cat "$tmp/err" >&2
    return "$rc"
  fi

  grep -qx 'oci://ghcr.io/cozystack/cozystack/cozystack-packages@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' "$tmp/flux.log"
  grep -q "stable tags, identical package tree, and the rc artifact's container digests" "$tmp/out"
}

@test "rejects an rc image reference inside the candidate" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  sed 's/:v9.9.9@/:v9.9.9-rc.3@/' "$tmp/artifact/system/dashboard/values.yaml" \
    > "$tmp/rc-values.yaml"
  mv "$tmp/rc-values.yaml" "$tmp/artifact/system/dashboard/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'still carries rc image references' "$tmp/err"
}

# The scan's `|| true` is not optional — `find … -exec … +` reports the
# legitimate "nothing matched" as a failure too — which leaves grep's own
# diagnostics as the only remaining signal that a file went unscanned. Produce
# that signal the way an unreadable file produces it, without depending on the
# runner's uid: root reads a chmod 000 fixture anyway, so a permissions-based
# test would quietly stop testing anything wherever the suite runs as root.
# A grep on PATH answering the scan's invocation the way the real one answers a
# file it cannot open is the same injection idiom as _tooling_with_collector
# below, one dependency further out.
_grep_failing_the_rc_scan() {
  real_grep="$(command -v grep)"
  cat > "$1/grep" <<EOF
#!/bin/sh
# Only the scan, which find invokes as: grep -lE -- <pattern> <files...>. Every
# other grep in the verifier, and in the collector it sources, must behave.
if [ "\$1" = "-lE" ]; then
  for _a in "\$@"; do _last="\$_a"; done
  echo "grep: \$_last: Permission denied" >&2
  exit 2
fi
exec $real_grep "\$@"
EOF
  chmod +x "$1/grep"
}

@test "refuses when the rc-reference scan could not read a file" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  _grep_failing_the_rc_scan "$tmp/bin"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'could not scan the promoted artifact for rc image references' "$tmp/err"
  # The diagnostic itself has to reach the log, or the failure names no file and
  # nobody can tell which one went unscanned.
  grep -q 'Permission denied' "$tmp/err"
  # …and the verifier did not go on to report a proof it skipped part of.
  # Counted rather than negated with `!`, which suppresses errexit and would
  # pass regardless.
  count="$(grep -c 'identical package tree' "$tmp/out" || true)"
  [ "${count:-0}" -eq 0 ]
}

# The threat this whole job exists for: someone edits packages/ on the release
# PR after promotion published the candidate, so the artifact the installer
# resolves is no longer the tree that was reviewed and tagged. A file present on
# BOTH sides with different bytes is the shape that takes, and only the per-file
# comparison catches it — the file-set compare sees the same names, the installer
# compare reads one file, and the rc-refs compare never looks at the release tree
# at all. The edit here is deliberately not an image reference, so no other leg
# can fire and claim the credit.
@test "rejects a file whose content changed on only one side" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  printf '  replicas: 3\n' >> "$tmp/release/system/dashboard/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'promoted artifact file differs from release tree: system/dashboard/values.yaml' "$tmp/err"
}

@test "rejects package content changed after the candidate was published" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  printf 'changed: true\n' > "$tmp/release/system/dashboard/extra.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'contain different files' "$tmp/err"
}

@test "rejects a candidate outside the trusted release repository" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  sed 's#oci://ghcr.io/cozystack/cozystack/cozystack-packages#oci://example.invalid/cozystack-packages#' \
    "$tmp/release/core/installer/values.yaml" > "$tmp/untrusted-values.yaml"
  mv "$tmp/untrusted-values.yaml" "$tmp/release/core/installer/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'must equal trusted repository' "$tmp/err"
  [ ! -s "$tmp/flux.log" ]
}

@test "rejects an embedded rc baseline outside the trusted release repository" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  sed 's#oci://ghcr.io/cozystack/cozystack/cozystack-packages#oci://example.invalid/cozystack-packages#' \
    "$tmp/artifact/core/installer/values.yaml" > "$tmp/untrusted-values.yaml"
  mv "$tmp/untrusted-values.yaml" "$tmp/artifact/core/installer/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "candidate's original platformSourceUrl.*must equal trusted repository" "$tmp/err"
}

# The mode check has to catch BOTH directions, and only one of them is the
# dangerous one. A candidate whose file is executable where the release tree's
# is not is content the merge reviewer never saw made runnable inside the
# artifact the operator installs — cmp(1) compares bytes and says nothing about
# it. The mirror case is the benign one, and it was the only one an earlier
# `&&`/`||` chain actually rejected: POSIX gives the two operators equal
# precedence and left associativity, so the guard grouped as
# `((A && B) || C) && D` and quietly accepted a candidate-only executable bit.
@test "rejects an executable bit set only in the candidate" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  chmod +x "$tmp/artifact/system/dashboard/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'executable bit differs from release tree' "$tmp/err"
}

@test "rejects an executable bit set only in the release tree" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  chmod +x "$tmp/release/system/dashboard/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'executable bit differs from release tree' "$tmp/err"
}

@test "rejects installer drift beyond the self-reference" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  printf '  platformSourceSecret: changed-after-push\n' \
    >> "$tmp/release/core/installer/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'installer values differ beyond their self-reference' "$tmp/err"
}

# Run the verifier from a copy whose sibling lib/ is ours. That is already how
# the workflows invoke it — `.release-tooling/hack/verify-promoted-packages.sh`,
# with its libraries resolved next to the script — so replacing one library is
# the faithful way to reach a failure inside it rather than a contrived one.
_tooling_with_collector() {
  mkdir -p "$1/lib"
  cp hack/verify-promoted-packages.sh "$1/"
  cp hack/lib/promoted-packages.sh "$1/lib/"
  cat > "$1/lib/image-refs.sh" <<EOF
collect_image_refs() {
$2
}
EOF
}

# The digest-set comparison is the only thing standing behind "no container
# bytes changed". Both tests below make it compare two empty sets, which
# succeeds, so without the fix the verifier reports that proof as passed having
# examined nothing.
@test "refuses when image-reference collection fails" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  _tooling_with_collector "$tmp/tooling" '  echo "collector exploded" >&2
  return 3'

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    "$tmp/tooling/verify-promoted-packages.sh" 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'collector exploded' "$tmp/err"
  # …and it did not also claim the proof it never completed. Counted rather
  # than negated with `!`, which suppresses errexit and would pass regardless.
  count="$(grep -c 'identical package tree' "$tmp/out" || true)"
  [ "${count:-0}" -eq 0 ]
}

@test "refuses when the collector reports no image references at all" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  _tooling_with_collector "$tmp/tooling" '  return 0'

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    "$tmp/tooling/verify-promoted-packages.sh" 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'collected no image references' "$tmp/err"
}

@test "rejects a changed container digest even when candidate and merge tree match" {
  tmp="$(_test_workspace)/fixture"
  _make_verify_fixture "$tmp"
  sed 's/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd/' \
    "$tmp/artifact/system/dashboard/values.yaml" > "$tmp/changed-values.yaml"
  mv "$tmp/changed-values.yaml" "$tmp/artifact/system/dashboard/values.yaml"
  cp "$tmp/artifact/system/dashboard/values.yaml" "$tmp/release/system/dashboard/values.yaml"

  rc=0
  MOCK_ARTIFACT="$tmp/artifact" MOCK_RC_ARTIFACT="$tmp/rc-artifact" MOCK_FLUX_LOG="$tmp/flux.log" \
    VERIFY_PACKAGES_WORKDIR="$tmp/work" \
    PATH="$tmp/bin:$PATH" \
    hack/verify-promoted-packages.sh 9.9.9 "$tmp/release" \
    > "$tmp/out" 2> "$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'promotion changed the container repository/digest set' "$tmp/err"
}
