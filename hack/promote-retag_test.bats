#!/usr/bin/env bats
# Tests for hack/promote-retag.sh — the rc->stable retag selector.
#
# Guards the regression where collect_refs scraped *every* @sha256 ref from the
# package values.yaml — including third-party images (docker.io/clastix/kubectl,
# ghcr.io/kvaps/...), bare upstream tags (kube-ovn/keycloak/kilo) and a
# "--migrate-image=..." arg string — so the first skopeo copy to a registry CI
# cannot push to aborted the whole promotion. The selector must emit only
# cozystack-owned ($REGISTRY/...) refs.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. cozytest.sh's
# awk parser recognizes only @test blocks and a bare `}` on its own line; there
# is no `run`, `$status`, `$output`, `skip`, or setup()/teardown(). Each test
# runs as a shell function under `set -eu -x`, so a non-zero exit aborts the
# test (that is the exit-0 assertion) and other expectations are direct shell
# tests. A test that expects a non-zero exit must capture it with `|| rc=$?`
# so the harness's `set -e` does not abort first. mikefarah yq is assumed
# present (provided by the test toolchain, like the other yq-using bats here).
#
# A negative expectation must be COUNTED — `[ "$(grep -c X f)" -eq 0 ]` — never
# written as `! grep -q X f`. POSIX and bash both exempt a `!`-negated pipeline
# from errexit, so the `!` form cannot fail a test: it is a comment that looks
# like an assertion. Six of them lived here, each proven inert by mutating the
# script under test (an unconditional copy before the write-once probe, and a
# MOVE_LATEST default of 1, both of which the `!` form waved through).
#
# Run with: hack/cozytest.sh hack/promote-retag_test.bats
#           (or `bats hack/promote-retag_test.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)

_make_registry_mocks() {
  t="$1"
  mkdir -p "$t/bin"
  cat >"$t/bin/yq" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo 'yq (https://github.com/mikefarah/yq/) version v4.45.1'
else
  printf '%s\n' "$MOCK_REF"
fi
EOF
  cat >"$t/bin/skopeo" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$MOCK_SKOPEO_LOG"
case "$1" in
  inspect)
    [ "$2" = "--raw" ]
    # An indeterminate failure: non-zero, no bytes, and the registry never says
    # the manifest is unknown. Must NOT read as "tag absent".
    if [ "${MOCK_TRANSIENT_ERROR:-0}" = "1" ]; then
      echo "Error: reading manifest from docker://${3:-?}: received unexpected HTTP status: 429 Too Many Requests" >&2
      exit 1
    fi
    if [ "$MOCK_MISSING_ONCE" = "1" ] && [ ! -f "$MOCK_STATE" ]; then
      # Absence as a registry actually reports it — the script requires proof,
      # not merely a non-zero exit.
      echo "Error: reading manifest from docker://${3:-?}: manifest unknown" >&2
      exit 1
    fi
    # A registry answering 200 with an empty body: exit 0, no bytes.
    if [ "${MOCK_EMPTY_MANIFEST:-0}" = "1" ]; then
      exit 0
    fi
    printf '%s' "$MOCK_MANIFEST"
    ;;
  copy)
    : >"$MOCK_STATE"
    ;;
  *)
    exit 2
    ;;
esac
EOF
  # sha256sum belongs in the stub for the same reason yq and skopeo do: the tests
  # below pin PATH to "$tmp/bin:/usr/bin:/bin", and where the hashing utility
  # lives is a property of the host, not of the thing under test. GNU coreutils
  # puts sha256sum in /usr/bin, which is inside that PATH; macOS ships shasum
  # there and sha256sum elsewhere, which is not -- so promote-retag.sh took its
  # "sha256sum is required" exit and six of the eleven tests failed on the
  # platform rather than on the code. They failed silently, too: each test
  # cleaned up from an EXIT trap, which replaces the one the bats binary installs
  # for its bookkeeping, so a failing test printed no TAP line at all and the run
  # showed five passes and no failures.
  #
  # Resolved from absolute candidates rather than through `command -v`: this stub
  # is first on PATH, so asking PATH for sha256sum finds this file and recurses.
  cat >"$t/bin/sha256sum" <<'EOF'
#!/bin/sh
set -eu
for _c in /usr/bin/sha256sum /bin/sha256sum /sbin/sha256sum /usr/local/bin/sha256sum; do
  [ -x "$_c" ] && exec "$_c" "$@"
done
for _c in /usr/bin/shasum /bin/shasum; do
  [ -x "$_c" ] && exec "$_c" -a 256 "$@"
done
echo "no sha256 implementation found outside the stub dir" >&2
exit 127
EOF
  chmod +x "$t/bin/yq" "$t/bin/skopeo" "$t/bin/sha256sum"
}

# _make_ref_tree <dir> <ref>... — build a throwaway package tree under <dir>
# whose only refs are the ones given, one images/*.tag file each, so a test can
# drive the destination-tag composition with ref shapes the committed tree does
# not contain. Run promote-retag.sh with <dir> as its CWD to pick it up:
# collect_image_refs is rooted at the relative path "packages", while the script
# sources hack/lib/image-refs.sh from its own $0.
#
# images/*.tag is the deliberate choice of storage shape. It is what
# ubuntu-container-disk actually uses, and it is the only shape whose collected
# ref still carries the source :tag — every yq shape in hack/lib/image-refs.sh
# rebuilds the ref as "<repo>@<digest>" and drops the tag — so it is the shape
# from which a prefix can be recovered at all.
_make_ref_tree() {
  t="$1"; shift
  n=0
  mkdir -p "$t/packages/apps/fake/images"
  for r in "$@"; do
    n=$((n + 1))
    printf '%s\n' "$r" >"$t/packages/apps/fake/images/ref-$n.tag"
  done
}

# _plan_destinations <plan-file> <out-file> — the destination ref of every copy
# in a --dry-run plan, one per line, sorted. The destination is the last field of
# a "DRY-RUN skopeo copy --multi-arch all docker://<src> docker://<dst>" line.
_plan_destinations() {
  awk '/^DRY-RUN skopeo copy /{print $NF}' "$1" | sed 's|^docker://||' | sort >"$2"
}

# _refute_duplicate_destinations <dst-file> — fail, loudly, if any destination
# ref appears twice. Two copies aimed at one tag mean the second trips the
# write-once guard mid-finalize, after the stable git tag and the GitHub release
# are already public. Written as an explicit non-empty test rather than
# `! uniq -d ... | grep -q .`: a `!`-negated pipeline is exempt from errexit, so
# that form cannot fail the test.
_refute_duplicate_destinations() {
  dup="$(uniq -d <"$1")"
  if [ -n "$dup" ]; then
    echo "duplicate destination refs in the promotion plan:" >&2
    printf '%s\n' "$dup" >&2
    return 1
  fi
}

@test "dry-run over the real tree retags only cozystack-owned refs" {
  tmp=$(mktemp -d)

  # `env -u REGISTRY`: the CI workflow exports REGISTRY=<OCIR build registry>
  # for every job (.github/workflows/pull-requests.yaml), but the committed
  # tree vendors its digests under the script's default ghcr.io/cozystack/
  # cozystack. Inheriting the ambient REGISTRY makes the selector filter for the
  # wrong registry, match nothing, and abort — so strip it and exercise the
  # default, the registry the refs below actually live under.
  #
  # An exit-0 is the assertion; on any non-zero, surface the script's own
  # stdout/stderr (collect_refs swallows yq errors, so its stderr is the only
  # breadcrumb) and the yq build, so a CI failure is self-diagnosing.
  rc=0
  env -u REGISTRY hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc; yq: $(yq --version 2>&1)" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    echo "--- script stdout ---" >&2; cat "$tmp/out" >&2
    return "$rc"
  fi

  # At least one cozystack-owned image is selected.
  grep -q 'docker://ghcr.io/cozystack/cozystack/' "$tmp/out"

  # Images whose digest is embedded in `tag` ({repository, tag: <t>@sha256:<d>})
  # must be selected too. The "at least one owned ref" check above cannot catch
  # their absence — it is satisfied by the shapes that already worked, which is
  # why eight images across six packages were silently skipped while this suite
  # stayed green. Naming concrete packages is deliberate: a count or a generic
  # pattern would drift back to proving nothing. linstor-csi and piraeus-server
  # are the two whose absence from GHCR broke the nightly e2e at image pre-pull.
  for owned in linstor-csi piraeus-server kamaji redis-operator; do
    grep -q "docker://ghcr.io/cozystack/cozystack/${owned}@sha256:" "$tmp/out"
  done

  # Images whose host is NOT inside `repository` must be selected too. Both are
  # built and pushed to $REGISTRY by cozystack, both carry the digest in `tag`,
  # and both were dropped by the ownership filter for looking host-less — so
  # neither has ever received a 1.x release tag (on GHCR keycloak-operator has
  # only `latest`, and kubeovn's newest cozystack-versioned tag predates 1.0).
  # keycloak-operator splits the host into a sibling `registry` key; kubeovn
  # keeps it in the document-level global.registry.address, written by the
  # cozystack/kubeovn-chart wrapper's own `make image`.
  for owned in keycloak-operator kubeovn; do
    grep -q "docker://ghcr.io/cozystack/cozystack/${owned}@sha256:" "$tmp/out"
  done

  # Every docker:// ref in the copy plan is under the cozystack registry — no
  # third-party repos and no malformed arg-string refs leak through.
  bad=$(grep -oE 'docker://[^ ]+' "$tmp/out" | sed 's|docker://||' \
        | grep -vE '^ghcr\.io/cozystack/cozystack/' || true)
  [ -z "$bad" ]
  rm -rf "$tmp"
}

@test "default leaves :latest unmoved" {
  tmp=$(mktemp -d)

  # :latest belongs to promotion, and only when the promoted version is the
  # newest published stable. Without MOVE_LATEST the plan retags the stable tag
  # but must NOT repoint :latest — otherwise a patch on an older line would drag
  # :latest backwards.
  rc=0
  env -u REGISTRY hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  # The stable tag is in the copy plan...
  grep -qE 'docker://ghcr\.io/cozystack/cozystack/[^ ]*:v9\.9\.9' "$tmp/out"
  # ...but nothing moves :latest.
  [ "$(grep -cE 'docker://[^ ]+:latest' "$tmp/out")" -eq 0 ]
  rm -rf "$tmp"
}

@test "MOVE_LATEST=1 also repoints :latest" {
  tmp=$(mktemp -d)

  rc=0
  env -u REGISTRY MOVE_LATEST=1 hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  # Every promoted repo also gets a :latest copy in the plan.
  grep -qE 'docker://ghcr\.io/cozystack/cozystack/[^ ]*:latest' "$tmp/out"
  rm -rf "$tmp"
}

@test "REGISTRY override scopes the selection" {
  tmp=$(mktemp -d)

  # No cozystack images live under example.com/nope, so the selector finds
  # nothing and exits non-zero rather than silently promoting the wrong set.
  # Capture the exit status without tripping the harness's `set -e`.
  rc=0
  REGISTRY="example.com/nope" hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  # The diagnostic is written to stderr.
  grep -q 'No cozystack-owned digest-pinned image refs found' "$tmp/err"
  rm -rf "$tmp"
}

@test "retags images whose ref lives outside a values.yaml" {
  # Until the file enumeration moved to hack/lib/image-refs.sh this scanned the
  # depth-2 values.yaml alone, so every ref held in an images/*.tag file or
  # stamped into a template was skipped — the promotion reported success while
  # never creating those images' :<version> tags. Twelve images were affected
  # (30 refs selected before, 42 after). Because the retag stays inside one
  # registry the digests still resolved, so nothing failed at pull time and the
  # gap went unnoticed until a release shipped reading as a release candidate.
  tmp=$(mktemp -d)

  rc=0
  env -u REGISTRY hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  # grafana-dashboards lives in
  # packages/system/grafana-operator/images/grafana-dashboards.tag, and
  # multus-cni is sed'd into packages/system/multus/templates/*.yml. Neither is
  # reachable from any values.yaml.
  grep -q 'cozystack/grafana-dashboards:v9.9.9' "$tmp/out"
  grep -q 'cozystack/multus-cni:v9.9.9' "$tmp/out"
  rm -rf "$tmp"
}

@test "raw manifest digest resolves for an OCI artifact" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.cncf.flux.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[]}'
  digest="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${digest}"

  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$manifest" \
    MOCK_MISSING_ONCE=0 MOCK_STATE="$tmp/state" MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  grep -q "already at ${digest}; skipping stable copy" "$tmp/out"
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 2 ]
  [ "$(grep -c '^copy ' "$tmp/skopeo.log")" -eq 0 ]
  rm -rf "$tmp"
}

@test "post-copy raw manifest digest mismatch fails verification" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.cncf.flux.config.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"layers":[]}'
  expected_manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.cncf.flux.config.v1+json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"layers":[]}'
  actual="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)"
  expected="sha256:$(printf '%s' "$expected_manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${expected}"

  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$manifest" \
    MOCK_MISSING_ONCE=1 MOCK_STATE="$tmp/state" MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "resolved to '${actual}', expected '${expected}'" "$tmp/err"
  grep -q '^copy --multi-arch all ' "$tmp/skopeo.log"
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 2 ]
  rm -rf "$tmp"
}

@test "missing stable tag remains an empty digest and proceeds to copy" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.cncf.flux.config.v1+json","digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"layers":[]}'
  digest="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${digest}"

  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$manifest" \
    MOCK_MISSING_ONCE=1 MOCK_STATE="$tmp/state" MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  grep -q '^copy --multi-arch all ' "$tmp/skopeo.log"
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 2 ]
  grep -q 'Retagged image refs to v1.6.0' "$tmp/out"
  rm -rf "$tmp"
}

@test "existing stable tag at a different digest is refused, not moved" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  # The stable tag already exists (MOCK_MISSING_ONCE=0) but resolves to a
  # manifest other than the rc's: released bytes must never be overwritten.
  published='{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},"layers":[]}'
  rc_manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"},"layers":[]}'
  published_digest="sha256:$(printf '%s' "$published" | sha256sum | cut -d' ' -f1)"
  rc_digest="sha256:$(printf '%s' "$rc_manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${rc_digest}"

  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$published" \
    MOCK_MISSING_ONCE=0 MOCK_STATE="$tmp/state" MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "already exists at '${published_digest}'; refusing to move it to '${rc_digest}'" "$tmp/err"
  # The refusal must happen BEFORE any write.
  [ "$(grep -c '^copy ' "$tmp/skopeo.log")" -eq 0 ]
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 1 ]
  rm -rf "$tmp"
}

@test "a zero-exit empty manifest refuses to decide and never writes" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333"},"layers":[]}'
  digest="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${digest}"

  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$manifest" \
    MOCK_EMPTY_MANIFEST=1 MOCK_MISSING_ONCE=0 MOCK_STATE="$tmp/state" \
    MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  # A 200 with an empty body proves nothing: the tag may hold released bytes this
  # promotion must not overwrite. So the script must abort BEFORE any copy —
  # reading "no bytes" as "not published" is what would turn a proxy hiccup into
  # an overwrite of a published stable tag.
  [ "$rc" -ne 0 ]
  grep -q 'returned no manifest bytes' "$tmp/err"
  [ "$(grep -c '^copy ' "$tmp/skopeo.log")" -eq 0 ]
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 1 ]
  # The empty-input hash must never appear: that is the guard being absent.
  [ "$(grep -c 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' "$tmp/err")" -eq 0 ]
  rm -rf "$tmp"
}

@test "a transient registry failure is not read as an unpublished tag" {
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"

  manifest='{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:4444444444444444444444444444444444444444444444444444444444444444"},"layers":[]}'
  digest="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)"
  ref="example.com/cozystack/cozystack-packages:v1.6.0-rc.1@${digest}"

  # 429 during finalize: non-zero exit, no bytes, no "manifest unknown". The old
  # shape of this helper made that indistinguishable from an absent tag and
  # proceeded to copy over it; finalize retags ~42 refs with no retry wrapper, so
  # a single rate-limit was enough to reach that path.
  rc=0
  REGISTRY="example.com/cozystack" MOCK_REF="$ref" MOCK_MANIFEST="$manifest" \
    MOCK_TRANSIENT_ERROR=1 MOCK_MISSING_ONCE=0 MOCK_STATE="$tmp/state" \
    MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
    PATH="$tmp/bin:/usr/bin:/bin" hack/promote-retag.sh v1.6.0 \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'cannot be treated as unpublished' "$tmp/err"
  grep -q '429' "$tmp/err"
  [ "$(grep -c '^copy ' "$tmp/skopeo.log")" -eq 0 ]
  rm -rf "$tmp"
}

@test "the destination tag keeps a source tag's prefix and nothing else" {
  root=$(pwd -P)
  tmp=$(mktemp -d)
  reg=ghcr.io/cozystack/cozystack
  d1=1111111111111111111111111111111111111111111111111111111111111111
  d2=2222222222222222222222222222222222222222222222222222222222222222
  d3=3333333333333333333333333333333333333333333333333333333333333333
  d4=4444444444444444444444444444444444444444444444444444444444444444
  d5=5555555555555555555555555555555555555555555555555555555555555555

  # One ref per source-tag shape a promoted tree carries, or plausibly will. The
  # version component is the part promotion replaces; whatever precedes it says
  # WHICH image in the repository the ref is, and has to survive the retag.
  _make_ref_tree "$tmp" \
    "$reg/plain:v1.5.2@sha256:$d1" \
    "$reg/prefixed:v1.30-v1.5.2@sha256:$d2" \
    "$reg/floating:latest@sha256:$d3" \
    "$reg/prerelease:v1.6.0-rc.1@sha256:$d4"

  # ...plus a ref with no tag at all, the form every yq shape in
  # hack/lib/image-refs.sh emits. Nothing to preserve, and nothing to invent.
  mkdir -p "$tmp/packages/system/fake-values"
  cat >"$tmp/packages/system/fake-values/values.yaml" <<EOF
image:
  repository: $reg/tagless
  digest: sha256:$d5
EOF

  rc=0
  ( cd "$tmp" && env -u REGISTRY "$root/hack/promote-retag.sh" v1.5.4 --dry-run ) \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    echo "--- script stdout ---" >&2; cat "$tmp/out" >&2
    return "$rc"
  fi

  _plan_destinations "$tmp/out" "$tmp/dst"

  # v1.30- is a prefix: the tail of the tag parses as a version, so the head
  # cannot be one.
  [ "$(grep -cFx "$reg/prefixed:v1.30-v1.5.4" "$tmp/dst")" -eq 1 ]
  # A version-only tag has no prefix. This is the regression guard that matters
  # most — all but six of the branch's refs are this shape.
  [ "$(grep -cFx "$reg/plain:v1.5.4" "$tmp/dst")" -eq 1 ]
  # "latest" is not a version, so it is not a prefix either: a tag that does not
  # END in a version contributes nothing and is replaced whole. Reading it as a
  # prefix would invent floating:latest-v1.5.4, a tag nothing references.
  [ "$(grep -cFx "$reg/floating:v1.5.4" "$tmp/dst")" -eq 1 ]
  # v1.6.0-rc.1 is ONE version, not "v1.6.0-" + "rc.1". Splitting on the last
  # hyphen instead would emit prerelease:v1.6.0-v1.5.4 and mis-tag every ref in a
  # real promotion, because an rc version string is exactly what the promoted
  # tree's refs carry.
  [ "$(grep -cFx "$reg/prerelease:v1.5.4" "$tmp/dst")" -eq 1 ]
  # A ref that reached the script tagless gets the bare stable tag.
  [ "$(grep -cFx "$reg/tagless:v1.5.4" "$tmp/dst")" -eq 1 ]

  [ "$(awk 'END{print NR}' "$tmp/dst")" -eq 5 ]
  _refute_duplicate_destinations "$tmp/dst"
  rm -rf "$tmp"
}

@test "several prefixed refs in one repository get distinct destinations" {
  root=$(pwd -P)
  tmp=$(mktemp -d)
  reg=ghcr.io/cozystack/cozystack

  # The 1.5-line ubuntu-container-disk refs, verbatim: one image per Kubernetes
  # minor, six distinct digests sharing one repository and told apart only by the
  # tag prefix. Composing the destination tag from the release version alone aims
  # all six at ubuntu-container-disk:v1.5.4 — the sort-first one is written and
  # the next trips the write-once guard, inside finalize, after the stable git tag
  # and the GitHub release are already public.
  _make_ref_tree "$tmp" \
    "$reg/ubuntu-container-disk:v1.30-v1.5.2@sha256:dc2e4794e75b861bf087e93037a2522fec398ffd8ac2b69591c4b0a50260f431" \
    "$reg/ubuntu-container-disk:v1.31-v1.5.2@sha256:4e3479b4f469581d87574c72246995d1df6262be42e60022084aca658ead8755" \
    "$reg/ubuntu-container-disk:v1.32-v1.5.2@sha256:79df2a0da2b1c13eee44f32793e4ca726bf4987935cecc34d204898d58cbbce1" \
    "$reg/ubuntu-container-disk:v1.33-v1.5.2@sha256:200d41bfb8108d43e1e938df47175dd4dbaae23d48e6d1c20ae6c6432213eeee" \
    "$reg/ubuntu-container-disk:v1.34-v1.5.2@sha256:21c0112abe14caba7a001b542d587ec1dab477e5f0fe65dba4383cac331c206c" \
    "$reg/ubuntu-container-disk:v1.35-v1.5.2@sha256:f90a024f2e2b4ef1b3ea653f5f47699218114e41bbd9b7c46e7c0ed00558aebb"

  rc=0
  ( cd "$tmp" && env -u REGISTRY "$root/hack/promote-retag.sh" v1.5.4 --dry-run ) \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  _plan_destinations "$tmp/out" "$tmp/dst"
  _refute_duplicate_destinations "$tmp/dst"
  [ "$(awk 'END{print NR}' "$tmp/dst")" -eq 6 ]
  for minor in v1.30 v1.31 v1.32 v1.33 v1.34 v1.35; do
    [ "$(grep -cFx "$reg/ubuntu-container-disk:${minor}-v1.5.4" "$tmp/dst")" -eq 1 ]
  done
  rm -rf "$tmp"
}

@test "MOVE_LATEST keeps the prefix so each prefix gets its own floating tag" {
  root=$(pwd -P)
  tmp=$(mktemp -d)
  reg=ghcr.io/cozystack/cozystack
  d1=1111111111111111111111111111111111111111111111111111111111111111
  d2=2222222222222222222222222222222222222222222222222222222222222222

  # :latest is prefixed for the same reason the stable tag is. Without it, both
  # refs below copy to disk:latest and the tag ends up pointing at whichever
  # digest the sort happened to put last — a floating tag that names one
  # Kubernetes minor's disk and reads as if it named all of them.
  _make_ref_tree "$tmp" \
    "$reg/disk:v1.30-v1.5.2@sha256:$d1" \
    "$reg/disk:v1.31-v1.5.2@sha256:$d2"

  rc=0
  ( cd "$tmp" && env -u REGISTRY MOVE_LATEST=1 \
      "$root/hack/promote-retag.sh" v1.5.4 --dry-run ) \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  _plan_destinations "$tmp/out" "$tmp/dst"
  _refute_duplicate_destinations "$tmp/dst"
  [ "$(awk 'END{print NR}' "$tmp/dst")" -eq 4 ]
  [ "$(grep -cFx "$reg/disk:v1.30-latest" "$tmp/dst")" -eq 1 ]
  [ "$(grep -cFx "$reg/disk:v1.31-latest" "$tmp/dst")" -eq 1 ]
  # The unprefixed floating tag is left alone: it belongs to no minor.
  [ "$(grep -cFx "$reg/disk:latest" "$tmp/dst")" -eq 0 ]
  rm -rf "$tmp"
}

@test "two digests behind one prefixed destination still trip the write-once guard" {
  root=$(pwd -P)
  tmp=$(mktemp -d)
  _make_registry_mocks "$tmp"
  reg=example.com/cozystack

  # Preserving the prefix must not become a way around the write-once guard. Two
  # digests that still land on the SAME destination tag have to be refused, not
  # silently overwritten — and same-repo/same-prefix is that case routed through
  # the new composition rather than the old path.
  published='{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:5555555555555555555555555555555555555555555555555555555555555555"},"layers":[]}'
  first="sha256:$(printf '%s' "$published" | sha256sum | cut -d' ' -f1)"
  # All-f is the largest hex digest, so the ref carrying $first is the one the
  # sorted plan copies first and the one the post-copy verify then matches. That
  # keeps the failure the write-once refusal rather than a verify mismatch.
  second=sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff

  _make_ref_tree "$tmp" \
    "$reg/disk:v1.30-v1.5.2@$first" \
    "$reg/disk:v1.30-v1.5.2@$second"

  rc=0
  ( cd "$tmp" && env REGISTRY="$reg" MOCK_REF="" MOCK_MANIFEST="$published" \
      MOCK_MISSING_ONCE=1 MOCK_STATE="$tmp/state" MOCK_SKOPEO_LOG="$tmp/skopeo.log" \
      PATH="$tmp/bin:/usr/bin:/bin" "$root/hack/promote-retag.sh" v1.5.4 ) \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  # The message names the PREFIXED destination, which is also what proves the
  # composition reached the guard.
  grep -q "disk:v1.30-v1.5.4 already exists at '${first}'; refusing to move it to '${second}'" "$tmp/err"
  # Exactly one copy: the first ref's, before the refusal. Counted rather than
  # negated, so the assertion can actually fail.
  [ "$(grep -c '^copy --multi-arch all ' "$tmp/skopeo.log")" -eq 1 ]
  rm -rf "$tmp"
}

@test "the real tree's promotion plan has no duplicate destination" {
  tmp=$(mktemp -d)

  # The check that would have caught the ubuntu-container-disk collision before a
  # release reached finalize. A duplicate destination in the plan is a promotion
  # that fails halfway through the irreversible step, so it is worth pinning
  # against the committed tree permanently rather than against fixtures alone.
  rc=0
  env -u REGISTRY hack/promote-retag.sh v1.5.4 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "promote-retag.sh exited $rc" >&2
    echo "--- script stderr ---" >&2; cat "$tmp/err" >&2
    return "$rc"
  fi

  _plan_destinations "$tmp/out" "$tmp/dst"
  _refute_duplicate_destinations "$tmp/dst"

  # And the per-Kubernetes-minor disks each get their own destination: one per
  # images/ubuntu-container-disk-*.tag file in the tree, derived from the tree so
  # adding or dropping a minor does not need this number edited. Branches that
  # ship no such image (main, release-1.6) assert 0 == 0 and stay green.
  minors=$(find packages/apps/kubernetes/images -name 'ubuntu-container-disk-*.tag' \
             2>/dev/null | awk 'END{print NR}')
  [ "$(grep -c '^ghcr\.io/cozystack/cozystack/ubuntu-container-disk:' "$tmp/dst")" \
    -eq "$minors" ]
  rm -rf "$tmp"
}
