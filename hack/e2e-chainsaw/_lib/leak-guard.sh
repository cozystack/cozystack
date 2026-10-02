# shellcheck shell=bash
# Sourced by hack/e2e-chainsaw-run.sh, which runs one `chainsaw test` per suite
# directory and calls lg_check_suite after each. Defines functions only.
#
# Why this exists. A suite can pass while the workloads it created outlive it.
# Nothing then fails until a later suite cannot schedule, hours after the cause
# and under the wrong name (a harbor `Insufficient memory` whose memory was held
# by brokers of four Kafka releases from suites that had already passed). This
# fails the suite that leaked, and names what it left behind.
#
# Attribution. A pod is charged to a suite when all three hold after the suite's
# own cleanup:
#   - it is not terminal (phase is neither Succeeded nor Failed), so it still
#     holds its requests on a node;
#   - its UID was not in the pod list taken before the suite started;
#   - the root of its ownerReferences chain was created at or after the suite
#     started, read on the apiserver's clock.
# A platform pod fails the third condition even when a rollout, an eviction or
# a crash replaced it mid-suite, because its root (the Deployment, DaemonSet,
# StatefulSet or operator CR the install created) is older than the suite. The
# second condition covers what the third cannot: a pre-existing pod adopted by
# a controller recreated mid-suite has a new root, but it is not the suite's
# pod. A leak is charged once, to its own suite: the next suite sees an old
# root.
#
# The start instant comes from an apiserver rather than from this machine. With
# several control-plane nodes the reading and a root's creationTimestamp may be
# stamped by different apiservers, so the comparison is across node clocks.
# No skew margin is added; the rule assumes those clocks agree more closely
# than the gap between the reading and the suite's first object, which is at
# least a chainsaw process start plus an API round trip. A node whose clock ran
# behind the reading's by more than that would make a suite's first objects
# look older than the suite and clear their pods; one running ahead only errs
# toward charging.
#
# The reading is the creationTimestamp on a server-side dry-run create: the
# generic registry's Store.create stamps it through
# rest.FillObjectMetaSystemFields before it hands the object to
# DryRunnableStorage.Create, which on a dry run copies it back without
# persisting anything (staging/src/k8s.io/apiserver/pkg/registry/generic/
# registry/store.go and dryrun.go, Kubernetes v1.33.0).
# creationTimestamp has one-second resolution; the comparison is `>=`, so an
# object created in the second the suite started counts as the suite's, which
# is the direction that errs toward reporting.

# How far the ownerReferences walk may go; the cap exists so an odd or cyclic
# chain costs a bounded number of reads. The longest chains here belong to
# tenant Kubernetes clusters. A tenant's apiserver pods run Pod -> ReplicaSet ->
# Deployment -> TenantControlPlane -> KamajiControlPlane -> Cluster, five links
# as the providers set them, which six walks whole. A worker VM's virt-launcher
# pod climbs through the Cluster API Machine objects and can run past six;
# there the walk stops at a MachineDeployment from a release the suite created,
# which gives the same verdict as the Cluster. A walk that hits the cap stops at
# the object it reached and says `depth-cap`. An owner normally exists before
# what it owns, so that object is usually no older than the real root, and a
# cut walk errs toward charging a pod rather than clearing it.
LG_DEPTH_CAP=${LG_DEPTH_CAP:-6}

# How long after a suite's chainsaw run returns its pods may still exist: the
# default `timeouts.cleanup` in hack/e2e-chainsaw/.chainsaw.yaml, five minutes
# (hack/chainsaw-leak-guard_test.bats keeps the two equal). A suite that
# overrides its own cleanup timeout (kafka sets 8m) is covered all the same:
# the runner calls the check only after `chainsaw test` has returned, and that
# happens once every cleanup in the suite has finished under whatever timeout
# it had, so the budget starts after the override has been spent rather than
# competing with it. What remains after chainsaw's own cleanup is the tail it does
# not wait for: garbage collection of the workloads under the deleted release,
# then each pod's termination grace. The leaks this exists for sat about 110
# minutes (Kafka brokers) and 30 minutes and more (a TenantControlPlane on its
# finalizer) past the end of their suites, so the budget is not what separates
# them from a slow teardown. The clear time of every suite is logged, so later
# runs can check the number against measurements.
LG_BUDGET=${COZY_LEAK_BUDGET:-300}

# lg_apiserver_now — the apiserver's current time as RFC 3339, second resolution.
lg_apiserver_now() {
    local ts
    ts=$(kubectl create configmap cozy-leak-guard-clock --namespace default \
        --dry-run=server --output 'jsonpath={.metadata.creationTimestamp}') || return 1
    [ -n "$ts" ] || { echo "leak-guard: the apiserver returned no creationTimestamp" >&2; return 1; }
    printf '%s\n' "$ts"
}

# lg_pods — every pod in the cluster as JSON. A failed read is an error, never
# an empty list: an empty list would read as a clean suite.
lg_pods() {
    kubectl get pods --all-namespaces --output json
}

# lg_baseline — the UIDs of every pod now, as a JSON array.
lg_baseline() {
    local pods
    pods=$(lg_pods) || return 1
    jq -c '[.items[].metadata.uid]' <<<"$pods"
}

# lg_resource APIVERSION KIND — the kubectl resource argument for an owner.
lg_resource() {
    case $1 in
        */*) printf '%s.%s.%s\n' "$2" "${1#*/}" "${1%%/*}" ;;
        *) printf '%s\n' "$2" ;;
    esac
}

# lg_root NAMESPACE OBJECT_JSON — prints "<creationTimestamp> <Kind>/<ns>/<name>"
# of the root of OBJECT's ownerReferences chain, with a note when the walk
# stopped early. The controller reference is followed when there is one, the
# first reference otherwise. An owner the apiserver reports NotFound ends the
# walk there; any other failed read is an error, because treating it as a
# missing owner would charge a platform pod whose lookup merely timed out.
#
# The owner is named as Kind.version.group, which kubectl resolves by kind:
# cli-runtime's Builder.mappingFor (staging/src/k8s.io/cli-runtime/pkg/resource/
# builder.go, Kubernetes v1.33.0) falls back from a resource name to
# schema.ParseKindArg and RESTMapping when no resource matches.
lg_root() {
    local ns=$1 obj=$2 depth=0 note="" ref api kind name next errf
    while :; do
        ref=$(jq -r '[.metadata.ownerReferences[]?] | ((map(select(.controller == true)) + .)[0]) // empty
            | "\(.apiVersion) \(.kind) \(.name)"' <<<"$obj")
        [ -n "$ref" ] || break
        if [ "$depth" -ge "$LG_DEPTH_CAP" ]; then
            note=" (depth-cap)"
            break
        fi
        read -r api kind name <<<"$ref"
        errf=$(mktemp)
        if ! next=$(kubectl get "$(lg_resource "$api" "$kind")" --namespace "$ns" "$name" --output json 2>"$errf"); then
            if grep -q '(NotFound)' "$errf"; then
                rm -f "$errf"
                note=" (owner $kind/$name missing)"
                break
            fi
            echo "leak-guard: could not read owner $kind/$name in $ns: $(cat "$errf")" >&2
            rm -f "$errf"
            return 1
        fi
        rm -f "$errf"
        obj=$next
        depth=$((depth + 1))
    done
    jq -r --arg note "$note" \
        '"\(.metadata.creationTimestamp) \(.kind)/\(.metadata.namespace // "-")/\(.metadata.name)\($note)"' <<<"$obj"
}

# lg_leaks START BASELINE_JSON — prints one line per pod charged to the suite
# that started at START. Returns 1 when the pod list or an owner cannot be read.
lg_leaks() {
    local start=$1 baseline=$2 pods cands pod ns root root_ts
    pods=$(lg_pods) || { echo "leak-guard: could not list pods" >&2; return 1; }
    # Filtered into a variable rather than piped into the loop, so a pod list
    # jq cannot parse is an error here instead of an empty loop that reads as
    # a clean suite.
    cands=$(jq -c --argjson base "$baseline" '
        ($base | map({(.): true}) | add // {}) as $seen
        | .items[]
        | select(.status.phase != "Succeeded" and .status.phase != "Failed")
        | select($seen[.metadata.uid] | not)' <<<"$pods") || {
        echo "leak-guard: the pod list could not be parsed" >&2
        return 1
    }
    while IFS= read -r pod; do
        [ -n "$pod" ] || continue
        ns=$(jq -r '.metadata.namespace' <<<"$pod")
        root=$(lg_root "$ns" "$pod") || return 1
        root_ts=${root%% *}
        [[ ! "$root_ts" < "$start" ]] || continue
        jq -r --arg root "${root#* }" --arg rts "$root_ts" \
            '"  \(.metadata.namespace)/\(.metadata.name) phase=\(.status.phase) deleting=\(.metadata.deletionTimestamp // "no") root=\($root) rootCreated=\($rts) instance=\(.metadata.labels["app.kubernetes.io/instance"] // "-")"' <<<"$pod"
    done <<<"$cands"
}

# lg_allowlisted SUITE ALLOWLIST — prints the issue ref when SUITE is listed.
lg_allowlisted() {
    awk -v s="$1" '$1 !~ /^#/ && $1 == s { print $2; exit }' "$2"
}

# lg_allowlist_validate ALLOWLIST ROOT — every entry must name an existing suite
# directory under ROOT and carry an issue ref, so a renamed or removed suite
# cannot keep an exemption nobody reads.
lg_allowlist_validate() {
    local list=$1 root=$2 bad=0 suite ref rest
    # `|| [ -n "$suite" ]` keeps a last line with no newline, which `read`
    # drops and lg_allowlisted's awk does not: without it that entry would
    # exempt its suite without ever being validated.
    while read -r suite ref rest || [ -n "$suite" ]; do
        case $suite in ''|'#'*) continue ;; esac
        if [ -z "$ref" ] || [ -n "$rest" ]; then
            echo "leak-guard: allowlist line for '$suite' must be '<suite-dir> <issue-ref>'" >&2
            bad=1
        fi
        if [ ! -f "$root/$suite/chainsaw-test.yaml" ] && [ ! -f "$root/$suite/chainsaw-test.yml" ]; then
            echo "leak-guard: allowlist names '$suite', which is not a suite directory under $root" >&2
            bad=1
        fi
    done <"$list"
    return "$bad"
}

# lg_check_suite SUITE START BASELINE_JSON CHAINSAW_RC ALLOWLIST
#
# Returns 1 when SUITE leaked and is not allowlisted, and 2 when the first
# reading after the suite could not be taken. That one fails loudly rather than
# going inconclusive: without it nothing about the suite was checked at all,
# whereas a read failing later in the wait follows a reading that did succeed.
# A suite whose chainsaw run failed is checked once without the wait and its
# leftovers are reported as a warning: it is red already, and its cleanup may be
# what failed.
lg_check_suite() {
    local suite=$1 start=$2 baseline=$3 crc=$4 allow=$5 ref leaks relist t0 deadline remaining line target out began werr="" timedout=0
    ref=$(lg_allowlisted "$suite" "$allow")
    t0=$(date +%s)
    deadline=$((t0 + LG_BUDGET))
    leaks=$(lg_leaks "$start" "$baseline") || {
        echo "::error title=leak-guard::suite=$suite the leak check could not read the cluster"
        return 2
    }
    # One wait per pod inside what is left of the budget, then a fresh list.
    # A pod that is already gone returns at once: kubectl's delete condition
    # reports done on NotFound, and a wait that runs out while watching reports
    # wait.ErrWaitTimeout, "timed out waiting for the condition"
    # (staging/src/k8s.io/kubectl/pkg/cmd/wait/delete.go and
    # staging/src/k8s.io/apimachinery/pkg/util/wait/error.go, Kubernetes
    # v1.33.0). That text is not the only way a timeout ends: RunWait puts the
    # whole --timeout on one context (wait.go, ContextWithOptionalTimeout), and
    # only the watch in IsDeleted maps its expiry to ErrWaitTimeout. If the
    # deadline passes during the resource lookup or the initial GET, which is
    # likeliest when little of the budget is left, kubectl reports the client's
    # "context deadline exceeded" instead. So a wait that fails after running
    # for its whole --timeout counts as a timeout whatever it printed; one that
    # fails sooner is an error. --timeout=0 would mean "check once, do not
    # wait" and fail with "condition not met", which is why a spent budget is
    # caught before calling kubectl rather than passed to it.
    # Three outcomes, nothing retried:
    #   - a timeout or a spent budget: the pod outlived the budget, and the last
    #     good list is the verdict, including when the re-list after it fails;
    #   - any other failure of a wait or of the re-list, before the budget is
    #     spent and before any timeout: the apiserver or the connection rather
    #     than the pod, so waiting stops and the result is inconclusive, because
    #     charging a leak on it would make the guard a source of the flaky reds
    #     it exists to attribute;
    #   - a failed first reading after the suite is handled above: unchecked,
    #     and the suite fails.
    if [ -n "$leaks" ] && [ -z "$ref" ] && [ "$crc" -eq 0 ]; then
        while [ -n "$leaks" ] && [ "$timedout" -eq 0 ] && [ -z "$werr" ]; do
            while IFS= read -r line; do
                target=${line#  }
                target=${target%% *}
                remaining=$((deadline - $(date +%s)))
                if [ "$remaining" -le 0 ]; then
                    timedout=1
                    break
                fi
                began=$(date +%s)
                if out=$(kubectl wait --for=delete "pod/${target#*/}" --namespace "${target%%/*}" \
                    --timeout="${remaining}s" 2>&1); then
                    continue
                fi
                if grep -q 'timed out waiting for the condition' <<<"$out" \
                    || [ $(($(date +%s) - began)) -ge "$remaining" ]; then
                    timedout=1
                else
                    # kubectl prints warnings ahead of the error on the same
                    # streams; the error is the last line that is not one.
                    werr=$(grep -v '^Warning:' <<<"$out" | tail -n 1)
                    [ -n "$werr" ] || werr="kubectl wait failed without a message"
                fi
                break
            done <<<"$leaks"
            [ -z "$werr" ] || break
            if relist=$(lg_leaks "$start" "$baseline"); then
                leaks=$relist
            elif [ "$timedout" -eq 0 ]; then
                werr="the re-list after the waits failed: the pod list or an owner could not be read"
            fi
        done
    fi
    if [ -z "$leaks" ]; then
        echo "leak-guard: suite=$suite clear after $(($(date +%s) - t0))s"
        return 0
    fi
    local count
    count=$(grep -c . <<<"$leaks")
    if [ -n "$ref" ]; then
        echo "::warning title=leaked-pods (allowlisted $ref)::suite=$suite count=$count, exempt by hack/e2e-chainsaw/_lib/leak-allowlist.txt"
        echo "LEAKED-PODS suite=$suite count=$count allowlisted=$ref"
        printf '%s\n' "$leaks"
        return 0
    fi
    # Waiting stopped on a failed read, so whether these pods would have gone
    # inside the budget is unknown. Report it, do not charge it. The error goes
    # last and quoted, since it carries spaces.
    if [ -n "$werr" ]; then
        echo "::warning title=leaked-pods (inconclusive)::suite=$suite count=$count stopped waiting on a failed read: $werr"
        echo "LEAKED-PODS suite=$suite count=$count inconclusive=\"$werr\""
        printf '%s\n' "$leaks"
        return 0
    fi
    # Checked once with no wait, so these pods may still be tearing down; the
    # suite is red on its own failure and this adds evidence, not a verdict.
    if [ "$crc" -ne 0 ]; then
        echo "::warning title=leaked-pods (suite already failed)::suite=$suite count=$count pods of this failed suite were present right after its cleanup; checked once, without the ${LG_BUDGET}s wait"
        echo "LEAKED-PODS suite=$suite checked-once=chainsaw-failed count=$count"
        printf '%s\n' "$leaks"
        return 0
    fi
    echo "::error title=leaked-pods::suite=$suite count=$count pods created by this suite outlived its cleanup by more than ${LG_BUDGET}s"
    echo "LEAKED-PODS suite=$suite budget=${LG_BUDGET}s count=$count"
    printf '%s\n' "$leaks"
    return 1
}

# lg_merge_reports DIR SUITE... — one JUnit document from the per-suite reports
# in DIR, in the shape chainsaw writes for a single run: a <testsuites> root
# carrying the summed counts, one <testsuite> per suite directory. A suite with
# no report of its own (chainsaw exited before writing one) and a suite that
# leaked each get a failed test case, so the report says what the job log says.
lg_merge_reports() {
    local dir=$1 suite key
    local -a files=()
    shift
    for suite in "$@"; do
        key=${suite//\//_}
        if [ ! -f "$dir/$key.xml" ]; then
            lg_synthetic_suite "$suite" "$suite" "chainsaw wrote no report for this suite" >"$dir/$key.xml"
        fi
        files+=("$dir/$key.xml")
        if [ -f "$dir/$key.unchecked" ]; then
            lg_synthetic_suite "$suite" leak-guard \
                "the leak guard could not read the cluster after this suite; see the leak-guard annotation for suite=$suite in the job log" >"$dir/$key.unchecked.xml"
            files+=("$dir/$key.unchecked.xml")
        fi
        if [ -f "$dir/$key.leaked" ]; then
            lg_synthetic_suite "$suite" leak-guard \
                "the leak guard failed this suite; see the leaked-pods lines for suite=$suite in the job log" >"$dir/$key.leaked.xml"
            files+=("$dir/$key.leaked.xml")
        fi
    done
    [ "${#files[@]}" -gt 0 ] || files=(/dev/null)
    # The root line is read for its counts and replaced by one summed root;
    # everything between it and the closing tag is carried through verbatim.
    awk '
        function attr(s, k) {
            if (match(s, " " k "=\"[^\"]*\"")) return substr(s, RSTART + length(k) + 3, RLENGTH - length(k) - 4)
            return 0
        }
        FNR == 1 {
            tests += attr($0, "tests"); errors += attr($0, "errors"); failures += attr($0, "failures")
            skipped += attr($0, "skipped"); time += attr($0, "time")
            next
        }
        /^<\/testsuites>$/ { next }
        { body = body $0 "\n" }
        END {
            printf "<testsuites name=\"chainsaw-report\" time=\"%.6f\"", time
            if (tests) printf " tests=\"%d\"", tests
            if (errors) printf " errors=\"%d\"", errors
            if (failures) printf " failures=\"%d\"", failures
            if (skipped) printf " skipped=\"%d\"", skipped
            printf ">\n%s</testsuites>\n", body
        }' "${files[@]}"
}

# lg_synthetic_suite SUITE CASE MESSAGE — a one-case failed report in chainsaw's shape.
lg_synthetic_suite() {
    printf '<testsuites name="chainsaw-report" time="0" tests="1" failures="1">\n'
    printf '  <testsuite name="%s" tests="1" failures="1" errors="0" id="0" time="">\n' "$1"
    printf '    <testcase name="%s" classname="" time="">\n' "$2"
    printf '      <failure message="%s"></failure>\n' "$3"
    printf '    </testcase>\n'
    printf '  </testsuite>\n'
    printf '</testsuites>\n'
}
