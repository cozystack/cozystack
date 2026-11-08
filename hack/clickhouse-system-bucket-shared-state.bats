#!/usr/bin/env bats
# The clickhouse-3-system-bucket-roundtrip Test edits the host CoreDNS Corefile
# and must put it back. Its restore runs from an EXIT trap under `set +e`, so a
# restore that fails without saying so leaves the rewrite in a cluster-wide
# object and still reports a green run; the next run then refuses to start.
# These run the restore against a stubbed kubectl.

load test_helper

LIB=hack/e2e-chainsaw/clickhouse/system-bucket-shared-state.sh
HELPERS=examples/backups/clickhouse/00-helpers.sh

setup() {
    strict_setup
    STUB=$(mktemp -d)
    # A Corefile carrying the Test's marked block, as the run step leaves it.
    jq -n '{apiVersion: "v1", kind: "ConfigMap",
            metadata: {name: "coredns", namespace: "kube-system", resourceVersion: "7"},
            data: {Corefile: ".:53 {\n    # === BEGIN e2e-clickhouse-system-bucket ===\n    rewrite name exact s3.example.org root-ingress-controller.tenant-root.svc.cozy.local\n    # === END e2e-clickhouse-system-bucket ===\n    errors\n}\n"}}' \
        >"$STUB/coredns.json"
    # The secret lookup follows kubectl's contract: a name together with a
    # selector is refused, and a selector-only lookup prints the names in
    # OWNED_SECRETS, the Secrets carrying the owner label.
    cat >"$STUB/kubectl" <<'EOF'
#!/bin/bash
echo "$*" >>"$STUB/calls"
case "$*" in
    *"get configmap coredns -o json"*) cat "$STUB/coredns.json" ;;
    replace*)
        cp "${@: -1}" "$STUB/replaced.json"
        if [ -n "${REPLACE_FAILS:-}" ]; then
            echo 'Error from server (Conflict): Operation cannot be fulfilled on configmaps "coredns": the object has been modified' >&2
            exit 1
        fi
        ;;
    *"get secret"*)
        # Positionals are "get", "secret" and then any names.
        words=0 selector=
        while [ $# -gt 0 ]; do
            case "$1" in
                -l|--selector) selector=$2; shift 2 ;;
                -n|--namespace|-o|--output) shift 2 ;;
                -*) shift ;;
                *) words=$((words + 1)); shift ;;
            esac
        done
        if [ -n "${LOOKUP_FAILS:-}" ]; then
            echo 'Unable to connect to the server: dial tcp: i/o timeout' >&2
            exit 1
        fi
        if [ -n "$selector" ] && [ "$words" -gt 2 ]; then
            echo 'error: name cannot be provided when a selector is specified' >&2
            exit 1
        fi
        [ "$selector" = cozystack.io/e2e-owner=clickhouse-system-bucket ] || exit 0
        for s in ${OWNED_SECRETS:-}; do echo "secret/$s"; done
        ;;
    *"delete secret "*) ;;
    *) echo "unexpected kubectl $*" >&2; exit 2 ;;
esac
EOF
    chmod +x "$STUB/kubectl"
    export STUB
}

restore() {
    PATH="$STUB:$PATH" NAMESPACE=tenant-root bash -c '
        set -euo pipefail
        source "$1"
        source "$2"
        set +e
        restore_shared_state
    ' _ "$HELPERS" "$LIB"
}

@test "a CoreDNS restore the API server refuses fails the restore" {
    export REPLACE_FAILS=1
    err=$(mktemp)
    if restore 2>"$err"; then
        echo "restore_shared_state returned 0 although kubectl replace was refused" >&2
        cat "$err" >&2
        false
    fi
    if grep -q 'Removed the s3.example.org rewrite' "$err"; then
        echo "restore_shared_state reported the rewrite removed although kubectl replace was refused:" >&2
        cat "$err" >&2
        false
    fi
}

@test "a CoreDNS restore sends the Corefile back without the marked block" {
    restore
    jq -e '.data.Corefile == ".:53 {\n    errors\n}\n"' "$STUB/replaced.json" >/dev/null || {
        echo "restored Corefile:" >&2
        jq -r '.data.Corefile' "$STUB/replaced.json" >&2
        false
    }
    # The resourceVersion read travels with the replace, so a concurrent
    # writer makes it fail rather than be overwritten.
    [ "$(jq -r '.metadata.resourceVersion' "$STUB/replaced.json")" = 7 ]
}

@test "the restore deletes the Ingress TLS Secret the Test created" {
    OWNED_SECRETS=seaweedfs-s3-ingress-tls restore
    grep -qE '^-n tenant-root delete secret seaweedfs-s3-ingress-tls( |$)' "$STUB/calls" || {
        echo "restore_shared_state did not delete the Secret carrying the owner label; kubectl calls:" >&2
        cat "$STUB/calls" >&2
        false
    }
}

@test "the restore leaves an Ingress TLS Secret without the owner label alone" {
    for owned in '' e2e-other seaweedfs-s3-ingress-tls-old; do
        : >"$STUB/calls"
        OWNED_SECRETS=$owned restore
        if grep -q 'delete secret' "$STUB/calls"; then
            echo "restore_shared_state deleted a Secret although the labelled ones are '$owned'; kubectl calls:" >&2
            cat "$STUB/calls" >&2
            false
        fi
    done
}

@test "a Secret lookup that fails fails the restore" {
    err=$(mktemp)
    if LOOKUP_FAILS=1 restore 2>"$err"; then
        echo "restore_shared_state returned 0 although it could not tell whether the Secret is left over" >&2
        cat "$err" >&2
        false
    fi
}

@test "the run step's finally restores the shared objects" {
    # When the step ceiling fires, Chainsaw kills the shell and the EXIT trap
    # of system-bucket-roundtrip.sh never runs; the step's finally still does.
    # Chainsaw runs a script's content with sh from the suite's directory.
    suite=hack/e2e-chainsaw/clickhouse
    finally=$(yq 'select(.metadata.name == "clickhouse-3-system-bucket-roundtrip")
        | .spec.steps[] | select(.name == "run") | .finally[]
        | select(.description == "restore the host CoreDNS and the S3 Ingress TLS Secret") | .script.content' \
        "$suite/chainsaw-test.yaml")
    [ -n "$finally" ] || { echo "the run step has no finally restoring the shared objects" >&2; false; }
    (cd "$suite" && PATH="$STUB:$PATH" NAMESPACE=tenant-root sh -c "$finally")
    [ -f "$STUB/replaced.json" ] || {
        echo "the run step's finally did not put the Corefile back" >&2
        false
    }
    jq -e '.data.Corefile == ".:53 {\n    errors\n}\n"' "$STUB/replaced.json" >/dev/null
}

# The Altinity driver names the Backup after the BackupJob, and a Backup left
# behind under that name records an earlier run's archive, which fails the next
# run's BackupJob. It is reclaimed by name, whether or not the BackupJob
# recorded it in status.backupRef.
@test "the round-trip reclaims its Backup by name before and after the run" {
    suite=hack/e2e-chainsaw/clickhouse/chainsaw-test.yaml
    test='select(.metadata.name == "clickhouse-3-system-bucket-roundtrip") | .spec.steps[]'
    calls="$STUB/calls"
    # Nothing is left over from an earlier run, and no BackupJob recorded a
    # backupRef.
    cat >"$STUB/kubectl" <<'STUBEOF'
#!/bin/bash
echo "$*" >>"$STUB/calls"
case "$*" in
    *" get secret "*) exit 1 ;;
esac
STUBEOF
    for selector in \
        'select(.name == "preconditions") | .try[].script.content' \
        'select(.name == "run") | .finally[] | select(.description == "system-bucket round-trip cleanup") | .script.content'; do
        : >"$calls"
        script=$(yq "$test | $selector" "$suite")
        [ -n "$script" ] || { echo "no script at: $selector" >&2; false; }
        PATH="$STUB:$PATH" NAMESPACE=tenant-root sh -c "$script"
        grep -qE '^-n tenant-root delete backup sysbucket-backup( |$)' "$calls" || {
            echo "the script at '$selector' does not delete Backup sysbucket-backup by name; kubectl calls:" >&2
            cat "$calls" >&2
            false
        }
    done
}

# cert-manager issues the Secret of a Certificate that still exists again as
# soon as it is deleted, so the Certificate has to go first or its Secret
# outlives the run.
@test "the round-trip cleanup deletes the Certificate before its Secret" {
    script=$(yq 'select(.metadata.name == "clickhouse-3-system-bucket-roundtrip") | .spec.steps[]
        | select(.name == "run") | .finally[]
        | select(.description == "system-bucket round-trip cleanup") | .script.content' \
        hack/e2e-chainsaw/clickhouse/chainsaw-test.yaml)
    [ -n "$script" ] || { echo "the run step has no system-bucket round-trip cleanup" >&2; false; }
    cat >"$STUB/kubectl" <<'STUBEOF'
#!/bin/bash
echo "$*" >>"$STUB/calls"
STUBEOF
    PATH="$STUB:$PATH" NAMESPACE=tenant-root sh -c "$script"
    cert=$(grep -nE '^-n tenant-root delete certificate e2e-system-bucket-s3( |$)' "$STUB/calls" | cut -d: -f1)
    secret=$(grep -nE '^-n tenant-root delete secret e2e-system-bucket-s3-tls( |$)' "$STUB/calls" | cut -d: -f1)
    [ -n "$cert" ] && [ -n "$secret" ] && [ "$cert" -lt "$secret" ] || {
        echo "the cleanup does not delete Certificate e2e-system-bucket-s3 before Secret e2e-system-bucket-s3-tls; kubectl calls:" >&2
        cat "$STUB/calls" >&2
        false
    }
}
