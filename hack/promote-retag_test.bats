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
  ! grep -qE 'docker://[^ ]+:latest' "$tmp/out"
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
  ! grep -q '^copy ' "$tmp/skopeo.log"
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
  ! grep -q '^copy ' "$tmp/skopeo.log"
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
  ! grep -q '^copy ' "$tmp/skopeo.log"
  [ "$(grep -c '^inspect --raw ' "$tmp/skopeo.log")" -eq 1 ]
  # The empty-input hash must never appear: that is the guard being absent.
  ! grep -q 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' "$tmp/err"
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
  ! grep -q '^copy ' "$tmp/skopeo.log"
  rm -rf "$tmp"
}
