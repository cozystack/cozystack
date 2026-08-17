#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for talos_spec_block in hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# It is the composition point for the tenant Kubernetes CR's `spec.talos`: the
# ghcr.io pull-through mirror contributes registryMirrors when it is up, and
# nothing when it is not. The result is spliced into an unquoted heredoc directly
# under `spec:`, so an indentation slip or a stray `talos:` key does not fail this
# function -- it produces a CR the API rejects, and every kubernetes-* suite dies
# at tenant creation with an error that points at the CR rather than at this line.
#
# It used to compose two independent fragments, the second being the Talos OS
# image cache's imageFactoryURL, which is why it is a composition point at all and
# not a one-line printf. That cache is gone: workers boot by CDI-cloning the golden
# Talos image in cozy-public rather than importing the OS image over HTTP per
# worker, so nothing selects a factory URL at e2e time any more. The shape is kept
# because the emptiness case and the single-`talos:`-key invariant are what the
# heredoc splice actually rides on, and both survive the fragment count.
#
# The resolver is replaced with a stub after sourcing, which reduces this to the
# pure composition it is: it has its own coverage (hack/ghcr-mirror_test.bats) and
# needs a cluster to mean anything.
#
# The assertions go through yq on a spliced document rather than through grep on
# the string, so what they pin is "the CR parses and the keys land under
# spec.talos", which is the actual contract. A string match would keep passing on a
# block indented into the wrong parent.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`. Sourcing run-kubernetes.sh only
# defines functions and touches no cluster.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-talos-spec_test.bats
# -----------------------------------------------------------------------------

# Splice a talos_spec_block result under `spec:` of a minimal CR and print the
# document, so yq can be asked what the tenant apiserver would see.
spec_doc() {
    printf 'apiVersion: apps.cozystack.io/v1alpha1\nkind: Kubernetes\nmetadata:\n  name: t\nspec:\n%s\n  host: ""\n' "$1"
}

@test "no mirror up renders nothing, so the chart defaults apply" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_ghcr_mirror_endpoint() { printf ''; }
    out=$(talos_spec_block)
    [ -z "$out" ] || { echo "expected no spec.talos block when the mirror is not up, got [$out]" >&2; exit 1; }
}

@test "the ghcr.io mirror up renders registryMirrors under one talos key" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_ghcr_mirror_endpoint() { printf 'http://ghcr-mirror.kube-system.svc'; }
    work=$(mktemp -d)
    block=$(talos_spec_block)
    spec_doc "$block" > "$work/cr.yaml"
    got=$(yq '.spec.talos.registryMirrors["ghcr.io"].endpoints[0]' "$work/cr.yaml")
    [ "$got" = "http://ghcr-mirror.kube-system.svc" ] || { echo "mirror endpoint did not land under spec.talos.registryMirrors, got [$got]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    # A second `talos:` would still parse -- yq keeps the last duplicate -- and
    # would silently drop whichever key came first, so count the key rather than
    # trusting the read above.
    keys=$(printf '%s\n' "$block" | grep -c '^  talos:$')
    [ "$keys" -eq 1 ] || { echo "expected exactly one talos key, found $keys" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "the worker OS disk is not sourced from spec.talos at all" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # The suites clone the golden image in cozy-public instead of importing over
    # HTTP, so an imageFactoryURL emitted here would point workers back at the
    # public factory for a disk they no longer fetch. Nothing would fail loudly:
    # the CR is valid either way and the group's image.builtin wins, so the key
    # would sit in the CR reading as the source of a disk it does not source.
    resolve_ghcr_mirror_endpoint() { printf 'http://ghcr-mirror.kube-system.svc'; }
    block=$(talos_spec_block)
    case "$block" in
        *imageFactoryURL*) echo "talos_spec_block still emits imageFactoryURL, which no longer sources the worker disk: [$block]" >&2; exit 1 ;;
    esac
    # And the group that boots the workers asks for the clone.
    lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
    grep -q '^      image:$' "$lib" \
        || { echo "expected the md0 node group in $lib to set an image source" >&2; exit 1; }
    grep -q '^        builtin: {}$' "$lib" \
        || { echo "expected the md0 node group in $lib to clone the golden via image.builtin" >&2; exit 1; }
}

@test "the key the sandbox keys off is spelled the way the chart declares it" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # talos_spec_block writes registryMirrors by hand. If the chart ever renames
    # it, the CR would be rejected only at e2e time, and this file is the cheapest
    # place to notice.
    yq -e '.talos | has("registryMirrors")' packages/apps/kubernetes/values.yaml >/dev/null \
        || { echo "packages/apps/kubernetes/values.yaml has no talos.registryMirrors; talos_spec_block would emit a key the chart rejects" >&2; exit 1; }
}
