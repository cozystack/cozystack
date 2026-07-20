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

@test "dry-run over the real tree retags only cozystack-owned refs" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

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

  # Every docker:// ref in the copy plan is under the cozystack registry — no
  # third-party repos and no malformed arg-string refs leak through.
  bad=$(grep -oE 'docker://[^ ]+' "$tmp/out" | sed 's|docker://||' \
        | grep -vE '^ghcr\.io/cozystack/cozystack/' || true)
  [ -z "$bad" ]
}

@test "default leaves :latest unmoved" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

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
}

@test "MOVE_LATEST=1 also repoints :latest" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

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
}

@test "REGISTRY override scopes the selection" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  # No cozystack images live under example.com/nope, so the selector finds
  # nothing and exits non-zero rather than silently promoting the wrong set.
  # Capture the exit status without tripping the harness's `set -e`.
  rc=0
  REGISTRY="example.com/nope" hack/promote-retag.sh v9.9.9 --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  # The diagnostic is written to stderr.
  grep -q 'No cozystack-owned digest-pinned image refs found' "$tmp/err"
}
