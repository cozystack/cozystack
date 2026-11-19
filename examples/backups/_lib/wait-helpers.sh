#!/bin/bash
# Wait helpers shared by every backup walkthrough under examples/backups/.
# Each walkthrough's 00-helpers.sh sources this file after defining the log_*
# functions these call; wait_hr_ready and wait_deleted read $NAMESPACE.
# hack/backup-examples-shared-helpers.bats fails if a walkthrough grows its own
# copy again.

# Wait until a JSONPath value on a resource matches the desired string.
#
# The optional 7th argument is a TERMINAL value to bail on: a BackupJob or
# RestoreJob that reaches Failed never reaches Succeeded, so polling out the
# remaining budget only delays the report, lets the TTL reaper take the Job's
# Pod log, and under Chainsaw risks the step being SIGKILLed at its op timeout
# instead of failing cleanly.
#
# kubectl's stderr is left attached: a read that fails for RBAC or transport
# reasons says so instead of reading as an empty value. --ignore-not-found
# keeps a resource that does not exist yet, the usual case at the start of a
# wait, from printing an error on every poll.
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"
    local fail_value="${7:-}"

    log_substep "Waiting for $resource_type/$resource_name $jsonpath to become '$desired'..."
    local elapsed=0
    local ns_flag=()
    [[ -n "$namespace" ]] && ns_flag=(-n "$namespace")

    while true; do
        local current
        current=$(kubectl get "$resource_type" "$resource_name" "${ns_flag[@]}" --ignore-not-found -o jsonpath="$jsonpath" || true)
        if [[ "$current" == "$desired" ]]; then
            log_success "$resource_type/$resource_name reached '$desired'"
            return 0
        fi
        if [[ -n "$fail_value" && "$current" == "$fail_value" ]]; then
            log_error "$resource_type/$resource_name reached terminal '$current' (expected '$desired')"
            return 1
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait for a HelmRelease to become Ready, with an existence backstop (the apps
# controller creates the HR asynchronously, so a bare `kubectl wait` right after
# `kubectl apply` races it) and a fail-fast on Stalled=True: a stalled HR has
# exhausted its remediation retries and will never turn Ready, so polling to the
# timeout only hides the real error.
wait_hr_ready() {
    local name="$1"
    local timeout="${2:-300}"
    local elapsed=0
    local lookup state seen=0

    log_substep "Waiting for HelmRelease/$name to become Ready..."
    while true; do
        # --ignore-not-found makes an absent release exit 0, so a non-zero exit
        # is kubectl failing to answer. Presence is read off the name line,
        # because a warning on stderr lands in the same string.
        if ! lookup=$(kubectl -n "$NAMESPACE" get hr "$name" --ignore-not-found -o name 2>&1); then
            state=error
        elif [[ $'\n'"$lookup"$'\n' != *"/$name"$'\n'* ]]; then
            state=absent
        else
            state=present
            seen=1
            local ready stalled
            ready=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' || true)
            if [[ "$ready" == "True" ]]; then
                log_success "HelmRelease/$name is Ready"
                return 0
            fi
            stalled=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' || true)
            if [[ "$stalled" == "True" ]]; then
                log_error "HelmRelease/$name is Stalled (terminal):"
                break
            fi
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for HelmRelease/$name to become Ready:"
            case "$state" in
                error)
                    echo "  could not look up HelmRelease/$name: $lookup" >&2
                    # Seen earlier: the reads below may answer where this lookup
                    # did not.
                    [[ $seen -eq 1 ]] || return 1
                    ;;
                absent)
                    if [[ $seen -eq 1 ]]; then
                        echo "  HelmRelease/$name was deleted from $NAMESPACE while waiting" >&2
                    else
                        echo "  HelmRelease/$name never appeared in $NAMESPACE" >&2
                    fi
                    return 1
                    ;;
            esac
            break
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    # Reached on Stalled or on timeout. Conditions and history rather than the
    # Ready message alone: a failed install is retried on an interval, so Ready
    # may describe the retry rather than the failure behind it.
    kubectl -n "$NAMESPACE" get hr "$name" \
        -o jsonpath='{range .status.conditions[*]}  {.type}={.status} ({.reason}): {.message}{"\n"}{end}' >&2 || true
    local hist
    if ! hist=$(kubectl -n "$NAMESPACE" get hr "$name" \
        -o jsonpath='{range .status.history[*]}  history: {.status} {.chartVersion} {.lastDeployed}{"\n"}{end}'); then
        echo "  history: kubectl could not read it" >&2
    elif [[ -n "${hist//[[:space:]]/}" ]]; then
        printf '%s\n' "$hist" >&2
    else
        # A release with no history prints nothing and exits 0; say so,
        # because a blank in a failure dump reads as a dump that broke.
        echo "  history: (none recorded)" >&2
    fi
    return 1
}

# Wait until a namespaced resource is really gone.
#
# `kubectl wait --for=delete` is not used: it errors out when the resource is
# already absent, which is the normal case on a clean namespace (cleanup.sh is
# idempotent and runs as a pre-clean too), and swallowing that error would also
# swallow a genuine failure. Polling `get --ignore-not-found` treats "already
# gone" and "gone now" as the same success (empty output, exit 0) while a
# retrieval failure — RBAC, API-server, transport — is a non-zero exit that
# must NOT be read as deletion, or cleanup settles on a false success and leaves
# half-uninstalled resources for the next run. Such an error just keeps us
# polling until the resource is confirmed gone or the timeout fires.
wait_deleted() {
    local resource_type="$1"
    local resource_name="$2"
    local timeout="${3:-300}"
    local elapsed=0
    local got

    while true; do
        if got=$(kubectl -n "$NAMESPACE" get "$resource_type" "$resource_name" --ignore-not-found 2>/dev/null) && [[ -z "$got" ]]; then
            [[ $elapsed -gt 0 ]] && log_success "$resource_type/$resource_name is gone"
            return 0
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name to be deleted; it is still present after ${timeout}s:"
            kubectl -n "$NAMESPACE" get "$resource_type" "$resource_name" -o wide >&2 || true
            return 1
        fi
        [[ $elapsed -eq 0 ]] && log_substep "Waiting for $resource_type/$resource_name to be deleted..."
        sleep 5
        elapsed=$((elapsed + 5))
    done
}
