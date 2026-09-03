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
stub_image="registry:test"
stub_apply_capture=""
stub_job_log=""
stub_job_log_rc=0
stub_job_status=""

kubectl() {
    case "$*" in
        # Before the plain `get deploy` arm: the warm-up reads the mirror's image
        # with a jsonpath on that same command, and the arm below would answer it
        # with the human-readable table instead.
        *jsonpath*containers*)
            [ -z "${stub_image}" ] || printf '%s' "${stub_image}"
            return 0
            ;;
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
            # Capture what was applied when a test asks, so the happy path can be
            # asserted on the object rather than on the fact that apply was reached.
            if [ -n "${stub_apply_capture}" ]; then
                cat > "${stub_apply_capture}"
            else
                cat >/dev/null
            fi
            # To stderr, as the real kubectl reports errors: the code under test
            # captures the reason via the braced group's 2>&1, and a stub that
            # printed it to stdout would have it eaten by kubectl's >/dev/null.
            [ -z "${stub_apply_err}" ] || printf '%s\n' "${stub_apply_err}" >&2
            return "${stub_apply_rc}"
            ;;
        *"logs job/"*)
            # Distinct payload from the registry log: the access-log read at the end
            # of ghcr_mirror_diagnose prints stub_log_lines too, so a test asserting
            # the warm-up's result against that shared value passes with the Job read
            # deleted. Two mutations survived on exactly that.
            [ -z "${stub_job_log}" ] || printf '%s\n' "${stub_job_log}"
            return "${stub_job_log_rc}"
            ;;
        *"get job "*)
            [ -z "${stub_job_status}" ] || printf '%s\n' "${stub_job_status}"
            return 0
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
    # run. After the 29m node-join budget that is 200+ probe lines stacked on top of
    # the one request worth finding, so a fixed tail window never reaches it.
    kubelet_line='10.244.1.92 - - [09/Aug/2026:01:00:00 +0000] "GET /v2/siderolabs/kubelet/manifests/v1.35.6 HTTP/1.1" 200 683'
    probe_lines=$(i=0; while [ "$i" -lt 300 ]; do echo '10.244.0.1 - - [09/Aug/2026:01:18:00 +0000] "GET /v2/ HTTP/1.1" 200 2'; i=$((i+1)); done)
    stub_log_lines=$(printf '%s\n%s' "$kubelet_line" "$probe_lines")
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -q 'a worker pulled siderolabs/kubelet through the mirror' || {
        echo "diagnose did not find the kubelet pull behind the probe log; it reported:" >&2
        printf '%s\n' "$out" | grep -iE 'through the mirror|reached the mirror' >&2
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
    printf '%s' "$out" | grep -qF "no $GHCR_WARM_REPO request appears in the mirror" || {
        echo "diagnose did not report the empty case distinctly" >&2
        printf '%s\n' "$out" | grep -iE 'through the mirror|reached the mirror' >&2
        exit 1
    }
}

@test "a worker's manifest HEAD alone counts as reaching the mirror" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # containerd resolves a manifest with a HEAD before it GETs anything, and the
    # registry writes that HEAD as a combined access line of the same shape (measured
    # on the pinned image with a containerd user-agent). A pull that got as far as the
    # HEAD and died before the GET is exactly the run this diagnostic exists for, and
    # a GET-anchored count would report it as "no request appears" while the duration
    # section below prints that same request's timing -- the two halves of one output
    # contradicting each other.
    stub_log_lines='10.244.1.92 - - [11/Aug/2026:23:17:27 +0000] "HEAD /v2/siderolabs/kubelet/manifests/v1.35.6 HTTP/1.1" 200 683 "" "containerd/2.2.4+unknown"'
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_log_lines=""
    printf '%s' "$out" | grep -qF "a worker pulled $GHCR_WARM_REPO through the mirror (1 request(s)" || {
        echo "a manifest HEAD was not counted as a worker reaching the mirror" >&2
        printf '%s\n' "$out" | grep -iE 'through the mirror|appears in the mirror' >&2
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
        printf '%s\n' "$out" | grep -iE 'reached the mirror|could not read' >&2
        exit 1
    }
    printf '%s' "$out" | grep -qF "no $GHCR_WARM_REPO request appears in the mirror" && {
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

@test "the warm-up covers every kubelet tag the chart can select" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    # The tags come from the chart's own version map, not from a list kept here.
    # A copy would drift the moment a version is added: the suite would boot a
    # tenant on a tag nothing warmed, the first worker would pay the cold upstream
    # fetch again, and nothing would report that the warm-up had missed it.
    want=$(yq '.[]' packages/apps/kubernetes/files/versions.yaml | sort)
    got=$(ghcr_warm_tags | sort)
    [ "$got" = "$want" ] || {
        echo "warm-up tag list drifted from the chart version map" >&2
        echo "want: $want" >&2
        echo "got:  $got" >&2
        exit 1
    }
}

@test "a fully warm run exits zero" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # The failure half of the exit contract is pinned by five tests; this is the
    # missing success half. Every other test that runs the emitted script swallows
    # its status with `|| true`, so a script that always exited non-zero passed the
    # whole suite -- and in the cluster that is a Job going Failed after its
    # backoffLimit on every run, with the node-join diagnostic then reporting a
    # failed warm-up on runs where the cache was warm: the mirror image of the
    # cold-tag-on-Complete bug the exit condition was rewritten to fix.
    #
    # The stub answers every request with success and gives manifests a record that
    # carries both the amd64 platform and a digest, so the walk completes: index,
    # platform manifest, blob, all warm, missed empty.
    {
        echo '#!/bin/sh'
        echo 'case "$*" in *manifests*) echo "{\"platform\":{\"architecture\":\"amd64\"},\"digest\":\"sha256:'"$(printf 'a%.0s' $(seq 64))"'\"}";; esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    if ! PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1; then
        echo "a run that warmed every tag exited non-zero" >&2
        PATH="$work/bin:$PATH" sh "$work/warm.sh" 2>&1 | tail -8 >&2
        rm -rf "$work"; exit 1
    fi
    rm -rf "$work"
}

@test "every request the warm-up makes goes to the mirror, not to ghcr.io" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # Run the emitted script against a wget that records the URLs it is asked for.
    # Asserting on the URLs the script actually builds survives a refactor of how
    # the base is assembled; the earlier version of this test pinned one literal
    # concatenation and went red the moment that moved into a variable, while
    # saying nothing about where the requests went.
    {
        echo '#!/bin/sh'
        echo 'for a in "$@"; do case "$a" in http*) echo "$a" >> "'"$work"'/urls";; esac; done'
        echo 'case "$*" in *manifests*) echo "{\"x\":\"sha256:'"$(printf 'a%.0s' $(seq 64))"'\"}";; esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1 || true
    [ -s "$work/urls" ] || { echo "the warm-up requested nothing at all" >&2; rm -rf "$work"; exit 1; }
    # The point of the Job is to populate the mirror's blobstore. Fetching from
    # ghcr.io directly would move the same bytes, leave the cache empty, and still
    # look like it worked.
    while read -r u; do
        case "$u" in
            "$GHCR_MIRROR_SVC_URL"/*) ;;
            *) echo "warm-up requested [$u], which is not on the mirror" >&2; rm -rf "$work"; exit 1 ;;
        esac
    done < "$work/urls"
    grep -q 'ghcr\.io' "$work/urls" && { echo "warm-up reached ghcr.io directly" >&2; rm -rf "$work"; exit 1; }
    # And it asked for the kubelet repository, not something else on the mirror.
    grep -qF "/v2/$GHCR_WARM_REPO/" "$work/urls" || {
        echo "warm-up never addressed $GHCR_WARM_REPO" >&2
        rm -rf "$work"; exit 1
    }
    # And a blob, which is the only thing that lands in the cache. Every other
    # assertion here is satisfied by a run that fetches the tag manifest and stops:
    # the exit-status tests all pass too, because a run that requested nothing fails
    # on the `got > 0` half either way. Breaking the digest extraction -- the one
    # regex holding this together, inside an unquoted heredoc -- left all 53 tests
    # green, which is the failure the comments in that file warn about three times.
    grep -q '/blobs/sha256:' "$work/urls" || {
        echo "the warm-up asked for no blob, so nothing would land in the cache" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the warm-up Job requests every tag it was given" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_warm_job_manifest registry:test v1.35.6 v1.34.9)
    for tag in v1.35.6 v1.34.9; do
        printf '%s' "$out" | grep -qF "$tag" || {
            echo "warm-up Job never mentions $tag" >&2
            exit 1
        }
    done
}

@test "the warm-up Job is a valid single Job in the mirror's namespace" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    manifest=hack/e2e-ghcr-mirror.yaml
    work=$(mktemp -d)
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    kinds=$(yq '.kind' "$work/job.yaml" | grep -vc '^---$')
    [ "$kinds" -eq 1 ] || { echo "expected exactly one document, got $kinds" >&2; rm -rf "$work"; exit 1; }
    [ "$(yq '.kind' "$work/job.yaml")" = "Job" ] || { echo "not a Job" >&2; rm -rf "$work"; exit 1; }
    # Same namespace as the mirror: the Job talks to a ClusterIP, and kube-system is
    # where the mirror already lives, so no cross-namespace egress question arises.
    ns=$(yq 'select(.kind == "Deployment") | .metadata.namespace' "$manifest")
    [ "$(yq '.metadata.namespace' "$work/job.yaml")" = "$ns" ] || {
        echo "warm-up Job is not in the mirror's namespace ($ns)" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the warm-up Job carries the image it was given and gives up eventually" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    [ "$(yq '.spec.template.spec.containers[0].image' "$work/job.yaml")" = "registry:test" ] || {
        echo "warm-up Job does not run the image it was given" >&2
        rm -rf "$work"; exit 1
    }
    # A warm-up that retried forever would keep hammering the same flaky upstream leg
    # for the whole run and keep a Pod on a sandbox node that is already short of CPU.
    [ "$(yq '.spec.template.spec.restartPolicy' "$work/job.yaml")" = "Never" ] || {
        echo "warm-up Pod restarts in place instead of failing the Job" >&2
        rm -rf "$work"; exit 1
    }
    limit=$(yq '.spec.backoffLimit' "$work/job.yaml")
    case "$limit" in
        ''|*[!0-9]*) echo "backoffLimit is not a plain number: [$limit]" >&2; rm -rf "$work"; exit 1 ;;
    esac
    rm -rf "$work"
}

@test "the warm-up Job runs PSS-restricted like the mirror it feeds" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    p='.spec.template.spec'
    [ "$(yq "$p.securityContext.runAsNonRoot" "$work/job.yaml")" = "true" ] || { echo "not runAsNonRoot" >&2; rm -rf "$work"; exit 1; }
    [ "$(yq "$p.securityContext.seccompProfile.type" "$work/job.yaml")" = "RuntimeDefault" ] || { echo "no RuntimeDefault seccomp" >&2; rm -rf "$work"; exit 1; }
    [ "$(yq "$p.automountServiceAccountToken" "$work/job.yaml")" = "false" ] || { echo "SA token mounted for a Job that only speaks HTTP" >&2; rm -rf "$work"; exit 1; }
    c="$p.containers[0].securityContext"
    [ "$(yq "$c.allowPrivilegeEscalation" "$work/job.yaml")" = "false" ] || { echo "priv-esc allowed" >&2; rm -rf "$work"; exit 1; }
    [ "$(yq "$c.readOnlyRootFilesystem" "$work/job.yaml")" = "true" ] || { echo "writable rootfs" >&2; rm -rf "$work"; exit 1; }
    [ "$(yq "$c.capabilities.drop[0]" "$work/job.yaml")" = "ALL" ] || { echo "capabilities not dropped" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "a tag that is not a plain version is refused rather than pasted into the Job" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    # The tags reach a `for tag in ...` inside the container's shell script. Anything
    # that is not a bare version there is command substitution running as the Job.
    # The chart's version map is in-tree today, which is exactly why the guard is
    # cheap now and the audit is expensive later. Refusing beats quoting: there is
    # no legitimate kubelet tag this rejects.
    # The last entry is the newline twin of the space-separated case above it. It
    # passed the guard for a while: `grep -q` matches per line and `$` anchors
    # end-of-line, so a value whose FIRST line is a bare version satisfied the
    # pattern and the remainder landed in the heredoc as shell.
    nl=$(printf 'v1.35.6\nid')
    # v1.35.6.7 and v1.35 are the shape-only cases: every character in them is one
    # the class check allows, so they are refused by the regex anchors alone. Without
    # them the ladder's "drop the end anchor" mutation survives, because the class
    # check happens to cover every other entry in this list.
    for bad in 'v1.35.6$(id)' 'v1.35.6;id' 'v1.35.6 v1.34.9' '`id`' '' '../../etc' "$nl" 'v1.35.6.7' 'v1.35'; do
        if out=$(ghcr_warm_job_manifest registry:test "$bad" 2>/dev/null); then
            echo "builder accepted a tag it should refuse: [$bad]" >&2
            exit 1
        fi
        [ -z "$out" ] || { echo "builder printed a Job for a refused tag: [$bad]" >&2; exit 1; }
    done
}

@test "an empty mirror endpoint or repo is refused instead of warming nothing" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    keep_url="$GHCR_MIRROR_SVC_URL"
    keep_repo="$GHCR_WARM_REPO"
    # Found by running the emitted script against a real pull-through registry: with
    # an empty endpoint the Job still builds, still runs, still exits 0, and prints
    # "index unavailable" for every tag. The cache stays cold and the run reports a
    # warm-up that happened. The Job cannot detect this -- an unreachable mirror
    # looks the same from inside -- so the builder has to.
    GHCR_MIRROR_SVC_URL=""
    if out=$(ghcr_warm_job_manifest registry:test v1.35.6 2>/dev/null); then
        GHCR_MIRROR_SVC_URL="$keep_url"
        echo "builder accepted an empty mirror endpoint" >&2
        exit 1
    fi
    [ -z "$out" ] || { GHCR_MIRROR_SVC_URL="$keep_url"; echo "builder printed a Job with no endpoint" >&2; exit 1; }
    GHCR_MIRROR_SVC_URL="$keep_url"
    GHCR_WARM_REPO=""
    if out=$(ghcr_warm_job_manifest registry:test v1.35.6 2>/dev/null); then
        GHCR_WARM_REPO="$keep_repo"
        echo "builder accepted an empty repository" >&2
        exit 1
    fi
    [ -z "$out" ] || { GHCR_WARM_REPO="$keep_repo"; echo "builder printed a Job with no repository" >&2; exit 1; }
    GHCR_WARM_REPO="$keep_repo"
}

@test "the builder emits nothing when given no tags" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_warm_job_manifest registry:test 2>/dev/null) && {
        echo "builder succeeded with no tags to warm" >&2
        exit 1
    }
    [ -z "$out" ] || { echo "builder printed a Job with nothing to warm" >&2; exit 1; }
}

@test "the warm-up runs the image the cluster reports, not one written down here" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    # Behavioural, not a grep over the source: the earlier version of this test
    # searched this file for a second `image:` pin, which would have passed with
    # warm_ghcr_mirror deleted outright. Assert on what gets applied instead.
    stub_get_rc=0
    stub_image="example.test/registry@sha256:feedface"
    stub_apply_capture="$work/applied.yaml"
    warm_ghcr_mirror >/dev/null 2>&1
    stub_apply_capture=""
    stub_image="registry:test"
    [ -s "$work/applied.yaml" ] || { echo "nothing was applied" >&2; rm -rf "$work"; exit 1; }
    got=$(yq '.spec.template.spec.containers[0].image' "$work/applied.yaml")
    [ "$got" = "example.test/registry@sha256:feedface" ] || {
        echo "applied Job runs [$got], not the image the Deployment reported" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the warm-up applies a Job carrying exactly the tags the suites select" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    # The happy path had no test at all: every earlier case exercised a refusal.
    # This pins that a Job reaches kubectl, that the tag list word-splits into one
    # argument per version rather than arriving as one blob, and that the set is
    # the suites' set. Warming the whole map would be ~300 MiB at the one-platform
    # walk the Job ships, all of it across the leg this feature exists because of,
    # three fifths for minors no suite can select.
    stub_get_rc=0
    stub_apply_capture="$work/applied.yaml"
    warm_ghcr_mirror >/dev/null 2>&1
    stub_apply_capture=""
    [ "$(yq '.kind' "$work/applied.yaml")" = "Job" ] || { echo "applied object is not a Job" >&2; rm -rf "$work"; exit 1; }
    script=$(yq '.spec.template.spec.containers[0].args[0]' "$work/applied.yaml")
    want_line=$(printf '%s' "$script" | grep -o 'for tag in [^;]*' | head -1)
    a=$(yq 'keys | sort_by(.) | .[-1]' packages/apps/kubernetes/files/versions.yaml)
    b=$(yq 'keys | sort_by(.) | .[-2]' packages/apps/kubernetes/files/versions.yaml)
    for tag in $(yq ".[\"$a\"], .[\"$b\"]" packages/apps/kubernetes/files/versions.yaml); do
        printf '%s' "$want_line" | grep -qF "$tag" || {
            echo "applied Job does not warm $tag, which a suite selects: [$want_line]" >&2
            rm -rf "$work"; exit 1
        }
    done
    n=$(printf '%s' "$want_line" | tr ' ' '\n' | grep -c '^v[0-9]')
    # Count what the suites actually ask for, from the suites. Comparing against
    # GHCR_WARM_TAG_COUNT would be the test reading the bound it guards: raising the
    # knob to 5 would move both sides together and the assertion would hold while
    # the warm set quietly grew to the whole map. That mutation survived until this
    # line stopped referring to the knob.
    suites=$(grep -ho "run_kubernetes_test '[^']*'" hack/e2e-chainsaw/kubernetes-*/chainsaw-test.yaml \
        | sort -u | wc -l | tr -d ' ')
    [ "$suites" -ge 2 ] || { echo "could not read the suites' version selectors" >&2; rm -rf "$work"; exit 1; }
    [ "$n" -eq "$suites" ] || {
        echo "applied Job warms $n tags, but the suites select $suites distinct versions: [$want_line]" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the pull count follows the configured repository" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    keep="$GHCR_WARM_REPO"
    stub_get_rc=0
    # The filter and the warm-up have to name the same repository. Both read
    # GHCR_WARM_REPO at HEAD, which makes a mutation back to the literal
    # siderolabs/kubelet behaviourally identical -- correctly green, since the value
    # is the same. What that mutation would cost is only visible if the knob moves:
    # point it elsewhere and a hardcoded filter counts zero while the warm-up and the
    # workers both use the new name.
    GHCR_WARM_REPO="example/other"
    stub_log_lines='10.244.1.92 - - [10/Aug/2026:04:33:54 +0000] "GET /v2/example/other/blobs/sha256:cc HTTP/1.1" 200 19455163 "" "containerd/2.2.4+unknown"'
    out=$(ghcr_mirror_diagnose 2>&1)
    # Capture what the run was configured with BEFORE restoring, or the assertion
    # compares the output against the value that was put back.
    used="$GHCR_WARM_REPO"
    GHCR_WARM_REPO="$keep"
    stub_log_lines=""
    printf '%s' "$out" | grep -qF "a worker pulled $used through the mirror" || {
        echo "the count did not follow GHCR_WARM_REPO; a renamed repository reports zero pulls" >&2
        printf '%s\n' "$out" | grep -i 'kubelet' >&2
        exit 1
    }
}

@test "the failure path surfaces how long the mirror took to answer the worker" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # Warm and cold are indistinguishable in every other artifact: the Job reports
    # success either way, because distribution stores the blob in the background and
    # a completed GET is a different event from a stored blob. Server-side duration
    # is the one number that separates them -- milliseconds off disk against minutes
    # streamed from ghcr.io -- and it only exists in this log.
    stub_log_lines=$(printf '%s\n%s' \
        'time="2026-08-11T04:33:54Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:bb" http.response.duration=1m57.479642915s http.request.useragent="containerd/2.2.4+unknown"' \
        'time="2026-08-11T03:10:00Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:aa" http.response.duration=12.2ms http.request.useragent=ghcr-mirror-warm')
    # xtrace off: this runner traces test bodies, the trace goes to the same stderr
    # this captures, and the traced command line contains the whole fixture -- both
    # durations included. The assertion would then read the tracer rather than the
    # section. Not turned back on, for the reason the apply-failure test gives.
    set +x
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_log_lines=""
    # Scope the assertion to this section. The raw access-log dump further down
    # legitimately contains every line, warm-up included, so asserting over the whole
    # output would read the dump and call it a leak -- the assertion has to see what
    # the section printed, not what the function printed.
    section=$(printf '%s\n' "$out" | awk '/how long did the mirror take/{f=1;next} /^--- /{f=0} f')
    printf '%s' "$section" | grep -qF 'http.response.duration=1m57.479642915s' || {
        echo "the worker request duration is not surfaced" >&2
        printf '%s\n' "$out" | tail -12 >&2
        exit 1
    }
    # The warm-up's own timings must not be mixed in: they say nothing about what the
    # worker experienced, and a millisecond figure next to a minutes-long one reads
    # as though the cache was warm.
    # `if`, not a trailing `cmd && { ...; exit 1; }`. This runner injects `return 0`
    # before every closing brace, so a body's status is always 0 and both forms look
    # the same here; real bats takes the status of the body's last command, where an
    # AND-list returns 1 exactly when the guarded command did not fire -- that is,
    # on the passing path. The form is only fatal in final position, which is why it
    # is harmless elsewhere in this file.
    if printf '%s' "$section" | grep -q 'duration=12.2ms'; then
        echo "the warm-up's own duration leaked into the worker measurement" >&2
        printf '%s\n' "$section" >&2
        exit 1
    fi
}

@test "the duration filter reads a log larger than one execve argument" {
    # xtrace off before the fixture exists rather than after: this runner traces test
    # bodies, and the fixture below is a megabyte. Tracing its construction would put
    # that megabyte into the log of every run, green ones included.
    set +x
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # Size is the subject here. Linux caps a single argv element at MAX_ARG_STRLEN,
    # 131072 bytes, independently of ARG_MAX -- measured on 6.8, 131000 bytes go
    # through and 131072 fail with E2BIG -- while macOS caps the whole argv at
    # kern.argmax, a megabyte. By the time this diagnostic runs the log is past both:
    # the readiness probe writes an access line every 5s and the mirror has been up
    # since install. So a filter that receives the log as an argument fails on every
    # real failing run and succeeds only against a fixture small enough to fit, which
    # is the shape every other test here has. Pad past a megabyte so this bites on
    # both kernels rather than only on the CI one.
    worker='time="2026-08-11T04:33:54Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:bb" http.response.duration=1m57.479642915s http.request.useragent="containerd/2.2.4+unknown"'
    probe=$(awk 'BEGIN{ l="10.244.0.1 - - [09/Aug/2026:01:18:00 +0000] \"GET /v2/ HTTP/1.1\" 200 2"; n=int(1100000/(length(l)+1))+1; for(i=0;i<n;i++) print l }')
    stub_log_lines=$(printf '%s\n%s' "$worker" "$probe")
    # The fixture has to prove it is over the limit. A padding expression that
    # silently produced 80 KiB would leave this test green for the wrong reason and
    # pin nothing at all.
    [ "${#stub_log_lines}" -gt 1048576 ] || {
        echo "fixture is ${#stub_log_lines} bytes, under the macOS argv limit it is meant to exceed" >&2
        exit 1
    }
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_log_lines=""
    section=$(printf '%s\n' "$out" | awk '/how long did the mirror take/{f=1;next} /^--- /{f=0} f')
    printf '%s' "$section" | grep -qF 'http.response.duration=1m57.479642915s' || {
        echo "the worker duration was lost in a log of realistic size" >&2
        printf '%s\n' "$out" | grep -iE 'how long|worker request durations|Argument list' >&2
        exit 1
    }
}

@test "a log carrying only the warm-up's own timings says so rather than printing nothing" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # The exclusion above can empty this section legitimately: the warm-up ran, the
    # worker never did. An empty section under a header is the same ambiguity the
    # unreadable-log case has, so the absence has to be stated rather than left as
    # blank space the reader fills in.
    stub_log_lines='time="2026-08-11T03:10:00Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:aa" http.response.duration=12.2ms http.request.useragent=ghcr-mirror-warm'
    set +x
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_log_lines=""
    section=$(printf '%s\n' "$out" | awk '/how long did the mirror take/{f=1;next} /^--- /{f=0} f')
    printf '%s' "$section" | grep -q 'were not observed' || {
        echo "an empty duration section did not say why it was empty" >&2
        printf '%s\n' "$section" >&2
        exit 1
    }
}

@test "a log that could not be read prints no duration section" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    stub_log_rc=1
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_log_rc=0
    # A header with nothing under it reads as "the mirror answered no worker", which
    # is a finding about the cluster. The log was never read, so the honest output is
    # no section at all -- the same discipline the pull count above follows when it
    # says the unreadable log says nothing either way.
    if printf '%s' "$out" | grep -qF 'how long did the mirror take'; then
        echo "the duration section was printed against a log that was never read" >&2
        printf '%s\n' "$out" | tail -12 >&2
        exit 1
    fi
}

@test "the failure path reports whether the warm-up itself got anywhere" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # Payloads only these two reads can produce. Asserting the warm-up's result
    # against stub_log_lines instead would pass with both reads deleted, because the
    # registry access-log read at the end of the same function prints that value.
    stub_job_status='ghcr-mirror-warm   Complete   1/1   4m2s'
    stub_job_log='warm: fetched 16, failed 0'
    out=$(ghcr_mirror_diagnose 2>&1)
    stub_job_status=""
    stub_job_log=""
    # Excluding the warm-up from the pull count without reporting the warm-up leaves
    # the run unfalsifiable: a red suite looks the same whether the cache was warm
    # and the guest still starved, or the warm-up never came up. This is the reader
    # that tells those apart, and it runs on the node-join failure path where the
    # question is actually asked.
    printf '%s' "$out" | grep -q 'did the warm-up populate the cache' || {
        echo "the failure path says nothing about the warm-up" >&2
        exit 1
    }
    printf '%s' "$out" | grep -q 'warm: fetched 16, failed 0' || {
        echo "the warm-up's own result is not surfaced" >&2
        printf '%s\n' "$out" | tail -8 >&2
        exit 1
    }
    printf '%s' "$out" | grep -q 'Complete   1/1' || {
        echo "the warm-up Job's status is not surfaced, so a Job that never ran looks the same" >&2
        printf '%s\n' "$out" | tail -8 >&2
        exit 1
    }
}

@test "the warm-up Job outlives the suites that would ask about it" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    ttl=$(yq '.spec.ttlSecondsAfterFinished' "$work/job.yaml")
    dl=$(yq '.spec.activeDeadlineSeconds' "$work/job.yaml")
    # The only reader of this Job's outcome is the node-join failure block, which
    # fires at the 29m node-join deadline, in a suite that starts tens of minutes
    # after install. A TTL that expires first deletes the evidence before the one
    # moment it is wanted.
    [ "$ttl" -gt "$dl" ] || {
        echo "ttlSecondsAfterFinished ($ttl) does not outlast activeDeadlineSeconds ($dl)" >&2
        rm -rf "$work"; exit 1
    }
    [ "$ttl" -ge 7200 ] || {
        echo "ttlSecondsAfterFinished ($ttl) is shorter than the gap to the suites that read it" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the caller's log names a missing yq, not an empty version map" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    stub_get_rc=0
    # The reason has to reach the log of the function an operator reads, not just the
    # helper that produced it. The earlier version of this pinned ghcr_warm_tags
    # directly and passed while warm_ghcr_mirror silently swallowed the line with a
    # 2>/dev/null and blamed versions.yaml instead.
    set +x
    err=$(PATH="$work" warm_ghcr_mirror 2>&1 >/dev/null) || true
    printf '%s' "$err" | grep -q 'yq is not installed' || {
        echo "warm_ghcr_mirror did not surface the missing yq; it reported:" >&2
        printf '%s\n' "$err" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "a missing yq is not reported as an empty version map" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    # Same distinction the kubectl path already makes: "the file says nothing" and
    # "the tool that reads the file is absent" are different causes, and only the
    # message carries which. An empty map would send the reader to versions.yaml.
    set +x
    err=$(PATH="$work" ghcr_warm_tags 2>&1 >/dev/null) || true
    printf '%s' "$err" | grep -q 'yq is not installed' || {
        echo "a missing yq was not named; the caller would blame the version map" >&2
        printf '%s\n' "$err" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "an image reference that is not one is refused rather than interpolated" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # The image is the only builder input that comes from the cluster, and it lands in
    # the Job YAML. Reading it requires no privilege but writing it does, so this is
    # not a trust boundary -- it is symmetry: both locally-set inputs are checked and
    # the tags are pattern-matched, and leaving the third unchecked reads as though
    # the builder validates nothing.
    stub_image='registry:test;id'
    out=$(warm_ghcr_mirror 2>&1)
    stub_image="registry:test"
    printf '%s' "$out" | grep -q 'is not an image reference' || {
        echo "a non-reference image was accepted:" >&2
        printf '%s\n' "$out" >&2
        exit 1
    }
}

@test "the warm-up says so when it cannot read the mirror image" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    stub_image=""
    out=$(warm_ghcr_mirror 2>&1)
    stub_image="registry:test"
    printf '%s' "$out" | grep -q 'could not read the ghcr-mirror image' || {
        echo "an unreadable image was not reported" >&2
        printf '%s\n' "$out" >&2
        exit 1
    }
}

@test "a transient lookup failure is not reported as an absent mirror" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=1
    stub_get_out='Error from server: etcdserver: request timed out'
    out=$(warm_ghcr_mirror 2>&1)
    stub_get_rc=0
    stub_get_out=""
    # resolve_ghcr_mirror_endpoint in this same file documents at length why only the
    # API's own NotFound means "never applied here". Saying "not deployed" after an
    # apiserver blip, a bad context or a missing kubectl blames the wrong thing in
    # the log, and the captured message is the only place the real cause exists.
    printf '%s' "$out" | grep -q 'not deployed' && {
        echo "a transient failure was reported as an absent mirror" >&2
        printf '%s\n' "$out" >&2
        exit 1
    }
    printf '%s' "$out" | grep -q 'etcdserver' || {
        echo "the real reason was discarded" >&2
        printf '%s\n' "$out" >&2
        exit 1
    }
}

@test "warm-up tags come out in the order the suites select them" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    keep="$GHCR_WARM_VERSIONS_FILE"
    work=$(mktemp -d)
    # The loop is serial, so order decides how long the tag the suite needs waits.
    # A new minor is appended at the bottom of versions.yaml -- that is what the
    # bump path does -- and file order then puts it last while `keys | sort_by(.) |
    # .[-1]` puts it first. Warming in file order would leave the one tag that
    # matters behind every tag that does not, with no signal that it happened.
    printf '"v1.34": "v1.34.9"\n"v1.33": "v1.33.13"\n"v1.36": "v1.36.1"\n' > "$work/versions.yaml"
    GHCR_WARM_VERSIONS_FILE="$work/versions.yaml"
    first=$(ghcr_warm_tags | head -1)
    GHCR_WARM_VERSIONS_FILE="$keep"
    [ "$first" = "v1.36.1" ] || {
        echo "warm-up starts with [$first]; the suites would ask for v1.36.1 first" >&2
        rm -rf "$work"; exit 1
    }
    # And on the real file the first two must be exactly what the two suites pick.
    a=$(yq 'keys | sort_by(.) | .[-1]' packages/apps/kubernetes/files/versions.yaml)
    b=$(yq 'keys | sort_by(.) | .[-2]' packages/apps/kubernetes/files/versions.yaml)
    want=$(yq ".[\"$a\"], .[\"$b\"]" packages/apps/kubernetes/files/versions.yaml)
    got=$(ghcr_warm_tags | head -2)
    [ "$got" = "$want" ] || {
        echo "first two warmed tags [$got] are not the two the suites select [$want]" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "an unreachable mirror fails the warm-up Job instead of completing it" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # Stub wget rather than talk to a real socket: this pins the script's own
    # control flow, which is where the defect was. Every fetch fails, exactly as it
    # does when the mirror has no endpoints yet -- the Job is submitted seconds
    # after the Deployment is created and the registry image is not in the pre-pull
    # set, so it has to pull before it can serve.
    printf '#!/bin/sh\nexit 1\n' > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    keep_ready="${GHCR_WARM_READY_TIMEOUT:-}"
    GHCR_WARM_READY_TIMEOUT=1
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    GHCR_WARM_READY_TIMEOUT="$keep_ready"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    # A script that exits 0 here makes backoffLimit dead: the Job goes Complete,
    # ttlSecondsAfterFinished deletes it, and nothing ever retries the warm-up.
    if PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1; then
        echo "the warm-up script reported success with every fetch failing" >&2
        rm -rf "$work"; exit 1
    fi
    rm -rf "$work"
}

@test "a mirror that answers but cannot deliver blobs also fails the Job" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # The sibling test covers the mirror being unreachable, which exits inside the
    # readiness wait. That left the FINAL exit status unpinned: a mutation replacing
    # it with `true` survived, because no test reached it. This is the live-mirror
    # half -- /v2/ answers, manifests answer, blobs do not -- which is exactly the
    # upstream failure the access log showed (HTTP 500 mid-blob after 7-8 MiB).
    {
        echo '#!/bin/sh'
        echo 'case "$*" in'
        echo '  *"/v2/ "*|*"/v2/"|*"/v2/ -"*) exit 0 ;;'
        echo '  *manifests*) echo "{\"d\":\"sha256:'"$(printf 'b%.0s' $(seq 64))"'\"}"; exit 0 ;;'
        echo '  *blobs*) exit 1 ;;'
        echo 'esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    if PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1; then
        echo "the Job reported success with every blob fetch failing" >&2
        rm -rf "$work"; exit 1
    fi
    rm -rf "$work"
}

@test "building the Job runs nothing and says nothing" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    # The Job's script lives in an UNQUOTED heredoc, so every backtick and $(...)
    # in it -- including inside comments -- is command substitution at render time.
    # A shell snippet quoted in a comment there once expanded into an endless loop:
    # the builder hung, and every structural assertion still passed, because the
    # YAML it never finished printing was valid as far as it got. Silence on stderr
    # is the cheap observable for "nothing was executed while rendering".
    #
    # xtrace off first, and not turned back on, for the reason the apply-failure test
    # below gives: this runner traces test bodies, that trace goes to the same stderr
    # this assertion reads, and it would then report the tracer rather than the
    # builder. Under plain bats a `set -x` here would enable tracing that was never
    # on, and a failing test after that hangs the runner instead of reporting.
    set +x
    ghcr_warm_job_manifest registry:test v1.35.6 >"$work/out.yaml" 2>"$work/err"
    [ ! -s "$work/err" ] || {
        echo "rendering the Job wrote to stderr, so something ran:" >&2
        head -5 "$work/err" >&2
        rm -rf "$work"; exit 1
    }
    [ "$(yq '.kind' "$work/out.yaml")" = "Job" ] || { echo "render produced no Job" >&2; rm -rf "$work"; exit 1; }
    rm -rf "$work"
}

@test "the Job heredoc carries no backticks at all" {
    f=hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    # Structural companion to the test above, and the reason both exist: a
    # substitution that HANGS cannot be caught by observing output, because there
    # is none and the runner never returns. This one fails instead of hanging.
    body=$(awk '/cat <<EOF$/{f=1;next} /^EOF$/{f=0} f' "$f")
    [ -n "$body" ] || { echo "could not isolate the heredoc body" >&2; exit 1; }
    if printf '%s\n' "$body" | grep -q '`'; then
        echo "the unquoted heredoc contains a backtick, which runs at render time:" >&2
        printf '%s\n' "$body" | grep -n '`' >&2
        exit 1
    fi
}

@test "a cold tag among several fails the Job even when the counters stay clean" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # Two tags, the second one's platform manifest unavailable. That path moved
    # neither counter -- no blob was attempted for it, and the index fetch succeeded
    # -- so the Job completed with one suite's tag cold and reported "failed 0".
    # Every other exit-status test here is single-tag, which is why it survived.
    amd=sha256:2222222222222222222222222222222222222222222222222222222222222222
    gone=sha256:9999999999999999999999999999999999999999999999999999999999999999
    {
        echo '#!/bin/sh'
        echo 'case "$*" in'
        echo "  *manifests/v1.35.6*) printf '{ \"manifests\": [ { \"digest\": \"$amd\", \"platform\": { \"architecture\": \"amd64\", \"os\": \"linux\" } } ] }' ;;"
        echo "  *manifests/v1.34.9*) printf '{ \"manifests\": [ { \"digest\": \"$gone\", \"platform\": { \"architecture\": \"amd64\", \"os\": \"linux\" } } ] }' ;;"
        echo "  *manifests/$amd*) echo '{\"layers\":[{\"digest\":\"sha256:3333333333333333333333333333333333333333333333333333333333333333\"}]}' ;;"
        echo "  *manifests/$gone*) exit 1 ;;"
        echo 'esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 v1.34.9 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    if PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >"$work/out" 2>&1; then
        echo "the Job completed with a selected tag left cold:" >&2
        grep '^warm: ' "$work/out" >&2
        rm -rf "$work"; exit 1
    fi
    grep -qF 'not warmed: v1.34.9' "$work/out" || {
        echo "the cold tag is not named in the report" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "a partly fetched tag is not reported as warmed" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # One blob lands, the next fails -- the shape a live run produced when the
    # upstream reset mid-transfer. Counting "did any blob arrive" called that tag
    # warmed, and a half-cached tag still sends the worker across the slow leg for
    # the rest, which is the whole thing being measured.
    ok=sha256:4444444444444444444444444444444444444444444444444444444444444444
    bad=sha256:5555555555555555555555555555555555555555555555555555555555555555
    {
        echo '#!/bin/sh'
        echo 'case "$*" in'
        echo "  *manifests/v1.35.6*) echo '{\"layers\":[{\"digest\":\"$ok\"},{\"digest\":\"$bad\"}]}' ;;"
        echo "  *blobs/$bad*) exit 1 ;;"
        echo 'esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    out=$(PATH="$work/bin:$PATH" sh -e "$work/warm.sh" 2>&1) || true
    printf '%s' "$out" | grep -q 'warm: warmed: *$' || {
        echo "a tag with a failed blob was reported as warmed:" >&2
        printf '%s\n' "$out" | grep '^warm: ' >&2
        rm -rf "$work"; exit 1
    }
    printf '%s' "$out" | grep -qF 'not warmed: v1.35.6' || {
        echo "the partly fetched tag is not listed as missed" >&2
        printf '%s\n' "$out" | grep '^warm: ' >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "only the platform the workers run is fetched, and it is not chosen by luck" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # A two-platform index in the shape a registry actually returns: pretty-printed,
    # arm64 listed first. Walking every manifest in the index fetched both, and the
    # digest sort put arm64 ahead of amd64 for both warmed tags, so on the slow leg
    # this feature exists for the Job spent its first half on an architecture nothing
    # in the sandbox pulls and left the second tag cold.
    arm=sha256:1111111111111111111111111111111111111111111111111111111111111111
    amd=sha256:2222222222222222222222222222222222222222222222222222222222222222
    {
        echo '#!/bin/sh'
        echo 'for a in "$@"; do case "$a" in http*) echo "$a" >> "'"$work"'/urls";; esac; done'
        echo 'case "$*" in'
        echo "  *manifests/v1.35.6*) printf '{\\n  \"manifests\": [\\n    { \"digest\": \"$arm\", \"platform\": { \"architecture\": \"arm64\", \"os\": \"linux\" } },\\n    { \"digest\": \"$amd\", \"platform\": { \"architecture\": \"amd64\", \"os\": \"linux\" } }\\n  ]\\n}' ;;"
        echo "  *manifests/$amd*) echo '{\"layers\":[{\"digest\":\"sha256:3333333333333333333333333333333333333333333333333333333333333333\"}]}' ;;"
        echo 'esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1 || true
    grep -qF "$amd" "$work/urls" || {
        echo "the amd64 manifest was never requested" >&2
        rm -rf "$work"; exit 1
    }
    grep -qF "$arm" "$work/urls" && {
        echo "the arm64 manifest was fetched; that is bytes no worker in the sandbox pulls" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "a run that fetched nothing is a failure, not a quiet success" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # Every request succeeds and the manifests carry no digests, so no blob is ever
    # asked for: failed stays 0 and so does the fetched count. Guarding only on
    # "nothing failed" calls that a warm cache. A mutation dropping the `got > 0`
    # half survived the rest of the suite, which is what this pins.
    #
    # Not hypothetical for a pull-through proxy: a 200 carrying an error document,
    # or a manifest for a platform set this repo does not use, both land here.
    {
        echo '#!/bin/sh'
        echo 'case "$*" in *manifests*) echo "{}" ;; esac'
        echo 'exit 0'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    if PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1; then
        echo "the Job reported success after fetching zero blobs" >&2
        rm -rf "$work"; exit 1
    fi
    rm -rf "$work"
}

@test "the warm-up waits for the mirror instead of concluding on the first refusal" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    mkdir -p "$work/bin"
    # Count the readiness attempts: the Job is submitted seconds after the mirror
    # Deployment, so a single probe would decide "not serving" while the registry is
    # still pulling its own image. This pins that the wait retries rather than that
    # some particular line of shell exists.
    {
        echo '#!/bin/sh'
        echo 'case "$*" in'
        echo '  *"/v2/"*) echo x >> "'"$work"'/probes"; exit 1 ;;'
        echo 'esac'
        echo 'exit 1'
    } > "$work/bin/wget"
    chmod +x "$work/bin/wget"
    keep="${GHCR_WARM_READY_TIMEOUT:-}"
    # The wait is a wall clock and each attempt costs only its 5s sleep (Cilium
    # rejects a Service with no ready endpoints, so the bounded fetch returns at
    # once), which means the test sleeps for the whole budget. 15s buys three tries,
    # enough to tell a loop that retries from one that concludes on the first
    # refusal, and does not add 45s to the file for no extra discrimination.
    # 20, not 15: at 15 the derived bound equals two while the loop makes three
    # probes, so one slow iteration on a loaded runner eats the whole margin.
    budget=20
    GHCR_WARM_READY_TIMEOUT="$budget"
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    GHCR_WARM_READY_TIMEOUT="$keep"
    yq '.spec.template.spec.containers[0].args[0]' "$work/job.yaml" > "$work/warm.sh"
    t0=$(date +%s)
    PATH="$work/bin:$PATH" sh -e "$work/warm.sh" >/dev/null 2>&1 || true
    elapsed=$(( $(date +%s) - t0 ))
    n=$(wc -l < "$work/probes" 2>/dev/null || echo 0)
    # The elapsed clock is the claim itself, the probe count below is only its
    # shadow. At this budget the derived count collapses to a number a hardcoded
    # three-iteration loop can also produce -- a count-only assertion is vacuous
    # exactly at the budget the test can afford to run. A loop that counts
    # iterations instead of watching the clock finishes in 3x5s and fails here;
    # a loop that watches the clock cannot leave before the budget is spent.
    [ "$elapsed" -ge "$budget" ] || {
        echo "the readiness wait gave up after ${elapsed}s of a ${budget}s budget; it is not keeping a wall clock" >&2
        rm -rf "$work"; exit 1
    }
    # Against the budget, not a magic 2. Cilium rejects a Service with no ready
    # endpoints instead of dropping (serviceNoBackendResponse defaults to reject), so
    # each attempt costs the 5s sleep and nothing else -- a 45s budget is nine tries.
    # A `>= 2` assertion tolerated an accounting bug that gave up after 16s while the
    # message still said 45; asking for most of the budget catches that direction.
    # Derived from the same variable that sets the budget, so the two cannot drift.
    want=$(( budget / 5 - 1 ))
    [ "$n" -ge "$want" ] || {
        echo "readiness was probed $n time(s) inside a ${budget}s budget; expected at least $want" >&2
        rm -rf "$work"; exit 1
    }
    rm -rf "$work"
}

@test "the blob fetch timeout stays sized for the throttled leg" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_warm_job_manifest registry:test v1.35.6)
    # 600 is a decision, not a default: the leg this Job crosses runs at 40-165 KB/s
    # with streams that stall mid-blob, and a short inactivity timeout converts a
    # slow-but-alive transfer into a failed one -- on exactly the runs the warm-up
    # exists for. Structural on purpose: the emitted script is data here, and the
    # claim is that the value does not drift silently, not that busybox wget
    # interprets it one way or another.
    # In the emitted manifest the heredoc's \$ is already resolved: the script text
    # carries a literal $blob, not a backslash. Grepping for the source file's
    # spelling here finds nothing and fails the healthy code.
    line=$(printf '%s' "$out" | grep -F 'blobs/$blob' | grep -o 'get -T [0-9]*' | head -1)
    to=${line#get -T }
    [ -n "$to" ] || {
        echo "could not find the blob fetch timeout in the emitted script" >&2
        exit 1
    }
    [ "$to" -ge 600 ] || {
        echo "the blob fetch timeout is ${to}s; a stalled-but-alive transfer on the throttled leg needs at least 600" >&2
        exit 1
    }
}

@test "the warm-up identifies itself so the pull diagnostic can exclude it" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    out=$(ghcr_warm_job_manifest registry:test v1.35.6)
    printf '%s' "$out" | grep -qF -- "-U ${GHCR_WARM_JOB_NAME}" || {
        echo "warm-up requests are indistinguishable from a worker's in the access log" >&2
        exit 1
    }
}

@test "the diagnostic does not count its own warm-up as a worker pull" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    # After the warm-up ships, every run has 50+ `GET /v2/siderolabs/kubelet/...`
    # lines from kube-system before any tenant exists. Counting those turns "the
    # mirror served N requests for the image, so workers did reach it" into a
    # sentence that is true of every run and therefore says nothing. These runs are
    # already hard to attribute from outside the guest; a signal that is always
    # positive removes the last one that distinguishes them.
    # The real three-line shape per GET, measured against the pinned registry image:
    # a challenge line, a "response completed" line, and one nginx-combined access
    # line. All three carry the user-agent -- the structured pair as
    # http.request.useragent=, the combined line positionally -- which is what makes
    # the exclusion able to be complete. A one-line-per-GET fixture would let this
    # test pass on a filter that misses two thirds of the traffic.
    warm=$(i=0; while [ "$i" -lt 40 ]; do
        echo 'time="2026-08-10T03:10:00Z" level=info msg="Challenge established with upstream" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:aa" http.request.useragent=ghcr-mirror-warm'
        echo 'time="2026-08-10T03:10:00Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:aa" http.request.useragent=ghcr-mirror-warm'
        echo '10.244.0.5 - - [10/Aug/2026:03:10:00 +0000] "GET /v2/siderolabs/kubelet/blobs/sha256:aa HTTP/1.1" 200 19455163 "" "ghcr-mirror-warm"'
        i=$((i+1)); done)
    stub_log_lines="$warm"
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -qF "no $GHCR_WARM_REPO request appears in the mirror" || {
        echo "warm-up traffic was counted as a worker pull" >&2
        printf '%s\n' "$out" | grep -iE 'through the mirror|reached the mirror' >&2
        exit 1
    }
    # And a real worker pull mixed into the same log must still be found.
    # Three lines per request, which is what the pinned registry image actually
    # writes for one GET; a one-line-per-request fixture is a shape the wire never
    # produces, and it would let a count-based assertion look right for the wrong
    # reason.
    worker=$(printf '%s\n%s\n%s' \
        'time="2026-08-10T04:33:54Z" level=info msg="response completed" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:bb" http.request.useragent="containerd/2.2.4+unknown"' \
        '10.244.1.92 - - [10/Aug/2026:04:33:54 +0000] "GET /v2/siderolabs/kubelet/blobs/sha256:bb HTTP/1.1" 200 19455163 "" "containerd/2.2.4+unknown"' \
        'time="2026-08-10T04:33:54Z" level=info msg="Challenge established with upstream" http.request.uri="/v2/siderolabs/kubelet/blobs/sha256:bb" http.request.useragent=containerd/2.2.4+unknown')
    stub_log_lines=$(printf '%s\n%s' "$warm" "$worker")
    out=$(ghcr_mirror_diagnose 2>&1)
    printf '%s' "$out" | grep -q 'a worker pulled siderolabs/kubelet through the mirror' || {
        echo "a worker pull was lost when warm-up lines were excluded" >&2
        printf '%s\n' "$out" | grep -iE 'kubelet' >&2
        exit 1
    }
    # One request, three log lines. Counting lines reported three, which is why the
    # count is taken off the access-line shape that appears exactly once per request.
    printf '%s' "$out" | grep -q '(1 request(s)' || {
        echo "the count is not a request count:" >&2
        printf '%s\n' "$out" | grep -i 'pulled the kubelet' >&2
        exit 1
    }
    stub_log_lines=""
}

@test "the warm-up Job cannot run past the step that waits for it to be irrelevant" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    ghcr_warm_job_manifest registry:test v1.35.6 > "$work/job.yaml"
    # Bounded wall clock, not just bounded retries: the fetches are slow by nature
    # on the leg this exists for, so a Job with only backoffLimit can sit pulling
    # on a sandbox node for hours after the suites it was meant to help are done.
    d=$(yq '.spec.activeDeadlineSeconds' "$work/job.yaml")
    case "$d" in
        ''|null|*[!0-9]*) echo "no numeric activeDeadlineSeconds: [$d]" >&2; rm -rf "$work"; exit 1 ;;
    esac
    rm -rf "$work"
}

@test "a warm-up that cannot be applied does not fail the caller" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    stub_get_rc=0
    stub_apply_rc=1
    # Best-effort by construction: this runs during install, and the run is expected
    # to survive without it exactly as it did before the warm-up existed.
    warm_ghcr_mirror >/dev/null 2>&1 || {
        echo "a failed warm-up apply propagated to the caller" >&2
        stub_apply_rc=0
        exit 1
    }
    stub_apply_rc=0
}

@test "the warm-up is skipped when the mirror was never deployed" {
    . hack/e2e-chainsaw/_lib/ghcr-mirror.sh
    work=$(mktemp -d)
    stub_get_rc=1
    stub_get_out='Error from server (NotFound): deployments.apps "ghcr-mirror" not found'
    # A valid image and an apply capture, deliberately: with only the message
    # asserted, deleting the early return after "not deployed" survived the suite,
    # because the flow then died further down on an unreadable image and still
    # applied nothing. The test's name promises a skip; the assertion has to be
    # "nothing was applied", and the fixture has to remove every later obstacle so
    # that a deleted return actually reaches kubectl.
    stub_image='mirror.gcr.io/library/registry:2.8.3@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    stub_apply_capture="$work/applied.yaml"
    out=$(warm_ghcr_mirror 2>&1)
    stub_get_rc=0
    stub_get_out=""
    stub_image=""
    stub_apply_capture=""
    printf '%s' "$out" | grep -q 'not deployed' || {
        echo "warm-up did not say why it did nothing" >&2
        printf '%s\n' "$out" >&2
        rm -rf "$work"; exit 1
    }
    if [ -s "$work/applied.yaml" ]; then
        echo "warm-up said 'not deployed' and then applied a Job anyway:" >&2
        head -5 "$work/applied.yaml" >&2
        rm -rf "$work"; exit 1
    fi
    rm -rf "$work"
}

@test "install starts the warm-up in the same step that deploys the mirror" {
    f=hack/e2e-install-cozystack.bats
    # The warm-up only pays off if it runs tens of minutes before the kubernetes
    # suites. Anywhere later and the first tenant worker still races a cold cache,
    # which is the failure it exists to remove.
    block=$(awk '/^@test /{b=""} {b=b ORS $0} /^}$/ && b ~ /e2e-ghcr-mirror\.yaml/ {print b; exit}' "$f")
    [ -n "$block" ] || { echo "no @test block in $f applies hack/e2e-ghcr-mirror.yaml" >&2; exit 1; }
    printf '%s\n' "$block" | grep -qF 'warm_ghcr_mirror' || {
        echo "the mirror is deployed without starting its warm-up" >&2
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
