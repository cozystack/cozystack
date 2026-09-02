#!/usr/bin/env bats
# Unit coverage for the single post-install HelmRelease readiness gate.

helmrelease_snapshot() {
    count="$1"
    not_ready="${2:-0}"
    stalled="${3:-0}"
    stale="${4:-0}"
    jq -nc --argjson count "$count" --argjson notReady "$not_ready" --argjson stalled "$stalled" --argjson stale "$stale" '
      {items: [range(1; $count + 1) as $i |
        {metadata: {namespace: "ns", name: ("hr-" + ($i | tostring)), generation: 2},
         status: {observedGeneration: (if $i == $stale then 1 else 2 end), conditions:
           ([{type: "Ready", status: (if $i == $notReady then "False" else "True" end), reason: "Progressing", message: "working"}]
            + (if $i == $stalled then [{type: "Stalled", status: "True", message: "terminal failure"}] else [] end))}}
      ]}
    '
}

@test "stale Ready stays closed until the current generation is observed" {
    . hack/e2e-wait-helmreleases.sh
    clock=$(mktemp)
    printf '0\n' > "$clock"
    date() { sed -n '1p' "$clock"; }
    sleep() {
        now=$(sed -n '1p' "$clock")
        printf '%s\n' "$(( now + $1 ))" > "$clock"
    }
    kubectl() {
        now=$(sed -n '1p' "$clock")
        if [ "$now" -lt 4 ]; then
            helmrelease_snapshot 11 0 0 3
        else
            helmrelease_snapshot 11
        fi
    }

    cozy_wait_all_helmreleases_ready 30 11 4 2 >/dev/null
    [ "$(sed -n '1p' "$clock")" -ge 8 ] || {
        echo "the gate accepted Ready=True from an unobserved generation" >&2
        exit 1
    }
}

@test "a late HelmRelease resets stability and is included before success" {
    . hack/e2e-wait-helmreleases.sh
    clock=$(mktemp)
    calls=$(mktemp)
    printf '0\n' > "$clock"
    date() { sed -n '1p' "$clock"; }
    sleep() {
        now=$(sed -n '1p' "$clock")
        printf '%s\n' "$(( now + $1 ))" > "$clock"
    }
    kubectl() {
        printf '%s\n' "$*" >> "$calls"
        now=$(sed -n '1p' "$clock")
        if [ "$now" -lt 2 ]; then
            helmrelease_snapshot 11
        elif [ "$now" -lt 4 ]; then
            helmrelease_snapshot 12 12
        else
            helmrelease_snapshot 12
        fi
    }

    cozy_wait_all_helmreleases_ready 30 11 5 2 >/dev/null
    [ "$(sed -n '1p' "$clock")" -ge 10 ] || {
        echo "the gate returned before the late release became Ready and stabilized" >&2
        exit 1
    }
    [ "$(grep -c -- '-o json' "$calls")" -ge 6 ]
}

@test "Stalled HelmRelease fails immediately without sleeping to timeout" {
    . hack/e2e-wait-helmreleases.sh
    calls=$(mktemp)
    date() { printf '100\n'; }
    sleep() { echo "sleep must not run after Stalled=True" >&2; return 1; }
    kubectl() {
        printf '%s\n' "$*" >> "$calls"
        helmrelease_snapshot 11 3 3
    }

    if cozy_wait_all_helmreleases_ready 900 11 5 2 >/dev/null 2>&1; then
        echo "a terminal Stalled HelmRelease passed readiness" >&2
        exit 1
    fi
    [ "$(wc -l < "$calls")" -eq 1 ] || {
        echo "the Stalled path kept polling instead of failing immediately" >&2
        exit 1
    }
}

@test "a transient list error keeps the gate closed" {
    . hack/e2e-wait-helmreleases.sh
    clock=$(mktemp)
    calls=$(mktemp)
    printf '0\n' > "$clock"
    date() { sed -n '1p' "$clock"; }
    sleep() {
        now=$(sed -n '1p' "$clock")
        printf '%s\n' "$(( now + $1 ))" > "$clock"
    }
    kubectl() {
        count=$(wc -l < "$calls")
        printf '%s\n' "$*" >> "$calls"
        if [ "$count" -eq 0 ]; then
            echo 'temporary apiserver error' >&2
            return 1
        fi
        helmrelease_snapshot 11
    }

    cozy_wait_all_helmreleases_ready 30 11 4 2 >/dev/null 2>&1
    [ "$(sed -n '1p' "$clock")" -ge 6 ] || {
        echo "the list error was mistaken for an all-Ready snapshot" >&2
        exit 1
    }
}

@test "install suite has one unmasked HelmRelease gate" {
    [ "$(grep -c 'cozy_wait_all_helmreleases_ready 900 11 5 2' hack/e2e-install-cozystack.bats)" -eq 1 ]
    if grep -q 'kubectl wait hr --all -A.*|| true' hack/e2e-install-cozystack.bats; then
        echo "the old masked HelmRelease wait survived" >&2
        exit 1
    fi
}
