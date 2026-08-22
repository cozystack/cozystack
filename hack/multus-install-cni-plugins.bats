#!/usr/bin/env bats
# EXIT-TRAP DEBT: 12 -- see hack/bats-no-exit-trap.bats; lower it as the traps go, delete it at zero.
# Behavioural tests for the multus install-cni-plugins init container script:
# extracted from the rendered DaemonSet and run. The helm-unittest cases in
# packages/system/multus/tests/multus_test.yaml match its source TEXT instead.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. The runner has no setup/teardown, so each
# @test builds its own fixture.

CHART=packages/system/multus

# Extract the init container's script from the rendered chart into $1 and rebind
# its two absolute paths ($2 = host cni bin, $3 = plugin source) so it can run
# unprivileged. The non-empty check must be an early `return 1`: hack/cozytest.sh
# rewrites every line matching ^}$ into `return 0` plus `}`, helpers included.
render_script() {
  out=$1
  # The chart's own default is off -- the platform turns it on per bundle -- so
  # the init container this file tests only exists when it is set here.
  helm template --set stageCniPlugins=true "$CHART" \
    | yq eval 'select(.kind == "DaemonSet") | .spec.template.spec.initContainers[] | select(.name == "install-cni-plugins") | .command[2]' - \
    | sed -e "s|/host/opt/cni/bin|$2|g" -e "s|/cni-plugins|$3|g" \
          -e "s|/dev/termination-log|${out}.termlog|g" > "$out"
  [ -s "$out" ] || return 1

  # Nothing may still point at a real /opt/cni/bin: the rebinding substitutes
  # /host/opt/cni/bin literally, and these tests EXECUTE the script.
  if grep -q '/opt/cni/bin' "$out"; then
    echo "rendered script still references a real /opt/cni/bin after rebinding;" >&2
    echo "refusing to execute it. Check that the manifest still writes through" >&2
    echo "/host/opt/cni/bin, which is what this rebinding substitutes." >&2
    return 1
  fi
}

# The names the image actually stages, read from the Dockerfile rather than
# repeated here: a script that skips a plugin by name is only exercised against
# a fixture that contains that name.
plugin_names() {
  sed -n '/tar -xz -C \/cni-plugins -f /,/;/p' \
    packages/system/multus/images/multus-cni/Dockerfile |
    tr ' ' '\n' | sed -n 's|^\./||p' | tr -d ';' | sort
}

# A fake image payload: plugin binaries that report their own name when run.
make_plugins() {
  mkdir -p "$1"
  for p in $(plugin_names); do
    printf '#!/bin/sh\necho %s-NEW\n' "$p" > "$1/$p"
    chmod 0755 "$1/$p"
  done
}

# Put an `rm` in $1 that refuses any path containing $2, leaving it in place, and
# delegates the rest. Injected rather than provoked with permissions: root's DAC
# override unlinks through a forbidding parent, so a filesystem-shaped fixture
# stops reproducing wherever the suite runs as root. $2 is narrow so staging can
# still succeed around one bad path.
stub_failing_rm() {
  mkdir -p "$1"
  sed -e "s|@MATCH@|$2|" > "$1/rm" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case $a in
    *@MATCH@*)
      echo "rm: $a: Read-only file system" >&2
      exit 1
      ;;
  esac
done
exec /bin/rm "$@"
STUB
  chmod 0755 "$1/rm"
}

@test "a render that produces no script fails instead of passing as a no-op" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/dst"
  # Guards the guard. Two tests below assert nothing landed in the destination,
  # so an unrendered script satisfies them. This also fails if the check is
  # moved into last-command position, where cozytest.sh's `return 0` rewrite
  # swallows its status. Each @test runs in its own subshell, so reassigning
  # CHART cannot leak.
  CHART=$tmp/no-such-chart
  if render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src" 2>/dev/null; then
    echo "FAIL: render_script reported success with no chart to render"
    false
  fi
}

@test "installs every staged plugin into the host cni bin dir, executable" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh"

  names=$(plugin_names)
  [ -n "$names" ]
  [ "$(printf '%s\n' "$names" | grep -c .)" = "14" ]
  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "14" ]
  # Each by name: a count is satisfied by that many of anything, and one plugin
  # landing unexecutable leaves the others to carry the assertion.
  for p in $names; do
    [ -f "$tmp/dst/$p" ]
    [ -x "$tmp/dst/$p" ]
    [ "$("$tmp/dst/$p")" = "$p-NEW" ]
  done
}

@test "installs a plugin executable even when the staged copy is not" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # cp reproduces the source mode, so the chmod is only load-bearing when a
  # staged plugin is not already 0755.
  chmod 0644 "$tmp/src/bridge"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh"

  # Two statements, not `[ -f X ] && [ -x X ]`: a failing command that is not the
  # last in an AND-OR list is exempt from errexit, so the pair silently continues
  # into cozytest.sh's appended `return 0` and the whole assertion is discarded.
  # Measured in dash and bash -- the `-f` half of every such pair was dead.
  [ -f "$tmp/dst/bridge" ]
  [ -x "$tmp/dst/bridge" ]
}

@test "replaces a plugin by rename, not by writing onto the live path" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # Stand in for a plugin another CNI already installed and the kubelet may exec.
  printf '#!/bin/sh\necho portmap-OLD\n' > "$tmp/dst/portmap"
  chmod 0755 "$tmp/dst/portmap"
  before=$(ls -i "$tmp/dst/portmap" | awk '{print $1}')

  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh"

  after=$(ls -i "$tmp/dst/portmap" | awk '{print $1}')
  [ "$("$tmp/dst/portmap")" = "portmap-NEW" ]
  # A rename swaps in a new inode, so an exec holding the old one keeps a
  # complete binary; a cp onto the live path truncates and rewrites in place.
  if [ "$before" = "$after" ]; then echo "FAIL: portmap replaced in place (inode $before unchanged) — copy was not atomic"; false; fi
}

@test "leaves no temp files behind, and clears ones stranded by an earlier kill" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # Named for a plugin this image does not stage, so no iteration renames it
  # away -- only the cleanup can remove it.
  echo stranded > "$tmp/dst/.tmp-cozystack-multus-plugin-from-an-older-image"

  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh"

  [ "$(ls -a1 "$tmp/dst" | grep -c '^\.tmp-cozystack-multus-' || true)" = "0" ]
}

@test "skips staging instead of failing when the image predates the plugins" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/dst"
  # No source dir at all: the release-prep digest re-pin lags a Dockerfile
  # change, so the pinned image can have no /cni-plugins.
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/absent"
  sh "$tmp/s.sh" 2>"$tmp/err"

  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "0" ]
  # The empty destination alone is satisfied by the guard being gone: the glob
  # stays literal and `[ -e "$plugin" ]` skips it. Only this line separates them.
  grep -q 'in this image; skipping staging' "$tmp/err"
  # And nothing may be reported as failed: a skip is not a partial install.
  if grep -q 'failed to install:' "$tmp/err"; then
    echo "FAIL: an absent plugin directory was reported as failed installs"
    cat "$tmp/err"
    false
  fi
}

@test "does not fail when the staged plugin directory is empty" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/src" "$tmp/dst"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  # An unmatched glob stays literal in sh, so without the `[ -e ]` guard the loop
  # runs once for a path that does not exist and reports a plugin named `*`.
  sh "$tmp/s.sh" 2>"$tmp/err"

  [ "$(ls -1 "$tmp/dst" | wc -l | tr -d ' ')" = "0" ]
  # Asserted, because the empty destination alone is satisfied by the guard
  # being gone.
  if grep -q 'failed to install:' "$tmp/err"; then
    echo "FAIL: an empty plugin directory was reported as failed installs"
    cat "$tmp/err"
    false
  fi
}

@test "a leftover directory is cleared, and the plugin installs as a file" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # The temp path as an ordinary directory `rm -rf` can clear: a previous run
  # killed between the cp and the rename. Staging proceeds.
  #
  # The assertions check for a regular FILE: `-x` alone is true of a directory.
  mkdir -p "$tmp/dst/.tmp-cozystack-multus-bridge"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh"

  [ -f "$tmp/dst/bridge" ]
  [ -x "$tmp/dst/bridge" ]
  [ "$("$tmp/dst/bridge")" = "bridge-NEW" ]
}

@test "a temp path that will not clear is refused, not published as a plugin" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # A temp path that is still a directory when the copy runs: cp writes INTO it,
  # chmod works on it, and mv publishes it as the plugin, because a rename onto
  # a free name succeeds. The runtime cannot exec a directory.
  mkdir -p "$tmp/dst/.tmp-cozystack-multus-bridge"
  stub_failing_rm "$tmp/bin" ".tmp-cozystack-multus-bridge"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"

  # Still must not fail the container. hostNetwork pods bypass CNI and would
  # start either way; every other pod on the node would not.
  if ! PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" 2>"$tmp/err"; then
    echo "FAIL: an unclearable temp path failed the container"
    cat "$tmp/err"
    false
  fi
  if [ -d "$tmp/dst/bridge" ]; then
    echo "FAIL: a directory was published as the plugin"
    ls -la "$tmp/dst/bridge"
    false
  fi
  # Refused, so nothing is there at all -- not a half-installed something.
  if [ -e "$tmp/dst/bridge" ]; then
    echo "FAIL: expected bridge to be absent after a refusal"
    false
  fi
  # The injection has to have happened: if PATH stopped being honoured, the real
  # `rm` would clear the fixture and every assertion above would still pass.
  if [ ! -d "$tmp/dst/.tmp-cozystack-multus-bridge" ]; then
    echo "FAIL: the temp path was removed, so no failure was injected;"
    echo "      this test proved nothing. Check that stub_failing_rm is on PATH"
    echo "      and that the script still invokes rm by bare name."
    false
  fi
  # And WHICH plugin: anchored at line start, matched as a whole token. The stub
  # prints the bridge path itself, `.*bridge` would also accept `bridgeport`,
  # and without `^` the contradictory `not failed to install:` passes too.
  grep -qE '^failed to install:( [^ ]+)* bridge( |$)' "$tmp/err"
  # The termination log is the channel the stub cannot write to, so assert there
  # too -- it is also what `kubectl describe` shows.
  grep -qE '^failed to install:( [^ ]+)* bridge( |$)' "$tmp/s.sh.termlog"
  # The other plugins are unaffected: one bad temp path is not a stop signal.
  [ -f "$tmp/dst/macvlan" ]
  [ -x "$tmp/dst/macvlan" ]
  [ "$("$tmp/dst/macvlan")" = "macvlan-NEW" ]
}

@test "refuses to nest a plugin inside a directory that already holds its name" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # mv moves its source INTO an existing directory, under the SOURCE's basename
  # -- the temp name, never <dst>/bridge/bridge. Assert the directory gained no
  # entries: a temp file one level down is deeper than the trap's glob.
  mkdir -p "$tmp/dst/bridge/occupied"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"
  sh "$tmp/s.sh" 2>"$tmp/err"

  nested=$(ls -A "$tmp/dst/bridge" | grep -v '^occupied$' || :)
  if [ -n "$nested" ]; then
    echo "FAIL: something was moved inside the existing directory: $nested"
    false
  fi
  # One line, summary and name together: as two greps this passes when the
  # summary names a different plugin and `mv` merely printed this one.
  grep -qE '^failed to install:( [^ ]+)* bridge( |$)' "$tmp/err"
  # And a CAUSE: this is the one branch that records a failure no command
  # printed an error for, so without this line the summary is unexplained.
  grep -q 'is a directory, not a plugin binary' "$tmp/err"
}

@test "survives a leftover it cannot clean up" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # A leftover only the trap's glob reaches. Its `rm` is guarded because a
  # command failing inside an EXIT trap sets the status even after `exit 0`.
  mkdir -p "$tmp/dst/.tmp-cozystack-multus-stranded"
  stub_failing_rm "$tmp/bin" "stranded"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"

  if ! PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" 2>"$tmp/err"; then
    echo "FAIL: a leftover that could not be removed failed the container;"
    echo "      every pod that needs CNI would then fail to start here"
    cat "$tmp/err"
    false
  fi
  # The leftover has to have SURVIVED: that is the only proof the failure was
  # injected, since a real `rm -rf` would clear it and staging would succeed.
  if [ ! -d "$tmp/dst/.tmp-cozystack-multus-stranded" ]; then
    echo "FAIL: the stranded leftover was removed, so no failure was injected;"
    echo "      this test proved nothing about the EXIT trap's guard."
    false
  fi
  # And the staging still has to have happened.
  [ -f "$tmp/dst/bridge" ]
  [ -x "$tmp/dst/bridge" ]
  [ "$("$tmp/dst/bridge")" = "bridge-NEW" ]
}

@test "reports a plugin it could not install, and still leaves the node usable" {
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  make_plugins "$tmp/src"; mkdir -p "$tmp/dst"
  # An entry cp cannot copy, sorted ahead of the good plugins so a later
  # iteration still runs.
  mkdir -p "$tmp/src/aaa-uncopyable"
  # Plus a leftover the trap cannot remove, so both failure paths run at once:
  # unguarded, the script dies in the EXIT trap before the summary.
  mkdir -p "$tmp/dst/.tmp-cozystack-multus-stranded"
  stub_failing_rm "$tmp/bin" "stranded"
  render_script "$tmp/s.sh" "$tmp/dst" "$tmp/src"

  # Both halves: exit 0 alone would pass with the message dropped, and the log
  # line is the only remaining signal.
  if ! PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" 2>"$tmp/err"; then
    echo "FAIL: a plugin that could not be installed failed the container;"
    echo "      every pod that needs CNI would then fail to start here"
    false
  fi
  # The script's own wording AND the name, as one line: cp prints the path it
  # could not copy, so either half alone passes with the report deleted.
  if ! grep -qE '^failed to install:( [^ ]+)* aaa-uncopyable( |$)' "$tmp/err"; then
    echo "FAIL: the script did not report the plugin it could not install"
    cat "$tmp/err"
    false
  fi

  # And into the termination log, which is what `kubectl describe` shows.
  # invisible to anyone not reading that container's logs specifically.
  if ! grep -q 'failed to install:.*aaa-uncopyable' "$tmp/s.sh.termlog" 2>/dev/null; then
    echo "FAIL: the failure was not written to the termination log"
    false
  fi

  # The stranded leftover has to have survived, or the injected failure never
  # happened and the both-go-wrong path was not exercised.
  if [ ! -d "$tmp/dst/.tmp-cozystack-multus-stranded" ]; then
    echo "FAIL: the stranded leftover was removed, so no rm failure was injected"
    false
  fi

  # The failure must not take the rest of the set with it.
  [ -f "$tmp/dst/bridge" ]
  [ -x "$tmp/dst/bridge" ]
  [ "$("$tmp/dst/bridge")" = "bridge-NEW" ]
  # And no temp FILE of its own. The planted directory is expected to survive:
  # stub_failing_rm refuses that path, which is what makes the cleanup fail.
  # Counting only regular files keeps the fixture from hiding a real leak.
  [ "$(find "$tmp/dst" -maxdepth 1 -type f -name '.tmp-cozystack-multus-*' | wc -l | tr -d ' ')" = "0" ]
}
