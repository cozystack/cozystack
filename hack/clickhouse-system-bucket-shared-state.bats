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
    cat >"$STUB/kubectl" <<'EOF'
#!/bin/bash
case "$*" in
    *"get configmap coredns -o json"*) cat "$STUB/coredns.json" ;;
    replace*)
        cp "${@: -1}" "$STUB/replaced.json"
        if [ -n "${REPLACE_FAILS:-}" ]; then
            echo 'Error from server (Conflict): Operation cannot be fulfilled on configmaps "coredns": the object has been modified' >&2
            exit 1
        fi
        ;;
    *"get secret"*) ;;
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

# The Altinity driver names the Backup after the BackupJob and adopts one that
# already exists under that name, so a Backup left behind would stand in for
# the next run's backup. It is reclaimed by name, whether or not the BackupJob
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
