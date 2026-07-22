#!/usr/bin/env bats
# Behavioural tests for the multus install-cni-plugins init container script.
#
# The script is 15 lines of privileged root shell that run on every node and
# replace CNI plugin binaries underneath a running kubelet, so the rendered
# helm-unittest assertions in packages/system/multus/tests/multus_test.yaml are
# not enough on their own: they match the script's source text, so dropping
# `chmod 0755` or `set -eu` leaves them green while the plugins land
# unexecutable or a failed copy is reported as success. These tests extract the
# script from the rendered DaemonSet and actually run it.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); the
# relative paths below resolve against that cwd. There are no setup/teardown
# directives in this runner, so each @test builds its own fixture.

CHART=packages/system/multus

# Extract the init container's script from the rendered chart and rebind its two
# absolute paths ($1 = plugin source, $2 = host cni bin) so it can run unprivileged.
render_script() {
  helm template "$CHART" \
    | yq eval 'select(.kind == "DaemonSet") | .spec.template.spec.initContainers[] | select(.name == "install-cni-plugins") | .command[2]' - \
    | sed -e "s|/host/opt/cni/bin|$1|g" -e "s|/cni-plugins|$2|g"
}

# A fake image payload: plugin binaries that report their own name when run.
make_plugins() {
  mkdir -p "$1"
  for p in bridge macvlan portmap; do
    printf '#!/bin/sh\necho %s-NEW\n' "$p" > "$1/$p"
    chmod 0755 "$1/$p"
  done
}

@test "installs every staged plugin into the host cni bin dir, executable" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"
  sh "$tmp/s.sh"

  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "3" ]
  [ -x "$tmp/dst/bridge" ]
  [ "$("$tmp/dst/bridge")" = "bridge-NEW" ]
}

@test "installs a plugin executable even when the staged copy is not" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # cp reproduces the source mode, so the chmod is only load-bearing when a
  # staged plugin is not already 0755. Without it the plugin lands unexecutable
  # and the NAD fails at runtime with permission denied, long after this ran.
  chmod 0644 "$tmp/src/bridge"
  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"
  sh "$tmp/s.sh"

  [ -x "$tmp/dst/bridge" ]
}

@test "replaces a plugin by rename, not by writing onto the live path" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # Stand in for a plugin another CNI already installed and the kubelet may exec.
  printf '#!/bin/sh\necho portmap-OLD\n' > "$tmp/dst/portmap"
  chmod 0755 "$tmp/dst/portmap"
  before=$(ls -i "$tmp/dst/portmap" | awk '{print $1}')

  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"
  sh "$tmp/s.sh"

  after=$(ls -i "$tmp/dst/portmap" | awk '{print $1}')
  [ "$("$tmp/dst/portmap")" = "portmap-NEW" ]
  # The atomicity signature: a rename swaps in a new inode, so any exec holding
  # the old one keeps a complete binary. A cp onto the live path would truncate
  # and rewrite in place, keeping the inode and racing that exec.
  if [ "$before" = "$after" ]; then echo "FAIL: portmap replaced in place (inode $before unchanged) — copy was not atomic"; false; fi
}

@test "leaves no temp files behind, and clears ones stranded by an earlier kill" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # Named for a plugin this image does not stage, so no iteration reuses and
  # renames it away — only the cleanup can remove it. A .tmp-bridge here would
  # be consumed by the bridge iteration and pass even with no cleanup at all.
  echo stranded > "$tmp/dst/.tmp-plugin-from-an-older-image"

  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"
  sh "$tmp/s.sh"

  [ "$(ls -a1 "$tmp/dst" | grep -c '^\.tmp-' || true)" = "0" ]
}

@test "skips staging instead of failing when the image predates the plugins" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/dst"
  # No source dir at all: the release-prep digest re-pin lags a Dockerfile
  # change, so the pinned image can have no /cni-plugins. Failing here would
  # crashloop the whole daemonset and take multus down on every node.
  render_script "$tmp/dst" "$tmp/absent" > "$tmp/s.sh"
  sh "$tmp/s.sh"

  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "0" ]
}

@test "does not fail when the staged plugin directory is empty" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/src" "$tmp/dst"
  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"
  # An unmatched glob stays literal in sh; without a guard cp fails and set -e
  # takes the daemonset down.
  sh "$tmp/s.sh"

  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "0" ]
}

@test "fails loudly when a plugin cannot be installed" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # An entry cp cannot copy, sorted ahead of the good plugins so that a later
  # iteration still succeeds. That ordering is the whole point: a for loop
  # reports the status of its last command, so a failure in the *final*
  # iteration exits non-zero with or without `set -e` and cannot tell the two
  # apart. Failing first and succeeding after, a dropped `set -eu` returns 0 and
  # the node is silently left with a partial install.
  mkdir -p "$tmp/src/aaa-uncopyable"
  render_script "$tmp/dst" "$tmp/src" > "$tmp/s.sh"

  if sh "$tmp/s.sh" 2>/dev/null; then echo "FAIL: script reported success despite failing to install a plugin"; false; fi
}
