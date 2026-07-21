#!/usr/bin/env bats
# Tests for hack/promote-rewrite-tags.sh — the rc->stable tag string rewrite.
#
# Guards the regression that shipped a half-promoted release: the rewrite lived
# inline in .github/workflows/promote-rc.yaml and globbed the depth-2
# values.yaml plus packages/apps/kubernetes/images/*.tag alone, on the premise
# that the kubernetes app was the only one whose .tag files carry the cozystack
# version. Nine other .tag files and one stamped template do, so v1.6.0 was
# staged with 33 refs still reading v1.6.0-rc.4. Being workflow-inline, there
# was nothing to unit test and the miss was only observable by cutting a
# release.
#
# The central test therefore round-trips against the REAL tree and — crucially —
# discovers ref-bearing files BY CONTENT rather than by asking the enumeration
# under test where to look. A fixture built from image_ref_files() could only
# ever confirm that the enumeration agrees with itself; grepping for the
# registry prefix finds files it has never heard of, which is the whole failure
# mode. Stamp wider than the rewrite, then assert the rewrite cleaned all of it.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. cozytest.sh's
# awk parser recognizes only @test blocks and a bare `}` on its own line; there
# is no `run`, `$status`, `$output`, `skip`, or setup()/teardown(). Each test
# runs as a shell function under `set -eu -x`, so a non-zero exit aborts the
# test (that is the exit-0 assertion). A test that expects a non-zero exit must
# capture it with `|| rc=$?` so the harness's `set -e` does not abort first.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset here and would abort
# the whole suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/promote-rewrite-tags_test.bats

@test "rewrite leaves no rc reference anywhere in the tree" {
  tmp=$(mktemp -d)
  RC=9.9.9-rc.9

  # Build the fixture from files discovered by CONTENT, independent of
  # hack/lib/image-refs.sh. Excluding charts/ mirrors the script's own scope:
  # vendored upstream chart values are not stamped by the build.
  # --exclude='*.md' keeps documentation examples (which carry placeholder or
  # historical versions) out of the fixture, matching the postcondition.
  for f in $(grep -rIl --exclude-dir=charts --exclude='*.md' 'ghcr\.io/cozystack/cozystack/' packages/); do
    mkdir -p "$tmp/$(dirname "$f")"
    cp "$f" "$tmp/$f"
  done

  # Stamp a synthetic rc version onto every VERSION-LINE ref, modelling what an
  # rc build actually produces. The tag must be exactly vX.Y.Z immediately
  # followed by @, which is the shape the build stamps; that anchor is what
  # keeps the fixture faithful rather than merely aggressive:
  #   - kamaji's v0.19.0-cozystack.0@ does not match (a '-' follows the patch),
  #     and must not — it is component-versioned and no promotion rewrites it
  #   - a ':<chart-version>' placeholder in a Dockerfile comment does not match
  #     (no digest follows), and must not — nothing ever stamps a version there
  # Stamping either would make the test demand a rewrite that would itself be a
  # bug, and both were flagged by the postcondition when this sed was looser.
  for f in $(grep -rIl 'ghcr\.io/cozystack/cozystack/' "$tmp/packages"); do
    sed -i -E "s|(ghcr\.io/cozystack/cozystack/[A-Za-z0-9._-]+):v[0-9]+\.[0-9]+\.[0-9]+@|\1:v${RC}@|g" "$f"
  done

  # Sanity: the fixture must actually contain the rc string, otherwise the
  # assertion below passes vacuously and the test guards nothing.
  before=$(grep -rIl -- "$RC" "$tmp/packages" | wc -l | tr -d ' ')
  [ "$before" -gt 0 ]

  hack/promote-rewrite-tags.sh "$RC" 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || {
    echo "--- promote-rewrite-tags.sh output ---" >&2; cat "$tmp/log" >&2; rm -rf "$tmp"; return 1
  }

  after=$(grep -rIl -- "$RC" "$tmp/packages" | wc -l | tr -d ' ')
  if [ "$after" -ne 0 ]; then
    echo "files still carrying $RC after the rewrite:" >&2
    grep -rIl -- "$RC" "$tmp/packages" >&2
    rm -rf "$tmp"
    return 1
  fi
  echo "stamped $before files, all rewritten"
  rm -rf "$tmp"
}

@test "rewrite covers .tag files outside packages/apps/kubernetes" {
  # The specific blind spot that shipped. Pinned as its own test so a future
  # narrowing of the glob fails with a message naming the cause, rather than
  # only tripping the broad round-trip above.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/monitoring/images"
  echo 'ghcr.io/cozystack/cozystack/grafana:v9.9.9-rc.9@sha256:'"$(printf 'a%.0s' $(seq 64))" \
    > "$tmp/packages/system/monitoring/images/grafana.tag"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  got=$(cat "$tmp/packages/system/monitoring/images/grafana.tag")
  case "$got" in
    *:v9.9.9@sha256:*) ;;
    *) echo "expected v9.9.9, got: $got" >&2; rm -rf "$tmp"; return 1 ;;
  esac
  rm -rf "$tmp"
}

@test "rewrite covers a ref stamped into a declared template" {
  # Storage shape 3 (IMAGE_REF_EXTRA_FILES). multus sed's its ref into a
  # vendored upstream manifest, so it is reachable by neither glob.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/multus/templates"
  printf '          image: ghcr.io/cozystack/cozystack/multus-cni:v9.9.9-rc.9@sha256:%s\n' \
    "$(printf 'b%.0s' $(seq 64))" > "$tmp/packages/system/multus/templates/multus-daemonset-thick.yml"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  grep -q ':v9.9.9@sha256:' "$tmp/packages/system/multus/templates/multus-daemonset-thick.yml" || {
    echo "multus template not rewritten" >&2; rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "component-versioned and third-party tags are left alone" {
  # kamaji is versioned by its upstream component (v0.19.0-cozystack.N) and
  # busybox is third-party; neither rides the cozystack version line, so a
  # promotion must not touch either. Rewriting them would be a bug introduced
  # by an over-eager fix to the one this file guards.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/capi/images" "$tmp/packages/apps/kubernetes/images"
  kamaji='ghcr.io/cozystack/cozystack/cluster-api-control-plane-provider-kamaji:v0.19.0-cozystack.0@sha256:c'
  busybox='docker.io/library/busybox:1.37.0@sha256:d'
  echo "$kamaji" > "$tmp/packages/system/capi/images/kamaji.tag"
  echo "$busybox" > "$tmp/packages/apps/kubernetes/images/busybox.tag"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  [ "$(cat "$tmp/packages/system/capi/images/kamaji.tag")" = "$kamaji" ]
  [ "$(cat "$tmp/packages/apps/kubernetes/images/busybox.tag")" = "$busybox" ]
  rm -rf "$tmp"
}

@test "digests are never altered by the rewrite" {
  # Promotion retags by digest and must not rebuild; if the rewrite could touch
  # a digest, the stable image would stop being bit-for-bit the tested rc.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/x/images"
  d=$(printf 'e%.0s' $(seq 64))
  echo "ghcr.io/cozystack/cozystack/x:v9.9.9-rc.9@sha256:$d" > "$tmp/packages/system/x/images/x.tag"

  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >/dev/null 2>&1 || {
    rm -rf "$tmp"; return 1
  }

  grep -q "@sha256:$d\$" "$tmp/packages/system/x/images/x.tag" || {
    echo "digest changed: $(cat "$tmp/packages/system/x/images/x.tag")" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "postcondition fails when a ref hides in an unenumerated location" {
  # The guard that makes a future blind spot loud instead of silent. A ref in a
  # location neither glob nor IMAGE_REF_EXTRA_FILES covers must abort the
  # promotion, not sail through as it did for v1.6.0.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/surprise/deeply/nested"
  echo 'image: ghcr.io/cozystack/cozystack/surprise:v9.9.9-rc.9@sha256:f' \
    > "$tmp/packages/system/surprise/deeply/nested/manifest.yaml"

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 9.9.9 "$tmp/packages" >"$tmp/log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "expected a non-zero exit for an unenumerated ref, got 0" >&2
    rm -rf "$tmp"; return 1
  fi
  grep -q 'still carry the rc version' "$tmp/log" || {
    echo "expected the postcondition's message; got:" >&2; cat "$tmp/log" >&2
    rm -rf "$tmp"; return 1
  }
  rm -rf "$tmp"
}

@test "malformed versions are rejected before any file is touched" {
  # RC_VERSION becomes a sed pattern and STABLE_VERSION its replacement, so an
  # unvalidated argument is an injection surface as well as a correctness one.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/packages/system/x/images"
  echo 'ghcr.io/cozystack/cozystack/x:v1.0.0@sha256:a' > "$tmp/packages/system/x/images/x.tag"
  orig=$(cat "$tmp/packages/system/x/images/x.tag")

  rc=0
  hack/promote-rewrite-tags.sh 'not-a-version' 9.9.9 "$tmp/packages" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]

  rc=0
  hack/promote-rewrite-tags.sh 9.9.9-rc.9 'v9.9.9; rm -rf /' "$tmp/packages" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]

  # Nothing was modified by either rejected invocation.
  [ "$(cat "$tmp/packages/system/x/images/x.tag")" = "$orig" ]
  rm -rf "$tmp"
}

@test "image_ref_files enumerates all three storage shapes" {
  # Direct cover for the shared enumeration, so a regression there is
  # attributed to the library rather than surfacing only through its consumers.
  . hack/lib/image-refs.sh

  files=$(image_ref_files packages)
  echo "$files" | grep -q '^packages/system/objectstorage-controller/values.yaml$'
  echo "$files" | grep -q '^packages/system/monitoring/images/grafana.tag$'
  echo "$files" | grep -q '^packages/system/multus/templates/multus-daemonset-thick.yml$'

  # And it must not reach into vendored charts, whose values `make update`
  # regenerates and whose images the build neither pushes nor stamps.
  if echo "$files" | grep -q '/charts/'; then
    echo "enumeration leaked into a vendored charts/ subtree:" >&2
    echo "$files" | grep '/charts/' >&2
    return 1
  fi
}

@test "collect_image_refs finds refs the depth-2 values.yaml glob misses" {
  # The mirror and the retag both filter on ownership, so a ref the collector
  # never emits is silently skipped rather than failing. Assert the two shapes
  # that were missing are present in the real tree's collection.
  . hack/lib/image-refs.sh

  refs=$(collect_image_refs packages)
  echo "$refs" | grep -q 'cozystack/grafana@sha256:\|cozystack/grafana:[^ ]*@sha256:'
  echo "$refs" | grep -q 'multus-cni'
}
