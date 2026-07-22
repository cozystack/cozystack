# shellcheck shell=sh
# Shared probe + attribution helpers for the site-router e2e suite. Sourced by
# the reachability, MSS and negative-security script steps (a Chainsaw script's
# cwd is the suite directory, so `. ./_probe-lib.sh` resolves here).
#
# Every helper below is a STUB that fails loudly. They are the seam between the
# offline-authored suite (this PR) and the live empirical validation (T13): the
# suite is deferred from CI until the VyOS golden image is published, and these
# probes cannot be exercised without the live gateway, a driveable remote-site B
# guest, Hubble, and the VyOS firewall-counter API. Implementing them against the
# live image is an explicit T13 empirical deliverable.
#
# They RETURN NON-ZERO until implemented so the negative-security GATE can never
# pass vacuously: any run before wiring fails with a clear TODO, never green.
#
# TODO(T13): implement all helpers against the live cluster.

# probe_from_b <proto> <dst> <port>
#   Run a single probe from remote-site B's guest, sourced from its 192.0.2.x
#   dummy interface, routed into the tunnel toward A. Return 0 if the target is
#   REACHABLE (packet delivered / connection succeeded), non-zero if not.
#   Mechanism (live): exec into B's guest via the VyOS API / serial console /
#   ssh and run ping|curl|nc from source 192.0.2.1.
probe_from_b() {
  echo "TODO(T13): probe_from_b not implemented — drive B's guest live (proto=$1 dst=$2 port=${3:-})" >&2
  return 64
}

# probe_from_b_stdout <proto> <dst> <port> <path>
#   Like probe_from_b but returns the RESPONSE BODY on stdout (used for the
#   source-preservation check: curl B → backend /clientip, echo the observed
#   client IP). Sourced from ${PROBE_SRC:-192.0.2.1}.
probe_from_b_stdout() {
  echo "TODO(T13): probe_from_b_stdout not implemented — drive B's guest live (proto=$1 dst=$2 port=$3 path=${4:-/})" >&2
  return 64
}

# transfer_from_b <dst> <port> <megabytes>
#   Push <megabytes> MB from B to <dst> across the tunnel and require completion
#   (a stalled MSS clamp hangs until the caller's step timeout). Return 0 on a
#   completed transfer.
transfer_from_b() {
  echo "TODO(T13): transfer_from_b not implemented — bulk transfer from B's guest live (dst=$1 port=$2 mb=${3:-})" >&2
  return 64
}

# hubble_dropped <dst> <reason-substring>
#   Assert Hubble recorded a DROPPED flow toward <dst> whose drop reason/verdict
#   matches <reason-substring> (e.g. POLICY_DENIED for the Cilium egressDeny /
#   tenant-isolation drops). Return 0 if such a dropped flow is observed.
#   Mechanism (live): hubble observe --to-ip <dst> --verdict DROPPED on the
#   cilium/hubble-relay pod.
hubble_dropped() {
  echo "TODO(T13): hubble_dropped not implemented — query Hubble live (dst=$1 reason=${2:-})" >&2
  return 64
}

# guest_counter_incremented <ruleset> <rule>
#   Assert the named VyOS firewall rule's drop counter on gateway A advanced,
#   proving the guest firewall (not merely absent connectivity) performed the
#   drop. Rulesets/rules of interest: TUNNEL-INGRESS default-action (source /
#   world-destination drops), firewall input rule 1 (Boundary-A management drop),
#   firewall forward default-action (tunnel->world default deny).
#   Mechanism (live): query `show firewall ... rule <n>` counters over the VyOS
#   management API with A's api-key Secret.
guest_counter_incremented() {
  echo "TODO(T13): guest_counter_incremented not implemented — read VyOS counters live (ruleset=$1 rule=${2:-})" >&2
  return 64
}

# assert_dropped <desc> <guard-check> <proto> <dst> [port]
#   The negative-security primitive: a probe from B to <dst> MUST NOT reach it,
#   AND the named guard MUST be the one that dropped it (attribution — the design
#   requires establishing the responsible guard, not merely the absence of
#   connectivity). <guard-check> is a command (with args) that returns 0 when the
#   attributing guard is confirmed, e.g. `hubble_dropped 169.254.169.254 POLICY_DENIED`.
assert_dropped() {
  desc=$1; guard=$2; proto=$3; dst=$4; port=${5:-}
  if probe_from_b "$proto" "$dst" "$port"; then
    echo "FAIL [$desc]: B reached $dst:$port — expected DROP" >&2
    return 1
  fi
  if ! sh -c "$guard"; then
    echo "FAIL [$desc]: $dst was unreachable but the drop was NOT attributed to the expected guard ($guard)" >&2
    return 1
  fi
  echo "OK [$desc]: $dst dropped and attributed to its guard"
}
