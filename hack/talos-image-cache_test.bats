#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the in-sandbox Talos image cache manifest split and the
# importer-reachability contract it relies on.
#
# hack/e2e-talos-image-cache.yaml bundles four documents but they are applied in
# two phases: hack/e2e-install-cozystack.bats applies everything EXCEPT the
# CiliumClusterwideNetworkPolicy (its CRD does not exist before Cozystack is
# installed), and hack/e2e-chainsaw/_lib/talos-image-cache.sh applies ONLY that
# policy later, once Cilium is up. If a future document is added to the manifest
# and silently dropped from the pre-Cilium apply, or the Cilium document leaks
# into it (and errors on the missing CRD), the mirror breaks and e2e falls back
# to the flaky public factory. These tests pin that split.
#
# They also pin the load-bearing reachability invariant: the throwaway probe pod
# is a faithful proxy for a real CDI importer only if it carries the exact label
# and namespace the egress policy selects. If either side drifts, the probe can
# pass while real importers stay blocked (a false positive that makes CI worse).
#
# Finally they cover the two pure fragments of the decision itself, sourced from
# hack/e2e-chainsaw/_lib/talos-image-cache.sh (sourcing it has no cluster side
# effects — it only sets parameter defaults and defines functions): the strict
# 206 gate that decides whether tenants are pointed at the mirror at all, and the
# --overrides JSON builder whose silent breakage would drop every probe to the
# public factory. The kubectl orchestration around them needs a live cluster and
# is covered by the e2e run, matching how the other hack/ helpers are tested.
#
# Root cause + fix are from cozystack/cozystack#3254 (@lexfrei); this is the port
# to the Chainsaw layout (hack/e2e-chainsaw/_lib/).
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`, and setup()/teardown() are not
# honored. Each test runs under `set -eu -x`; assertions are direct shell tests
# that exit non-zero on failure. mikefarah yq prints `---` between matched
# documents, so document streams are compared with those separators stripped.
#
# Run with: hack/cozytest.sh hack/talos-image-cache_test.bats
# -----------------------------------------------------------------------------

@test "manifest documents partition into pre-Cilium apply plus the Cilium policy" {
    manifest=hack/e2e-talos-image-cache.yaml
    total=$(yq '.kind' "$manifest" | grep -vc '^---$')
    excluded=$(yq 'select(.kind != "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -vc '^---$')
    selected=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -vc '^---$')
    [ "$total" -eq 4 ]
    [ "$excluded" -eq 3 ]
    [ "$selected" -eq 1 ]
    [ $((excluded + selected)) -eq "$total" ]
}

@test "pre-Cilium apply keeps Service, Deployment, ConfigMap and drops the Cilium policy" {
    manifest=hack/e2e-talos-image-cache.yaml
    kinds=$(yq 'select(.kind != "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -v '^---$')
    for want in Service Deployment ConfigMap; do
        printf '%s\n' "$kinds" | grep -qx "$want" || { echo "pre-Cilium apply is missing $want" >&2; exit 1; }
    done
    if printf '%s\n' "$kinds" | grep -qx CiliumClusterwideNetworkPolicy; then
        echo "CiliumClusterwideNetworkPolicy leaked into the pre-Cilium apply" >&2
        exit 1
    fi
}

@test "point-of-use apply selects exactly the Cilium policy" {
    manifest=hack/e2e-talos-image-cache.yaml
    kinds=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -v '^---$')
    [ "$kinds" = "CiliumClusterwideNetworkPolicy" ]
}

@test "egress policy selects the importer label and namespace the probe pod uses" {
    manifest=hack/e2e-talos-image-cache.yaml
    helper=hack/e2e-chainsaw/_lib/talos-image-cache.sh
    label=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.endpointSelector.matchLabels["k8s:cdi.kubevirt.io"]' "$manifest")
    ns=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.endpointSelector.matchLabels["k8s:io.kubernetes.pod.namespace"]' "$manifest")
    [ "$label" = "importer" ]
    [ "$ns" = "tenant-test" ]
    # The probe pod must carry the same label the policy selects, else it faces a
    # different egress than a real importer and the guarantee breaks.
    grep -q "cdi.kubevirt.io=importer" "$helper" || { echo "probe pod label drifted from the egress selector" >&2; exit 1; }
    grep -q "TALOS_IMAGE_CACHE_PROBE_NS:-tenant-test" "$helper" || { echo "probe namespace drifted from the egress selector" >&2; exit 1; }
}

@test "egress rule targets the mirror pod's own identity" {
    manifest=hack/e2e-talos-image-cache.yaml
    target=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.egress[0].toEndpoints[0].matchLabels["k8s:app.kubernetes.io/name"]' "$manifest")
    target_ns=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.egress[0].toEndpoints[0].matchLabels["k8s:io.kubernetes.pod.namespace"]' "$manifest")
    mirror=$(yq 'select(.kind == "Deployment") | .spec.template.metadata.labels["app.kubernetes.io/name"]' "$manifest")
    mirror_ns=$(yq 'select(.kind == "Deployment") | .metadata.namespace' "$manifest")
    [ "$target" = "talos-image-cache" ]
    [ "$target_ns" = "kube-system" ]
    # The allow's destination must equal the mirror Deployment's own pod label and
    # namespace, otherwise the hole is punched toward nothing: moving the mirror
    # would leave these tests green while importers could no longer reach it.
    [ "$target" = "$mirror" ]
    [ "$target_ns" = "$mirror_ns" ]
}

@test "strict 206 gate selects the mirror on a byte-range success" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    if ! _talos_image_cache_probe_succeeded "code=206"; then
        echo "expected a 206 probe result to select the mirror" >&2
        exit 1
    fi
}

@test "strict 206 gate finds the marker inside surrounding probe output" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    out=$(printf 'pod/talos-image-cache-probe created\ncode=206\npod "talos-image-cache-probe" deleted\n')
    if ! _talos_image_cache_probe_succeeded "$out"; then
        echo "expected 206 to be found in multi-line attach output" >&2
        exit 1
    fi
}

@test "strict 206 gate falls back on range-less, unreachable, empty and unrelated output" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    # A plain 200 means the mirror answered without range support; 000 means curl
    # never connected. Both fall back to the public factory, as does output that
    # carried no result marker at all (an attach stream lost with no logs to recover).
    for bad in "code=200" "code=000" "code=404" "" "error: pods not found"; do
        if _talos_image_cache_probe_succeeded "$bad"; then
            echo "expected non-206 output to fall back to the public factory: [$bad]" >&2
            exit 1
        fi
    done
}

@test "probe overrides builder emits valid JSON with the image, command and restricted PSA" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    img=example.invalid/img:tag
    cmd='curl -s -o /dev/null -w code=%{http_code} -r 0-0 http://talos-image-cache.kube-system.svc/x.raw.xz'
    json=$(_talos_image_cache_probe_overrides "$img" "$cmd")
    # Invalid JSON would make every probe fail and silently drop CI back to the
    # public factory, so parse it rather than pattern-match it.
    printf '%s' "$json" | yq -p=json -o=json '.' >/dev/null
    [ "$(printf '%s' "$json" | yq -p=json '.spec.containers[0].name')" = "c" ]
    [ "$(printf '%s' "$json" | yq -p=json '.spec.containers[0].image')" = "$img" ]
    [ "$(printf '%s' "$json" | yq -p=json '.spec.containers[0].command[2]')" = "$cmd" ]
    [ "$(printf '%s' "$json" | yq -p=json '.spec.securityContext.runAsNonRoot')" = "true" ]
    [ "$(printf '%s' "$json" | yq -p=json '.spec.containers[0].securityContext.allowPrivilegeEscalation')" = "false" ]
}
