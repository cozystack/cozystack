#!/usr/bin/env bats
# Tests for hack/promote-publish-chart.sh — the stable cozy-installer publish.
#
# The behaviour under test is re-runnability. Finalize's registry side effects
# run after the stable tag and the GitHub release are already irreversible, so
# when they fail partway the fix is to re-run them — which means publishing an
# already-published chart must be a decision, not an accident. `helm package` is
# not byte-reproducible, so an unconditional re-push would silently move the
# stable chart tag onto new bytes; these tests pin the content comparison that
# prevents it, in both directions (identical -> skip, different -> refuse).
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. cozytest.sh's
# awk parser recognizes only @test blocks and a bare `}` on its own line; there
# is no `run`, `$status`, `$output`, `skip`, or setup()/teardown(). Each test
# runs as a shell function under `set -eu -x`, so a non-zero exit aborts the
# test (that is the exit-0 assertion) and other expectations are direct shell
# tests. A test that expects a non-zero exit must capture it with `|| rc=$?`
# so the harness's `set -e` does not abort first. Every closing brace inside a
# test body must stay indented — one at column 0 would end the block early.
#
# Run with: hack/cozytest.sh hack/promote-publish-chart_test.bats
#           (or `bats hack/promote-publish-chart_test.bats` if the bats binary
#           is installed; cozytest.sh is the CI path.)

# Build an isolated fixture: a copy of the installer chart to package, plus
# skopeo/helm shims on PATH. The copy matters — the script stamps
# packages/core/installer/values.yaml in place, which must never touch the real
# tree. Real `helm package` still runs (through the shim's passthrough) so the
# tarballs compared are the ones the release would actually ship.
#
# Sets: FIX (fixture root), and the FIX* variables the shims read.
_chart_fixture() {
  ROOT="$PWD"
  FIX="$(mktemp -d)"

  mkdir -p "$FIX/packages/core" "$FIX/bin"
  cp -r "$ROOT/packages/core/installer" "$FIX/packages/core/installer"

  export FIXREALHELM="$(command -v helm)"
  export FIXLOG_SKOPEO="$FIX/skopeo.log"
  export FIXLOG_HELM="$FIX/helm.log"
  export FIXPUSHED="$FIX/pushed.tgz"
  # Absent by default: the "nothing published yet" state.
  export FIXPUB="$FIX/published.tgz"
  : >"$FIXLOG_SKOPEO"
  : >"$FIXLOG_HELM"

  cat >"$FIX/bin/skopeo" <<'SHIM'
#!/bin/sh
echo "$*" >>"$FIXLOG_SKOPEO"
case "$1" in
  inspect)
    # Existence probe: succeeds only when the fixture says a chart is published.
    [ -f "$FIXPUB" ] || exit 1
    echo '{"schemaVersion":2}'
    ;;
esac
exit 0
SHIM

  cat >"$FIX/bin/helm" <<'SHIM'
#!/bin/sh
echo "$*" >>"$FIXLOG_HELM"
case "$1" in
  push)
    cp "$2" "$FIXPUSHED"
    exit 0
    ;;
  pull)
    dest=""
    prev=""
    for a in "$@"; do
      [ "$prev" = "--destination" ] && dest="$a"
      prev="$a"
    done
    cp "$FIXPUB" "$dest/cozy-installer-9.9.9.tgz"
    exit 0
    ;;
esac
# Everything else (notably `helm package`) is the real thing.
exec "$FIXREALHELM" "$@"
SHIM

  chmod +x "$FIX/bin/skopeo" "$FIX/bin/helm"
  PATH="$FIX/bin:$PATH"
  export PATH
}

@test "a prerelease tag is refused before anything is packaged" {
  _chart_fixture
  cd "$FIX"

  # An rc never reaches the registry side effects — finalize gates them on a
  # dash-free tag — so a chart published under an rc version would be an
  # artifact no release process produces.
  rc=0
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9-rc.1 \
    >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 1 ]

  # Refused early: no packaging, no registry traffic, and the chart's values are
  # left unstamped.
  [ ! -s "$FIXLOG_HELM" ]
  [ ! -s "$FIXLOG_SKOPEO" ]
  grep -q 'platformVersion: ""' "$FIX/packages/core/installer/values.yaml"
}

@test "a tag that is not vX.Y.Z is refused" {
  _chart_fixture
  cd "$FIX"

  rc=0
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" 9.9.9 >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  [ ! -s "$FIXLOG_HELM" ]
}

@test "an unpublished version is packaged with the stable stamps and pushed" {
  _chart_fixture
  cd "$FIX"

  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9

  grep -q '^push ' "$FIXLOG_HELM"
  [ -f "$FIXPUSHED" ]

  # The three stamps the release depends on: chart version and appVersion (the
  # `--version 9.9.9` install path resolves on them) and the platformVersion
  # default, which is how a retagged-not-rebuilt operator image reports the
  # stable version instead of the rc version baked in at build time.
  out="$FIX/unpacked"
  mkdir -p "$out"
  tar -xzf "$FIXPUSHED" -C "$out"
  grep -q '^version: 9.9.9$' "$out/cozy-installer/Chart.yaml"
  grep -q '^appVersion: 9.9.9$' "$out/cozy-installer/Chart.yaml"
  # yq keeps the original scalar's quoting style, so the stamped value lands
  # double-quoted exactly as it did when this ran inline in the workflow.
  grep -q 'platformVersion: "v9.9.9"' "$out/cozy-installer/values.yaml"
}

@test "an identical published chart is left alone on a re-run" {
  _chart_fixture
  cd "$FIX"

  # First run publishes; the shim keeps the pushed tarball.
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9
  cp "$FIXPUSHED" "$FIXPUB"
  rm -f "$FIXPUSHED"
  : >"$FIXLOG_HELM"

  # Second run is the recovery case: same tree, chart already published. It must
  # be a no-op rather than a re-push, because a fresh `helm package` of the same
  # sources produces different bytes (tar/gzip metadata) and would move the
  # stable chart tag off what users already pulled.
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9

  grep -q '^pull ' "$FIXLOG_HELM"
  ! grep -q '^push ' "$FIXLOG_HELM"
  [ ! -f "$FIXPUSHED" ]
}

@test "a differing published chart fails the run unless FORCE_CHART is set" {
  _chart_fixture
  cd "$FIX"

  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9

  # Forge a published chart whose CONTENT differs from this tree.
  forged="$FIX/forged"
  mkdir -p "$forged"
  tar -xzf "$FIXPUSHED" -C "$forged"
  echo "# published from a different tree" >>"$forged/cozy-installer/values.yaml"
  tar -czf "$FIXPUB" -C "$forged" cozy-installer
  rm -f "$FIXPUSHED"
  : >"$FIXLOG_HELM"

  # Content drift at an already-published version means the release tree and the
  # registry disagree about what vX.Y.Z is. Fail red rather than pick a winner.
  rc=0
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9 >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  [ ! -f "$FIXPUSHED" ]

  # Break-glass: an operator who has looked at the diff can overwrite.
  : >"$FIXLOG_HELM"
  env -u REGISTRY FORCE_CHART=1 "$ROOT/hack/promote-publish-chart.sh" v9.9.9
  grep -q '^push ' "$FIXLOG_HELM"
  [ -f "$FIXPUSHED" ]
}

@test "the latest chart tag moves only when MOVE_LATEST is set" {
  _chart_fixture
  cd "$FIX"

  # Default off: a patch on an older line publishes its version but must not
  # drag :latest backwards. Same contract as hack/promote-retag.sh.
  env -u REGISTRY "$ROOT/hack/promote-publish-chart.sh" v9.9.9
  ! grep -q 'cozy-installer:latest' "$FIXLOG_SKOPEO"

  cp "$FIXPUSHED" "$FIXPUB"
  : >"$FIXLOG_SKOPEO"
  env -u REGISTRY MOVE_LATEST=1 "$ROOT/hack/promote-publish-chart.sh" v9.9.9
  grep -q 'ghcr.io/cozystack/cozystack/cozy-installer:9.9.9' "$FIXLOG_SKOPEO"
  grep -q 'ghcr.io/cozystack/cozystack/cozy-installer:latest' "$FIXLOG_SKOPEO"
}
