#!/usr/bin/env bats
# Asserts that the sandbox Kubernetes cluster CIDR overlaps none of the address
# ranges the sandbox platform is configured with (pod, service and join).
#
# Two allocators are live on this datapath and neither knows about the other.
# kube-ovn owns pod IPAM and allocates flat from POD_CIDR across all nodes.
# Cilium allocates nothing for pods -- it is chained behind kube-ovn -- but it
# does carve its own per-node infrastructure addresses out of
# Node.spec.podCIDR, which kube-controller-manager slices from the cluster
# CIDR: the router IP on cilium_host, and the per-node Ingress IP that Gateway
# API brings with it. Let the two ranges overlap and kube-ovn eventually hands
# a pod an address cilium already holds.
#
# On the Ingress IP that is loud: the agent refuses the sandbox with "IP
# ipv4:<addr> is already in use". Cilium never relents on the address, so the
# pod recovers only if something puts it on a different one. Whether that
# happens is not a property of this collision: hack/e2e-cilium-endpoint-leak-
# healer.sh records deleting the wedged pod landing it on a clean address, and
# in the failure this guard comes from the same address was refused seven times
# across six distinct pod UIDs before the suite ran out of budget. On the
# router IP it is silent instead: the endpoint is created, the pod runs, and
# only host-to-pod traffic breaks, because to the node's own kernel that
# address is local.
#
# Cilium picks those offsets by random scan over the node's range, so excluding
# a fixed set of addresses on the kube-ovn side does not work -- the ranges
# have to be disjoint.
#
# The invariant is checked, not the literals: any of the values may move, and
# this still passes as long as none of them overlap. An extraction that comes
# up empty, duplicated or misshapen fails the test instead of shrinking the
# comparison, and comments and blank lines inside the list are skipped rather
# than read as the end of it. This is not a YAML parser, though. Anything it
# does not read as a bare dotted quad fails the shape validation rather than
# being understood, and that set is wider than the exotic shapes: an anchor, a
# nested flow mapping, but also a plainly quoted scalar. Quoting is asymmetric
# between the two sides as well -- a platform key is read only when its value
# is double-quoted, a Talos entry only when its value is not. All of those are
# legal reformats and all of them turn the guard red with the offending value
# in the message, which is the safe direction but not the same promise.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure. A
# `!`-negated command never trips errexit, so every assertion below is written
# as an explicit `if ... return 1`. A top-level helper function would have
# `return 0` injected before its closing brace by the runner's awk, which is
# why the arithmetic is inlined instead. Paths are repo-root-relative.
#
# Run with: hack/cozytest.sh hack/sandbox-cidr-disjoint.bats

@test "sandbox cluster CIDR overlaps no platform range" {
  talos_file=hack/e2e-prepare-cluster.bats
  install_file=hack/e2e-install-cozystack.bats

  # Talos cluster.network.podSubnets: every list item under the key, including
  # ones separated from the first by a comment or a blank line, so a second
  # entry cannot slip past unexamined. An IPv6 entry does not get an overlap
  # check, it fails the shape validation below; teaching this guard dual-stack
  # means extending it, not just adding the value.
  cluster_cidrs=$(awk '
    /^[[:space:]]*podSubnets:[[:space:]]*$/ { inlist = 1; next }
    inlist && /^[[:space:]]*-[[:space:]]+[^[:space:]]/ { print $2; next }
    # A comment or a blank line between two entries is not the end of the
    # list. Treating it as the end would drop every entry below it and still
    # leave a non-empty result, which is the one way this extraction could go
    # green while checking less than it says. The block comment that landed
    # above podSubnets alongside this guard makes that the likely shape rather
    # than a rare one; the guard does not require that comment, it just has to
    # survive it and whatever gets written next to it later.
    inlist && /^[[:space:]]*(#|$)/ { next }
    inlist { inlist = 0 }
  ' "$talos_file")

  if [ -z "$cluster_cidrs" ]; then
    echo "found no podSubnets entries in $talos_file" >&2
    echo "the extraction below is keyed on 'podSubnets:' followed by a list" >&2
    return 1
  fi

  # The ranges the install step hands the platform chart. podCIDR is the one
  # that collides today; serviceCIDR and joinCIDR are here because the cluster
  # CIDR must miss all three, and picking a replacement that lands on the join
  # range instead is the obvious next way to get this wrong. These three are
  # the allocator ranges and nothing else: the sandbox node network, the
  # load-balancer pool and the transit range of an off-by-default component
  # are not read here, so this list is not a complete map of what the cluster
  # CIDR must avoid.
  platform_cidrs=
  for key in podCIDR serviceCIDR joinCIDR; do
    # `|| true`: grep -c exits 1 when it counts zero, and under the runner's
    # errexit that kills the test on the assignment, before the message below
    # can name which key went missing. The outcome is red either way; this is
    # only about saying why.
    count=$(grep -cE "^[[:space:]]*${key}:[[:space:]]*\"" "$install_file" || true)
    # grep exits 2 with no stdout when the file itself is gone, which leaves
    # count empty and turns the arithmetic below into "integer expression
    # expected". Still red, but named after the wrong thing.
    count=${count:-0}
    if [ "$count" -ne 1 ]; then
      echo "expected exactly one $key key in $install_file, found $count" >&2
      return 1
    fi
    value=$(awk -F'"' -v k="$key" '$0 ~ "^[[:space:]]*" k ":[[:space:]]*\"" { print $2; exit }' "$install_file")
    # A present-but-empty value passes the count check above, then disappears
    # in the unquoted word split below, dropping that range from the
    # comparison with nothing to show for it. Refuse instead.
    if [ -z "$value" ]; then
      echo "$key in $install_file is present but empty" >&2
      return 1
    fi
    platform_cidrs="$platform_cidrs $value"
  done

  for cidr in $cluster_cidrs $platform_cidrs; do
    if printf '%s\n' "$cidr" | grep -qvE '^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$'; then
      echo "extracted value is not an IPv4 CIDR: '$cidr'" >&2
      return 1
    fi
  done

  for cluster_cidr in $cluster_cidrs; do
    for platform_cidr in $platform_cidrs; do
      # Two prefixes overlap when, masked to the shorter of the two lengths,
      # their network addresses are equal. Symmetric by construction: neither
      # argument is privileged, so containment is caught whichever way round
      # it is. awk arithmetic is IEEE double, exact well past the 2^32 needed.
      if printf '%s\n%s\n' "$cluster_cidr" "$platform_cidr" | awk '
        function toint(a,   p) {
          split(a, p, ".")
          return ((p[1] * 256 + p[2]) * 256 + p[3]) * 256 + p[4]
        }
        NR == 1 { split($0, a, "/"); ip1 = toint(a[1]); len1 = a[2] }
        NR == 2 { split($0, b, "/"); ip2 = toint(b[1]); len2 = b[2] }
        END {
          shorter = (len1 < len2 ? len1 : len2)
          block = 2 ^ (32 - shorter)
          exit (int(ip1 / block) == int(ip2 / block)) ? 0 : 1
        }
      '; then
        echo "sandbox cluster CIDR $cluster_cidr ($talos_file)" >&2
        echo "overlaps platform range $platform_cidr ($install_file)" >&2
        echo "cilium carves its per-node router and Ingress addresses out of the cluster CIDR." >&2
        echo "Pick a cluster CIDR that misses every range the platform is configured with." >&2
        return 1
      fi
    done
  done
}
