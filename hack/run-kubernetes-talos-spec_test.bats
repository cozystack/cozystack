#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for talos_spec_block in hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# It is the composition point for the tenant Kubernetes CR's `spec.talos`: two
# independent in-sandbox mirrors (the Talos OS image cache's imageFactoryURL and
# the ghcr.io pull-through's registryMirrors) each contribute a fragment, and
# either, both or neither may be present on a given run. The result is spliced
# into an unquoted heredoc directly under `spec:`, so an indentation slip or a
# stray `talos:` key does not fail this function -- it produces a CR the API
# rejects, and every kubernetes-* suite dies at tenant creation with an error
# that points at the CR rather than at this line.
#
# Both resolvers are replaced with stubs after sourcing, which reduces this to
# the pure composition it is: the resolvers have their own coverage
# (hack/ghcr-mirror_test.bats) and need a cluster to mean anything.
#
# The four-combination assertions go through yq on a spliced document rather
# than through grep on the string, so what they pin is "the CR parses and the
# keys land under spec.talos", which is the actual contract. A string match
# would keep passing on a block indented into the wrong parent.
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
    resolve_talos_image_factory_url() { printf ''; }
    resolve_ghcr_mirror_endpoint() { printf ''; }
    out=$(talos_spec_block)
    [ -z "$out" ] || { echo "expected no spec.talos block when neither mirror is up, got [$out]" >&2; exit 1; }
}

@test "only the Talos image cache up renders imageFactoryURL and no registryMirrors" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_talos_image_factory_url() { printf 'http://talos-image-cache.kube-system.svc'; }
    resolve_ghcr_mirror_endpoint() { printf ''; }
    work=$(mktemp -d)
    spec_doc "$(talos_spec_block)" > "$work/cr.yaml"
    got=$(yq '.spec.talos.imageFactoryURL' "$work/cr.yaml")
    [ "$got" = "http://talos-image-cache.kube-system.svc" ] || { echo "imageFactoryURL did not land under spec.talos, got [$got]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    mirrors=$(yq '.spec.talos.registryMirrors' "$work/cr.yaml")
    [ "$mirrors" = "null" ] || { echo "expected no registryMirrors when the ghcr mirror is down, got [$mirrors]" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "only the ghcr.io mirror up renders registryMirrors and no imageFactoryURL" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_talos_image_factory_url() { printf ''; }
    resolve_ghcr_mirror_endpoint() { printf 'http://ghcr-mirror.kube-system.svc'; }
    work=$(mktemp -d)
    spec_doc "$(talos_spec_block)" > "$work/cr.yaml"
    got=$(yq '.spec.talos.registryMirrors["ghcr.io"].endpoints[0]' "$work/cr.yaml")
    [ "$got" = "http://ghcr-mirror.kube-system.svc" ] || { echo "mirror endpoint did not land under spec.talos.registryMirrors, got [$got]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    url=$(yq '.spec.talos.imageFactoryURL' "$work/cr.yaml")
    [ "$url" = "null" ] || { echo "expected no imageFactoryURL when the Talos cache is down, got [$url]" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "both mirrors up render under one talos key" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_talos_image_factory_url() { printf 'http://talos-image-cache.kube-system.svc'; }
    resolve_ghcr_mirror_endpoint() { printf 'http://ghcr-mirror.kube-system.svc'; }
    work=$(mktemp -d)
    block=$(talos_spec_block)
    spec_doc "$block" > "$work/cr.yaml"
    url=$(yq '.spec.talos.imageFactoryURL' "$work/cr.yaml")
    ep=$(yq '.spec.talos.registryMirrors["ghcr.io"].endpoints[0]' "$work/cr.yaml")
    [ "$url" = "http://talos-image-cache.kube-system.svc" ] || { echo "imageFactoryURL missing when both mirrors are up, got [$url]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    [ "$ep" = "http://ghcr-mirror.kube-system.svc" ] || { echo "mirror endpoint missing when both mirrors are up, got [$ep]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    # A second `talos:` would still parse -- yq keeps the last duplicate -- and
    # would silently drop whichever key came first, so count the key rather than
    # trusting the reads above.
    keys=$(printf '%s\n' "$block" | grep -c '^  talos:$')
    [ "$keys" -eq 1 ] || { echo "expected exactly one talos key, found $keys" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "the key the sandbox keys off is spelled the way the chart declares it" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # talos_spec_block writes registryMirrors and imageFactoryURL by hand. If the
    # chart ever renames either, the CR would be rejected only at e2e time, and
    # this file is the cheapest place to notice.
    for key in imageFactoryURL registryMirrors; do
        yq -e ".talos | has(\"${key}\")" packages/apps/kubernetes/values.yaml >/dev/null \
            || { echo "packages/apps/kubernetes/values.yaml has no talos.${key}; talos_spec_block would emit a key the chart rejects" >&2; exit 1; }
    done
}
