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
# Finally they cover the decision itself, sourced from
# hack/e2e-chainsaw/_lib/talos-image-cache.sh (sourcing it has no cluster side
# effects — it only sets parameter defaults and defines functions). Two pure
# fragments are exercised directly: the strict 206 gate that decides whether
# tenants are pointed at the mirror at all, and the --overrides JSON builder
# whose silent breakage would drop every probe to the public factory. The
# kubectl orchestration around them is driven here too, against a throwaway
# `kubectl` and `timeout` placed on PATH — the decision of which factory a whole
# suite pulls from is worth pinning without waiting for a cluster.
#
# On PATH, not shell functions, and that is not a style choice: the lookup runs
# `timeout -k 5 40 kubectl …` wherever timeout is installed, and an external
# timeout binary execs a real file.
# A `kubectl() { … }` override is invisible to it, so the stub would never be
# reached, and a suite that stubs that way goes quietly vacuous. A stub on PATH
# is also the only way to assert how the real call was spelled, which matters
# for the flags the three-way answer depends on: the stub answers exit 0 with no
# output for an absence no matter what flags it is handed, while a real kubectl
# does that only with --ignore-not-found, so that flag is pinned by inspecting
# the recorded call rather than by the behaviour around it.
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

_stub_kubectl_dir() {
    # Build a throwaway PATH entry whose `kubectl get deploy ... -o name` fails
    # its first $2 invocations and then answers with $3 on stdout (empty for a
    # genuine absence). $1 is the scratch dir. printf, not a heredoc: cozytest.sh
    # translates the file with awk and would read a @test or a bare `}` inside a
    # heredoc as real syntax.
    mkdir -p "$1/bin"
    printf '%s\n' \
        '#!/bin/sh' \
        'printf "%s\n" "$*" >> "$STUB_CALLS"' \
        'n=$(cat "$STUB_COUNTER")' \
        'n=$((n + 1))' \
        'printf "%s" "$n" > "$STUB_COUNTER"' \
        'if [ "$n" -le "$STUB_FAILS" ]; then' \
        '  echo "error: the server was unable to return a response" >&2' \
        '  exit 1' \
        'fi' \
        'printf "%s" "$STUB_OUT"' \
        > "$1/bin/kubectl"
    chmod +x "$1/bin/kubectl"
    # A transparent `timeout` that records how it was invoked and then runs the
    # command anyway, so a test can pin the wrapper without changing what the
    # wrapped call does. Same idiom as hack/run-kubernetes-serial-console_test.bats.
    # With STUB_TIMEOUT_RC set it instead exits that status without running the
    # command and without printing, which is what a real kill looks like from the
    # caller's side: the wrapper says nothing and the command never reaches the
    # point where it would have.
    printf '%s\n' \
        '#!/bin/sh' \
        'printf "%s\n" "$*" >> "$STUB_TIMEOUT_CALLS"' \
        'if [ -n "$STUB_TIMEOUT_RC" ]; then exit "$STUB_TIMEOUT_RC"; fi' \
        'while [ $# -gt 0 ] && [ "$1" != kubectl ]; do shift; done' \
        'exec "$@"' \
        > "$1/bin/timeout"
    chmod +x "$1/bin/timeout"
    printf '0' > "$1/counter"
    : > "$1/calls"
    : > "$1/timeout.calls"
    export STUB_COUNTER="$1/counter" STUB_CALLS="$1/calls" \
        STUB_TIMEOUT_CALLS="$1/timeout.calls" STUB_FAILS="$2" STUB_OUT="$3" \
        STUB_TIMEOUT_RC=""
    PATH="$1/bin:$PATH"
}

@test "deployment lookup reports present when the mirror answers with a name" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 0 'deployment.apps/talos-image-cache'
    state=$(_talos_image_cache_deploy_state)
    rm -rf "$d"
    [ "$state" = present ]
}

@test "deployment lookup reports absent only on an empty answer the apiserver actually gave" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    # --ignore-not-found: a real absence is exit 0 with no output. This is the
    # one outcome allowed to turn the whole suite toward the public factory.
    _stub_kubectl_dir "$d" 0 ''
    state=$(_talos_image_cache_deploy_state)
    # The flag has to be pinned by inspection, because the stub gives exit 0 and
    # no output whether or not the call asks for it. Drop it from the real call
    # and kubectl reports a genuine absence as an error instead: `absent` becomes
    # a state production can never reach, while this test keeps passing.
    flagged=$(grep -c -- '--ignore-not-found' "$d/calls" || true)
    rm -rf "$d"
    [ "$state" = absent ]
    [ "$flagged" = 1 ]
}

@test "deployment lookup reports unknown when the apiserver never answers" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    # Every attempt refused. Reading this as "absent" is the defect: a blip would
    # send every tenant worker in the suite to the public factory.
    _stub_kubectl_dir "$d" 99 'deployment.apps/talos-image-cache'
    state=$(_talos_image_cache_deploy_state)
    rm -rf "$d"
    [ "$state" = unknown ]
}

@test "deployment lookup rides out a blip and reports what a later attempt found" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 1 'deployment.apps/talos-image-cache'
    state=$(_talos_image_cache_deploy_state)
    tries=$(cat "$d/counter")
    rm -rf "$d"
    [ "$state" = present ]
    [ "$tries" = 2 ]
}

@test "every attempt at the lookup carries a request deadline" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 99 'deployment.apps/talos-image-cache'
    state=$(_talos_image_cache_deploy_state)
    # kubectl's --request-timeout defaults to 0, so a connection that
    # establishes and then stalls hangs with no deadline, and re-asking would
    # multiply that hang instead of riding out a blip. The flag alone is not a
    # wall-clock bound either — kubectl retries discovery several times over
    # before it gives up — so each attempt needs the wrapper as well. The last
    # count pins the number of attempts, not their duration.
    bounded=$(grep -c -- '--request-timeout=30s' "$d/calls" || true)
    # The wall-clock bound is deliberately above the request deadline beside it:
    # level with it, the kill lands first every time and --request-timeout can
    # never fire, which is how the one silent failure mode stays silent.
    wrapped=$(grep -c '^-k 5 40 kubectl ' "$d/timeout.calls" || true)
    total=$(grep -c . "$d/calls" || true)
    rm -rf "$d"
    [ "$state" = unknown ]
    [ "$total" = 3 ]
    [ "$bounded" = 3 ]
    [ "$wrapped" = 3 ]
}

@test "a present mirror routes the decision into the reachability probe" {
    d=$(mktemp -d)
    _TALOS_IMAGE_FACTORY_DECISION_FILE="$d/decision"
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 0 'deployment.apps/talos-image-cache'
    set +x
    url=$(resolve_talos_image_factory_url 2>"$d/err")
    err=$(cat "$d/err")
    rm -rf "$d"
    # The state token is a contract between this helper and its two readers.
    # Drift on the resolve side is the worst case there is: the present branch
    # would fall through to "not deployed", cache an empty URL, and send every
    # tenant worker in the suite to the public factory — the very harm the
    # three-way answer exists to prevent, with every other test still green.
    [ -z "$url" ]
    printf '%s' "$err" | grep -q 'Available but a tenant-scoped probe' || { echo "a present mirror must reach the probe, not the not-deployed branch: [$err]" >&2; exit 1; }
}

@test "an undecidable lookup warns and is not cached for the rest of the suite" {
    d=$(mktemp -d)
    _TALOS_IMAGE_FACTORY_DECISION_FILE="$d/decision"
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 99 'deployment.apps/talos-image-cache'
    # cozytest.sh runs each test under `set -x`, and xtrace shares the stderr
    # this captures. Off for the call, so the assertions below read what the
    # function said rather than an echo of the commands it ran, and left off
    # afterwards: a test that turns it back on and then fails wedges `bats`
    # instead of reporting, which costs a reader the failure they came for.
    set +x
    url=$(resolve_talos_image_factory_url 2>"$d/err")
    cached=no
    if [ -f "$d/decision" ]; then cached=yes; fi
    err=$(cat "$d/err")
    rm -rf "$d"
    # Falling back for this one caller is fine; pinning that fallback for every
    # later tenant test on the strength of a failed question is not.
    [ -z "$url" ]
    [ "$cached" = no ]
    printf '%s' "$err" | grep -q 'could not determine' || { echo "an undecidable lookup must say so: [$err]" >&2; exit 1; }
    # And it must say why. "could not determine" alone leaves an operator with a
    # new silent failure in place of the old one: refused, Unauthorized and
    # timed-out all read the same until kubectl's own message reaches the log.
    printf '%s' "$err" | grep -q 'the server was unable to return a response' || { echo "the reason the lookup failed must reach the log: [$err]" >&2; exit 1; }
}

@test "a lookup killed by the wall-clock bound still accounts for itself" {
    d=$(mktemp -d)
    _TALOS_IMAGE_FACTORY_DECISION_FILE="$d/decision"
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 0 'deployment.apps/talos-image-cache'
    # The stall this loop exists for, and the one case where nobody speaks:
    # `timeout` prints nothing when it fires, and the kubectl it killed never
    # reached the point where it would have printed either. The sibling test
    # below pins the reason on a kubectl that failed loudly, which every stub
    # does by default -- so without this case "the reason reaches the log" is
    # pinned only where it was never in doubt.
    export STUB_TIMEOUT_RC=124
    set +x
    url=$(resolve_talos_image_factory_url 2>"$d/err")
    err=$(cat "$d/err")
    spoke=$(grep -c . "$d/calls" || true)
    rm -rf "$d"
    [ -z "$url" ]
    # kubectl never ran, so anything the warning points at has to come from the
    # lookup itself. Drop this and the case degrades into the sibling below.
    [ "$spoke" = 0 ]
    printf '%s' "$err" | grep -q 'the last one exited 124' || { echo "a lookup cut off by its own bound must say so: [$err]" >&2; exit 1; }
}

@test "a genuine absence still resolves once and stays cached" {
    d=$(mktemp -d)
    _TALOS_IMAGE_FACTORY_DECISION_FILE="$d/decision"
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 0 ''
    set +x
    url=$(resolve_talos_image_factory_url 2>"$d/err")
    cached=no
    if [ -f "$d/decision" ]; then cached=yes; fi
    err=$(cat "$d/err")
    rm -rf "$d"
    [ -z "$url" ]
    [ "$cached" = yes ]
    printf '%s' "$err" | grep -q 'not deployed' || { echo "a real absence must report itself as such: [$err]" >&2; exit 1; }
}

@test "with timeout off PATH the lookup still asks instead of answering unknown" {
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    d=$(mktemp -d)
    _TALOS_IMAGE_CACHE_QUERY_DELAY=0
    _stub_kubectl_dir "$d" 0 'deployment.apps/talos-image-cache'
    rm -f "$d/bin/timeout"
    # Everything the kubectl stub itself needs, staged from known directories so
    # the stripped PATH below loses `timeout` and nothing else. Resolved this way
    # rather than through `command -v` for the reason the same idiom gives in
    # hack/run-kubernetes-node-join_test.bats: a name mocked as a shell function
    # answers with the function and no link gets made.
    for c in cat sleep; do
        for p in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
            if [ -x "$p/$c" ]; then ln -sf "$p/$c" "$d/bin/$c"; break; fi
        done
    done
    if [ ! -x "$d/bin/cat" ]; then
        echo "could not stage a real cat in the stripped PATH; the check below would be vacuous" >&2
        rm -rf "$d"
        exit 1
    fi
    # Stripped for the call and restored right after: the assertions below need a
    # `grep` this PATH deliberately does not have.
    oldpath=$PATH
    PATH="$d/bin"
    state=$(_talos_image_cache_deploy_state)
    PATH=$oldpath
    wrapped=$(grep -c . "$d/timeout.calls" || true)
    rm -rf "$d"
    # Calling the wrapper anyway when it is not installed makes every attempt
    # exit 127, and three of those read as an apiserver that never answered: a
    # missing local binary would pin the whole suite to the public factory while
    # every message blamed the cluster. Asserting the state rather than the exit
    # is what distinguishes the two — 127 lands on `unknown`, not on an error.
    [ "$state" = present ]
    [ "$wrapped" = 0 ]
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
