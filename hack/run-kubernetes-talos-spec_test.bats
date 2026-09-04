#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for talos_spec_block in hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# It is the composition point for the tenant Kubernetes CR's `spec.talos`: the
# Talos OS image cache contributes imageFactoryURL when available. The result is
# spliced into an unquoted heredoc directly under `spec:`, so an indentation slip
# does not fail this function -- it produces a CR the API rejects, and every
# kubernetes-* suite dies at tenant creation with an error that points at the CR
# rather than at this line.
#
# The resolver is replaced with a stub after sourcing, which reduces this to the
# pure composition it is: the resolver has its own coverage in
# hack/talos-image-cache_test.bats and needs a cluster to mean anything.
#
# The assertion goes through yq on a spliced document rather than through grep
# on the string, so what it pins is "the CR parses and the key lands under
# spec.talos", which is the actual contract. A string match would keep passing
# on a block indented into the wrong parent.
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

@test "no Talos image cache renders nothing, so the chart default applies" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_talos_image_factory_url() { printf ''; }
    out=$(talos_spec_block)
    [ -z "$out" ] || { echo "expected no spec.talos block when the image cache is down, got [$out]" >&2; exit 1; }
}

@test "the Talos image cache renders imageFactoryURL and no registryMirrors" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    resolve_talos_image_factory_url() { printf 'http://talos-image-cache.kube-system.svc'; }
    work=$(mktemp -d)
    spec_doc "$(talos_spec_block)" > "$work/cr.yaml"
    got=$(yq '.spec.talos.imageFactoryURL' "$work/cr.yaml")
    [ "$got" = "http://talos-image-cache.kube-system.svc" ] || { echo "imageFactoryURL did not land under spec.talos, got [$got]" >&2; cat "$work/cr.yaml" >&2; rm -rf "$work"; exit 1; }
    mirrors=$(yq '.spec.talos.registryMirrors' "$work/cr.yaml")
    [ "$mirrors" = "null" ] || { echo "e2e must not inject registryMirrors, got [$mirrors]" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "the key the sandbox keys off is spelled the way the chart declares it" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    # talos_spec_block writes imageFactoryURL by hand. If the chart ever renames
    # it, the CR would be rejected only at e2e time, and this file is the cheapest
    # place to notice.
    yq -e '.talos | has("imageFactoryURL")' packages/apps/kubernetes/values.yaml >/dev/null \
        || { echo "packages/apps/kubernetes/values.yaml has no talos.imageFactoryURL; talos_spec_block would emit a key the chart rejects" >&2; exit 1; }
}
