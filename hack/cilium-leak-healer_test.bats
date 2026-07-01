#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pure decision helper in
# hack/e2e-cilium-endpoint-leak-healer.sh -- specifically ip_is_node_ingress,
# the gate that decides whether the disputed IP is THIS node's per-node Cilium
# ingress IP (CiliumNode.spec.ingress.ipv4). When it matches, the healer skips
# the futile/harmful Tier-2 agent restart; when it does not, the normal Tier-1/
# Tier-2 flow runs unchanged.
#
# The match is safety-critical: a false positive would suppress a cilium-agent
# restart that would otherwise clear a real in-memory endpoint leak, so the gate
# must (a) require an EXACT string match and (b) NEVER match on an empty ingress
# IP (CiliumNode absent, field unset, or RBAC denied). These tests pin exactly
# that contract; the kubectl lookup itself needs a live cluster and is not
# unit-testable here.
#
# Strategy mirrors hack/capture-dataplane.bats: the script is sourced once with
# CILIUM_LEAK_HEALER_LIB set, which its sourcing guard honours by defining the
# helpers and returning before the heal loop -- so no cluster is required and
# the loop never executes. Assertions use plain `[ ... ]` / if-then-false, not a
# `run` helper (cozytest.sh has no `run`). Mock IPs use the RFC 5737
# documentation range (TEST-NET-1, 192.0.2.0/24).
#
# Run with: hack/cozytest.sh hack/cilium-leak-healer_test.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-cilium-endpoint-leak-healer.sh"

# Load the pure helpers. The guard returns before the heal loop, so this is
# side-effect-free and needs no cluster.
CILIUM_LEAK_HEALER_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

@test "ip_is_node_ingress matches when the disputed IP equals the node ingress IP" {
  ip_is_node_ingress 192.0.2.131 192.0.2.131
}

@test "ip_is_node_ingress does not match a different IP so the Tier two restart still runs" {
  if ip_is_node_ingress 192.0.2.55 192.0.2.131; then
    echo "BUG: a non-ingress IP must not match the node ingress IP"
    false
  fi
}

@test "ip_is_node_ingress never matches an empty ingress IP so an unconfirmed lookup cannot skip a restart" {
  if ip_is_node_ingress 192.0.2.131 ""; then
    echo "BUG: an empty ingress IP (CiliumNode absent or RBAC denied) must never match"
    false
  fi
}

@test "ip_is_node_ingress never matches when both inputs are empty" {
  if ip_is_node_ingress "" ""; then
    echo "BUG: empty disputed IP and empty ingress IP must not be treated as a match"
    false
  fi
}
