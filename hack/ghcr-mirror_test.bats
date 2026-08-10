#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the in-sandbox ghcr.io pull-through mirror (cozystack/cozystack#3513).
#
# hack/e2e-ghcr-mirror.yaml bundles Service + Deployment + CiliumClusterwideNetworkPolicy
# but they apply in two phases, exactly like the talos-image-cache manifest:
# hack/e2e-install-cozystack.bats applies everything EXCEPT the Cilium policy (its CRD
# does not exist before Cozystack is installed), and hack/e2e-chainsaw/_lib/ghcr-mirror.sh
# applies ONLY that policy later once Cilium is up. These tests pin that split, the
# egress selector/target identities the allow depends on, the pull-through config, and
# the one pure string fragment (ghcr_registry_mirrors_block) whose indentation decides
# whether tenant CRs render at all.
#
# They also cover resolve_ghcr_mirror_endpoint, which is where the helper's safety
# property actually lives: it decides whether tenant workers are pointed at the mirror
# at all, and it caches that decision for every later suite in the shared sandbox. Its
# kubectl calls are stubbed below, so all four outcomes are reachable without a cluster.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own line;
# there is no bats `run`/`$status`, and setup()/teardown() are not honored. Sourcing
# ghcr-mirror.sh has no cluster side effects (it only sets defaults and defines
# functions). mikefarah yq prints `---` between matched documents.
#
# Run with: hack/cozytest.sh hack/ghcr-mirror_test.bats
# -----------------------------------------------------------------------------

# kubectl stub for the resolve_ghcr_mirror_endpoint cases. Each test sets the three
# globals below to pick a branch; every path returns explicitly, because cozytest.sh
# rewrites a bare `}` at column 0 into `return 0; }` and would otherwise force this
# stub to succeed no matter what the test asked for.
stub_get_rc=0
stub_get_out=""
stub_rollout_rc=0
stub_apply_rc=0
stub_log_lines=""
stub_avail="False"
stub_avail_rc=0
stub_log_rc=0
stub_apply_err=""

kubectl() {
    case "$*" in
        *conditions*)
            [ -z "${stub_avail}" ] || printf '%s' "${stub_avail}"
            return "${stub_avail_rc}"
            ;;
        *"get deploy ghcr-mirror"*)
            [ -z "${stub_get_out}" ] || printf '%s\n' "${stub_get_out}"
            return "${stub_get_rc}"
            ;;
        *"rollout status"*)
            return "${stub_rollout_rc}"
            ;;
        *apply*)
            cat >/dev/null
            # To stderr, as the real kubectl reports errors: the code under test
            # captures the reason via the braced group's 2>&1, and a stub that
            # printed it to stdout would have it eaten by kubectl's >/dev/null.
            [ -z "${stub_apply_err}" ] || printf '%s\n' "${stub_apply_err}" >&2
            return "${stub_apply_rc}"
            ;;
        *logs*)
            [ "${stub_log_rc}" -eq 0 ] || return "${stub_log_rc}"
            # Honour --tail=N, so a test can tell a read that filters the whole log
            # from one that truncates it. Note kubectl's own default with a label
            # selector is --tail=10, not "everything".
            _n=10
            for _a in "$@"; do
                case "${_a}" in --tail=*) _n=${_a#--tail=} ;; esac
            done
            if [ "${_n}" -ge 0 ] 2>/dev/null; then
                printf '%s\n' "${stub_log_lines}" | tail -n "${_n}"
            else
                printf '%s\n' "${stub_log_lines}"
            fi
            return 0
            ;;
    esac
    return 0
}

# ghcr_mirror_diagnose bounds every read with `timeout -k <grace> <n>`. Real
# timeout would exec past the kubectl shell-function stub above -- a process
# cannot exec a shell function -- so the diagnose tests would talk to a real
# kubectl or to nothing. Model timeout in-process: reject anything not spelled
# `-k <grace> <n>` with a status no kubectl returns, then drop those three
# tokens and run the command as the shell sees it, so the stub stays reachable.
timeout() {
    [ "${1:-}" = -k ] || return 97
    shift 3
    "$@"
}

@test "manifest documents partition into pre-Cilium apply plus the Cilium policy" {
    manifest=hack/e2e-ghcr-mirror.yaml
    total=$(yq '.kind' "$manifest" | grep -vc '^---$')
    excluded=$(yq 'select(.kind != "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -vc '^---$')
    selected=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -vc '^---$')
    [ "$total" -eq 3 ]
    [ "$excluded" -eq 2 ]
    [ "$selected" -eq 1 ]
    [ $((excluded + selected)) -eq "$total" ]
}

@test "pre-Cilium apply keeps Service and Deployment and drops the Cilium policy" {
    manifest=hack/e2e-ghcr-mirror.yaml
    kinds=$(yq 'select(.kind != "CiliumClusterwideNetworkPolicy") | .kind' "$manifest" | grep -v '^---$')
    for want in Service Deployment; do
        printf '%s\n' "$kinds" | grep -qx "$want" || { echo "pre-Cilium apply is missing $want" >&2; exit 1; }
    done
    if printf '%s\n' "$kinds" | grep -qx CiliumClusterwideNetworkPolicy; then
        echo "CiliumClusterwideNetworkPolicy leaked into the pre-Cilium apply" >&2
        exit 1
    fi
}

@test "registry is configured as a pull-through cache for ghcr.io" {
    manifest=hack/e2e-ghcr-mirror.yaml
    remote=$(yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "REGISTRY_PROXY_REMOTEURL") | .value' "$manifest")
    [ "$remote" = "https://ghcr.io" ]
}

@test "egress policy selects the worker VM (virt-launcher) identity that pulls the kubelet image" {
    manifest=hack/e2e-ghcr-mirror.yaml
    label=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.endpointSelector.matchLabels["k8s:kubevirt.io"]' "$manifest")
    ns=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.endpointSelector.matchLabels["k8s:io.kubernetes.pod.namespace"]' "$manifest")
    [ "$label" = "virt-launcher" ]
    [ "$ns" = "tenant-test" ]
}

@test "egress rule targets the mirror pod's own identity" {
    manifest=hack/e2e-ghcr-mirror.yaml
    target=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.egress[0].toEndpoints[0].matchLabels["k8s:app.kubernetes.io/name"]' "$manifest")
    target_ns=$(yq 'select(.kind == "CiliumClusterwideNetworkPolicy") | .spec.egress[0].toEndpoints[0].matchLabels["k8s:io.kubernetes.pod.namespace"]' "$manifest")
    mirror=$(yq 'select(.kind == "Deployment") | .spec.template.metadata.labels["app.kubernetes.io/name"]' "$manifest")
    mirror_ns=$(yq 'select(.kind == "Deployment") | .metadata.namespace' "$manifest")
    [ "$target" = "ghcr-mirror" ]
    [ "$target_ns" = "kube-system" ]
    # The allow's destination must equal the mirror Deployment's own pod label and
    # namespace, otherwise the hole is punched toward nothing.
    [ "$target" = "$mirror" ]
    [ "$target_ns" = "$mirror_ns" ]
}

@test "registryMirrors block is empty when no mirror endpoint is chosen" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_registry_mirrors_block "")
    [ -z "$out" ]
}

@test "registryMirrors block renders ghcr.io endpoints indented under spec.talos" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_registry_mirrors_block "http://ghcr-mirror.kube-system.svc")
    # 4-space indent so it nests under `  talos:`; ghcr.io host with the endpoint.
    printf '%s\n' "$out" | grep -qx '    registryMirrors:' || { echo "registryMirrors key not at spec.talos indent" >&2; exit 1; }
    printf '%s\n' "$out" | grep -qx '      ghcr.io:' || { echo "ghcr.io host key missing/misindented" >&2; exit 1; }
    printf '%s\n' "$out" | grep -qx '          - http://ghcr-mirror.kube-system.svc' || { echo "endpoint missing/misindented" >&2; exit 1; }
}

@test "registry Deployment runs PSS-restricted (non-root, no priv-esc, dropped caps, RO rootfs, no SA token)" {
    manifest=hack/e2e-ghcr-mirror.yaml
    d='select(.kind == "Deployment")'
    [ "$(yq "$d | .spec.template.spec.automountServiceAccountToken" "$manifest")" = "false" ]
    [ "$(yq "$d | .spec.template.spec.securityContext.runAsNonRoot" "$manifest")" = "true" ]
    [ "$(yq "$d | .spec.template.spec.securityContext.seccompProfile.type" "$manifest")" = "RuntimeDefault" ]
    c='.spec.template.spec.containers[0].securityContext'
    [ "$(yq "$d | $c.allowPrivilegeEscalation" "$manifest")" = "false" ]
    [ "$(yq "$d | $c.readOnlyRootFilesystem" "$manifest")" = "true" ]
    [ "$(yq "$d | $c.capabilities.drop[0]" "$manifest")" = "ALL" ]
}

@test "a NotFound mirror Deployment falls back to direct pulls and caches that" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=1
    stub_get_out='Error from server (NotFound): deployments.apps "ghcr-mirror" not found'
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint when the Deployment does not exist, got [$out]" >&2; rm -rf "$work"; exit 1; }
    # A NotFound is a stable answer: the mirror was never applied in this sandbox, so
    # every later suite may reuse it instead of re-asking.
    [ -f "$work/decision" ] || { echo "expected a NotFound decision to be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a transient query failure falls back without caching" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=1
    stub_get_out='Error from server: etcdserver: request timed out'
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint after a failed query, got [$out]" >&2; rm -rf "$work"; exit 1; }
    # Caching this would let one apiserver blip disable the mirror for every later
    # suite in the shared sandbox, which is the failure the helper exists to avoid.
    [ ! -f "$work/decision" ] || { echo "a transient query failure must not be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a missing kubectl binary is not mistaken for an absent Deployment" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=127
    stub_get_out='sh: 1: kubectl: not found'
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint when kubectl could not run, got [$out]" >&2; rm -rf "$work"; exit 1; }
    # Only the API's own NotFound means "the mirror was never applied here". A
    # shell reporting a missing binary, or a bad context, also says "not found"
    # and would otherwise be cached as that stable answer for the whole sandbox.
    [ ! -f "$work/decision" ] || { echo "a failure that is not an API NotFound must not be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a failed egress allow falls back without caching" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=0
    stub_apply_rc=1
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint when the egress allow did not apply, got [$out]" >&2; rm -rf "$work"; exit 1; }
    # Same class as the query blip above: a failed `kubectl apply` says nothing about
    # whether the next attempt would succeed, so it must not become the sandbox-wide
    # answer. This is the branch that used to cache and the query branch did not.
    [ ! -f "$work/decision" ] || { echo "a failed egress allow must not be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a ready mirror with its egress allow applied commits the endpoint and caches it" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=0
    stub_apply_rc=0
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ "$out" = "$GHCR_MIRROR_SVC_URL" ] || { echo "expected the mirror endpoint, got [$out]" >&2; rm -rf "$work"; exit 1; }
    [ -f "$work/decision" ] || { echo "expected the committed endpoint to be cached" >&2; rm -rf "$work"; exit 1; }
    [ "$(cat "$work/decision")" = "$GHCR_MIRROR_SVC_URL" ] || { echo "cached decision does not match the returned endpoint" >&2; rm -rf "$work"; exit 1; }
    # The cache is what later suites read, so it has to short-circuit the kubectl path
    # entirely -- that is the whole reason only the first tenant test pays the wait.
    stub_get_rc=1
    stub_get_out='Error from server (NotFound): deployments.apps "ghcr-mirror" not found'
    again=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ "$again" = "$GHCR_MIRROR_SVC_URL" ] || { echo "a cached decision must be returned without re-querying, got [$again]" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "the diagnostic finds a kubelet pull buried behind the readiness probe's own log" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # The shape the real log has at diagnose time: the worker's kubelet pull happens
    # early, then the readinessProbe writes a /v2/ line every 5s for the rest of the
    # run. After the 18m node-join budget that is 200+ probe lines stacked on top of
    # the one request worth finding, so a fixed tail window never reaches it.
    kubelet_line='10.244.1.92 - - [09/Aug/2026:01:00:00 +0000] "GET /v2/siderolabs/kubelet/manifests/v1.35.6 HTTP/1.1" 200 683'
    probe_lines=$(i=0; while [ "$i" -lt 300 ]; do echo '10.244.0.1 - - [09/Aug/2026:01:18:00 +0000] "GET /v2/ HTTP/1.1" 200 2'; i=$((i+1)); done)
    stub_log_lines=$(printf '%s\n%s' "$kubelet_line" "$probe_lines")
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -q 'the mirror served 1 kubelet-image request' || {
        echo "diagnose did not find the kubelet pull behind the probe log; it reported:" >&2
        printf '%s\n' "$out" | grep -i 'kubelet-image' >&2
        exit 1
    }
}

@test "the diagnostic reports honestly when no kubelet pull reached the mirror" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # Probe traffic only: the mirror was up and nothing pulled through it. This must
    # not be reported the same way as the case above, which is the whole point.
    stub_log_lines=$(i=0; while [ "$i" -lt 50 ]; do echo '10.244.0.1 - - [09/Aug/2026:01:18:00 +0000] "GET /v2/ HTTP/1.1" 200 2'; i=$((i+1)); done)
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -q 'no kubelet-image request reached the mirror' || {
        echo "diagnose did not report the empty case distinctly" >&2
        printf '%s\n' "$out" | grep -i 'kubelet-image' >&2
        exit 1
    }
}

@test "a rollout watch that failed for another reason is not cached as unavailability" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=1
    # `kubectl rollout status` exits non-zero on a broken or aborted watch as well
    # as on elapsing its timeout, and its output is discarded, so the call site
    # cannot tell the two apart. Here the follow-up read cannot answer either.
    stub_avail=""
    stub_avail_rc=1
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint when readiness could not be confirmed, got [$out]" >&2; rm -rf "$work"; exit 1; }
    [ ! -f "$work/decision" ] || { echo "an unconfirmed rollout failure must not be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a broken rollout watch over a Deployment the API calls Available still commits the mirror" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=1
    stub_apply_rc=0
    # The watch broke, but the API says the Deployment is up. Falling back here
    # would discard a mirror that is serving, on the strength of a failed watch.
    stub_avail="True"
    stub_avail_rc=0
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ "$out" = "$GHCR_MIRROR_SVC_URL" ] || { echo "expected the mirror endpoint when the API reports Available=True, got [$out]" >&2; rm -rf "$work"; exit 1; }
    [ -f "$work/decision" ] || { echo "expected the committed endpoint to be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a Deployment the API reports Available=False is cached as unavailable" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=1
    # A definite False is the settled answer: nothing in the run re-applies the
    # manifest, so this one may be cached for the rest of the sandbox.
    stub_avail="False"
    stub_avail_rc=0
    out=$(resolve_ghcr_mirror_endpoint 2>/dev/null)
    [ -z "$out" ] || { echo "expected no endpoint for an unavailable mirror, got [$out]" >&2; rm -rf "$work"; exit 1; }
    [ -f "$work/decision" ] || { echo "a definite Available=False should be cached" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a log the diagnostic cannot read is not reported as zero kubelet pulls" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    stub_log_rc=1
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -q 'could not read the mirror' || {
        echo "an unreadable log must not be reported as a finding about the workers" >&2
        printf '%s\n' "$out" | grep -iE 'kubelet-image|could not read' >&2
        exit 1
    }
    printf '%s' "$out" | grep -q 'no kubelet-image request reached the mirror' && {
        echo "an unreadable log was reported as zero pulls" >&2
        exit 1
    }
    stub_log_rc=0
}

@test "a failed egress allow reports why it failed" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    _GHCR_MIRROR_DECISION_FILE="$work/decision"
    stub_get_rc=0
    stub_rollout_rc=0
    stub_apply_rc=1
    stub_apply_err='error: unable to recognize "STDIN": no matches for kind "CiliumClusterwideNetworkPolicy"'
    # xtrace off from here on: cozytest runs test bodies under set -x, and the
    # helper's own `2>&1` then captures trace lines along with real stderr --
    # the trace of the stub's printf carries 'no matches for kind' whether or not
    # the helper delivered it, and the trace of yq pushes the real reason off the
    # warning's line. Both fake the assertion, in opposite directions. Not turned
    # back on: xtrace is the runner's artifact, nothing after this needs it, and
    # under plain bats a `set -x` here ENABLES tracing that was never on -- a
    # test that then fails hangs the bats runner instead of reporting.
    set +x
    err=$(resolve_ghcr_mirror_endpoint 2>&1 >/dev/null)
    # A permanent cause -- an unserved apiVersion, an RBAC denial, a missing yq --
    # must reach the log rather than be summarised as a blip. Retrying is what the
    # code does; asserting the cause is not something it can know. Anchored to the
    # warning's own prefix so only the helper's real line -- 'Reason:' and the
    # cause together -- satisfies it.
    printf '%s' "$err" | grep -q 'Reason: .*no matches for kind' || {
        echo "the apply failure reason was discarded" >&2
        printf '%s\n' "$err" >&2
        rm -rf "$work"; exit 1
    }
    stub_apply_err=""
    stub_apply_rc=0
    rm -rf "$work"
}

@test "the install-time apply is not gated behind the Talos image cache's precondition" {
    f=hack/e2e-install-cozystack.bats
    # The Talos image cache's install step reads talos.schematicID/version out of
    # packages/apps/kubernetes/values.yaml and returns early when either is absent,
    # because its manifest carries placeholders for them. This manifest carries no
    # placeholders and needs neither value, so sharing that @test block would let a
    # values.yaml restructure silently stop deploying the mirror while the log
    # blamed the Talos factory. Each mirror gets its own block.
    ghcr=$(awk '/^@test /{n=$0} /e2e-ghcr-mirror\.yaml/ && n {print n}' "$f" | head -1)
    talos=$(awk '/^@test /{n=$0} /e2e-talos-image-cache\.yaml/ && n {print n}' "$f" | head -1)
    [ -n "$ghcr" ] || { echo "no @test in $f applies hack/e2e-ghcr-mirror.yaml" >&2; exit 1; }
    [ -n "$talos" ] || { echo "no @test in $f applies hack/e2e-talos-image-cache.yaml" >&2; exit 1; }
    [ "$ghcr" != "$talos" ] || {
        echo "both mirrors are applied from one @test block, so its early return skips this one too:" >&2
        echo "  $ghcr" >&2
        exit 1
    }
}

@test "the install-time apply excludes the Cilium policy" {
    f=hack/e2e-install-cozystack.bats
    # Tests above pin what the yq exclusion produces from the manifest; this pins
    # that the install step actually runs it. Without the filter the whole-file
    # apply still converges -- the policy apply fails on the absent CRD, the || arm
    # swallows it, and point-of-use applies the policy later anyway -- but the step
    # then logs a scary unrelated error and its WARNING line misreports what
    # happened, in the subsystem whose legibility is the point.
    block=$(awk '/^@test /{b=""} {b=b ORS $0} /^}$/ && b ~ /e2e-ghcr-mirror\.yaml/ {print b; exit}' "$f")
    [ -n "$block" ] || { echo "no @test block in $f applies hack/e2e-ghcr-mirror.yaml" >&2; exit 1; }
    printf '%s\n' "$block" | grep -qF "select(.kind != \"CiliumClusterwideNetworkPolicy\")" || {
        echo "the install-time apply no longer excludes the CiliumClusterwideNetworkPolicy" >&2
        exit 1
    }
}

@test "the manifest and helper agree on the mirror Service DNS name" {
    manifest=hack/e2e-ghcr-mirror.yaml
    helper=hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    svc=$(yq 'select(.kind == "Service") | .metadata.name' "$manifest")
    svc_ns=$(yq 'select(.kind == "Service") | .metadata.namespace' "$manifest")
    # The helper's default endpoint must resolve to the Service this manifest ships,
    # else workers are mirrored at a name that does not exist and fall back silently.
    grep -q "GHCR_MIRROR_SVC_URL:-http://${svc}.${svc_ns}.svc" "$helper" \
        || { echo "helper endpoint drifted from the mirror Service name" >&2; exit 1; }
}
