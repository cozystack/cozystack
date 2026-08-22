#!/usr/bin/env bats
# Regression coverage for host-node Talos diagnostics in hack/cozyreport.sh.
# The full report needs a cluster; this test sources only the focused helper and
# replaces talosctl with a shell function that records the exact argument vector.

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/cozyreport.sh"

COZYREPORT_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

# A PATH stub, not a shell function. These reads now go through the bounded
# reader, which runs them under `timeout` -- an external binary that execs its
# argument and therefore cannot see a function defined in this shell. A function
# mock is not merely bypassed, it is bypassed SILENTLY: the real talosctl runs, and
# what the test then reports is whatever the machine happens to have installed.
#
# cozytest.sh's parser ends an @test block at the first bare `}`, so the helper
# stays at top level rather than nested inside a test.
talos_stub_dir() {
  _td=$1
  mkdir -p "$_td/bin"
  cat > "$_td/bin/talosctl" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$TALOSCTL_CALLS"
[ -z "${TALOSCTL_FAIL:-}" ] && exit 0
echo 'error: rpc error: code = Unavailable desc = connection refused' >&2
exit 1
STUB
  chmod +x "$_td/bin/talosctl"
  # And a `timeout` that runs its command rather than timing it, so the assertions
  # do not depend on the host having GNU coreutils.
  printf '#!/bin/sh\n[ "$1" = "-k" ] && shift 2\nshift\nexec "$@"\n' > "$_td/bin/timeout"
  chmod +x "$_td/bin/timeout"
  COZYREPORT_BOUND="timeout -k 5 $COZYREPORT_READ_TIMEOUT"
}

@test "Talos host diagnostics pass an endpoint and use valid dmesg flags" {
  # Cleanup at the end of the body, never a `trap ... EXIT`. An EXIT trap inside
  # an @test replaces the one the bats binary installs for its own bookkeeping, and
  # a test that then fails prints no TAP line at all -- it does not appear as
  # `not ok`, it disappears, so a reader grepping the output sees a green suite.
  # See docs/agents/e2e-testing.md. Both runners set `set -e`, so on a failure the
  # cleanup line is never reached and the directory survives for inspection, which
  # is what you want from a test that just failed.
  tmp=$(mktemp -d)
  talos_stub_dir "$tmp"
  calls="$tmp/talosctl.calls"

  TALOSCTL_CALLS="$calls" PATH="$tmp/bin:$PATH" \
    cozyreport_collect_talos_node /workspace/talosconfig 192.0.2.11 "$tmp"

  [ "$(sed -n '1p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 dmesg" ]
  [ "$(sed -n '2p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 logs kubelet --tail=500" ]
  [ "$(sed -n '3p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 logs containerd --tail=500" ]
  case "$(sed -n '1p' "$calls")" in *--tail*)
    echo "dmesg must not receive the boolean --tail flag" >&2
    false
  esac

  rm -rf "$tmp"
}

@test "a Talos node that refuses the connection leaves the reason in its files" {
  tmp=$(mktemp -d)
  talos_stub_dir "$tmp"
  calls="$tmp/talosctl.calls"

  TALOSCTL_FAIL=1 TALOSCTL_CALLS="$calls" PATH="$tmp/bin:$PATH" \
    cozyreport_collect_talos_node /workspace/talosconfig 192.0.2.11 "$tmp"

  # The reads are best-effort and must not abort the report, but "did not abort"
  # and "produced nothing" are different outcomes and only one of them is about
  # the node. Each read merges stderr into its own file, so a node that refused
  # the connection says so where a reader looking for its dmesg will find it,
  # rather than leaving three empty files that read like a quiet node.
  for f in dmesg.txt kubelet.log containerd.log; do
    t="$tmp/talos-192.0.2.11-$f"
    [ -s "$t" ] || { echo "FAIL: $t is empty on a refused connection"; false; }
    grep -q 'connection refused' "$t" || { echo "FAIL: $t does not carry the reason"; cat "$t"; false; }
  done
  rm -rf "$tmp"
}
