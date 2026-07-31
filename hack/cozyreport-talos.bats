#!/usr/bin/env bats
# Regression coverage for host-node Talos diagnostics in hack/cozyreport.sh.
# The full report needs a cluster; this test sources only the focused helper and
# replaces talosctl with a shell function that records the exact argument vector.

load test_helper

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/cozyreport.sh"

COZYREPORT_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

# The talosctl mock stays at top level rather than nested inside a test. That
# was forced by cozytest.sh's parser, which ended an @test block at the first
# bare `}`; bats does not, but a shared top-level mock is clearer anyway.
talosctl() {
  printf '%s\n' "$*" >> "$calls"
}

@test "Talos host diagnostics pass an endpoint and use valid dmesg flags" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  calls="$tmp/talosctl.calls"

  cozyreport_collect_talos_node /workspace/talosconfig 192.0.2.11 "$tmp"

  [ "$(sed -n '1p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 dmesg" ]
  [ "$(sed -n '2p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 logs kubelet --tail=500" ]
  [ "$(sed -n '3p' "$calls")" = "--talosconfig /workspace/talosconfig -e 192.0.2.11 -n 192.0.2.11 logs containerd --tail=500" ]
  case "$(sed -n '1p' "$calls")" in *--tail*)
    echo "dmesg must not receive the boolean --tail flag" >&2
    false
  esac
}
