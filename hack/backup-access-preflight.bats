#!/usr/bin/env bats
# Unit tests for the E2E-only S3 access gate that runs before expensive backup
# workloads. All cluster and S3 calls are shell stubs.

@test "preflight helper remains sourceable by the POSIX test runner" {
    helper=hack/e2e-chainsaw/_lib/backup-access-preflight.sh
    [ "$(sed -n '1p' "$helper")" = '#!/bin/sh' ]
    if grep -nE '^[[:space:]]*(set[[:space:]].*pipefail|local([[:space:]]|$))' "$helper"; then
        echo "backup preflight is sourced by /bin/sh and must not contain Bash-only shell state" >&2
        exit 1
    fi
    sh -n "$helper"
}

@test "preflight fails closed when the BucketInfo Secret cannot be read" {
    . hack/e2e-chainsaw/_lib/backup-access-preflight.sh
    out=$(mktemp)
    kubectl() {
        case "$*" in
            *" get secret bucket-demo-backup "*) return 1 ;;
            *) echo "preflight continued after the Secret read failed: $*" >&2; return 1 ;;
        esac
    }

    if cozy_backup_access_preflight tenant-test bucket-demo-backup 0 >"$out" 2>&1; then
        echo "failed BucketInfo Secret read passed the S3 preflight" >&2
        exit 1
    fi
    grep -q 'failed to read BucketInfo Secret tenant-test/bucket-demo-backup' "$out"
    if grep -q 'lacks access key' "$out"; then
        echo "preflight continued into credential parsing after the Secret read failed" >&2
        exit 1
    fi
}

@test "preflight retries IAM convergence then proves PUT GET compare and DELETE" {
    . hack/e2e-chainsaw/_lib/backup-access-preflight.sh
    attempts=$(mktemp)
    uploaded_source=$(mktemp)
    calls=$(mktemp)
    printf '0\n' > "$attempts"

    kubectl() {
        printf 'kubectl %s\n' "$*" >> "$calls"
        case "$*" in
            *" get secret bucket-demo-backup "*)
                printf '%s' '{"spec":{"secretS3":{"accessKeyID":"access","accessSecretKey":"secret"},"bucketName":"bucket-real"}}' | base64 | tr -d '\n'
                ;;
            *" port-forward "*)
                while :; do command sleep 1; done
                ;;
            *) echo "unexpected kubectl call: $*" >&2; return 1 ;;
        esac
    }
    timeout() {
        shift
        if [ "$1" = sh ]; then
            return 0
        fi
        "$@"
    }
    sleep() { :; }
    mc() {
        printf 'mc %s\n' "$*" >> "$calls"
        case "$1" in
            alias) return 0 ;;
            cp)
                case "$3" in
                    backup-preflight/*)
                        command cp "$(sed -n '1p' "$uploaded_source")" "$4"
                        ;;
                    *)
                        count=$(sed -n '1p' "$attempts")
                        count=$(( count + 1 ))
                        printf '%s\n' "$count" > "$attempts"
                        if [ "$count" -eq 1 ]; then
                            echo 'AccessDenied: IAM identity not loaded yet' >&2
                            return 1
                        fi
                        printf '%s\n' "$3" > "$uploaded_source"
                        ;;
                esac
                ;;
            rm) return 0 ;;
            *) echo "unexpected mc call: $*" >&2; return 1 ;;
        esac
    }

    cozy_backup_access_preflight tenant-test bucket-demo-backup 5 >/dev/null
    [ "$(sed -n '1p' "$attempts")" -eq 2 ] || {
        echo "preflight did not retry the not-yet-loaded IAM identity" >&2
        cat "$calls" >&2
        exit 1
    }
    [ "$(grep -c '^mc rm ' "$calls")" -eq 1 ] || {
        echo "preflight did not delete its successfully verified object exactly once" >&2
        cat "$calls" >&2
        exit 1
    }
}

@test "preflight fails before backup work when granted credentials never reach S3" {
    . hack/e2e-chainsaw/_lib/backup-access-preflight.sh
    out=$(mktemp)
    kubectl() {
        case "$*" in
            *" get secret bucket-demo-backup "*)
                printf '%s' '{"spec":{"secretS3":{"accessKeyID":"access","accessSecretKey":"secret"},"bucketName":"bucket-real"}}' | base64 | tr -d '\n'
                ;;
            *" port-forward "*) while :; do command sleep 1; done ;;
            *) return 1 ;;
        esac
    }
    timeout() {
        shift
        if [ "$1" = sh ]; then return 0; fi
        "$@"
    }
    mc() {
        case "$1" in
            alias) return 0 ;;
            cp) echo 'AccessDenied' >&2; return 1 ;;
            *) return 0 ;;
        esac
    }

    if cozy_backup_access_preflight tenant-test bucket-demo-backup 0 >"$out" 2>&1; then
        echo "unusable credentials passed the S3 preflight" >&2
        exit 1
    fi
    grep -q 'granted but did not pass S3 PUT/GET/DELETE within 0s' "$out"
    grep -q 's3-preflight: AccessDenied' "$out"
}

@test "all active database backup flows gate immediately after accessGranted" {
    for file in \
        examples/backups/postgres/run-all.sh \
        examples/backups/mariadb/run-all.sh \
        examples/backups/mongodb/run-all.sh \
        examples/backups/clickhouse/03-create-bucket.sh; do
        granted=$(grep -n 'accessGranted' "$file" | head -n 1 | cut -d: -f1)
        preflight=$(grep -n 'cozy_backup_access_preflight' "$file" | head -n 1 | cut -d: -f1)
        coordinates=$(grep -n 'Reading bucket coordinates' "$file" | head -n 1 | cut -d: -f1)
        if [ -z "$granted" ] || [ -z "$preflight" ] || [ -z "$coordinates" ] \
            || [ "$granted" -ge "$preflight" ] || [ "$preflight" -ge "$coordinates" ]; then
            echo "$file must run the S3 preflight after accessGranted and before consuming bucket coordinates" >&2
            exit 1
        fi
    done
}

@test "all four Chainsaw backup roundtrips enable the E2E preflight" {
    for file in \
        hack/e2e-chainsaw/postgres/chainsaw-test.yaml \
        hack/e2e-chainsaw/mariadb/chainsaw-test.yaml \
        hack/e2e-chainsaw/mongodb/chainsaw-test.yaml \
        hack/e2e-chainsaw/clickhouse/chainsaw-test.yaml; do
        grep -q 'COZY_E2E_BACKUP_PREFLIGHT=1' "$file" || {
            echo "$file does not enable the backup access preflight" >&2
            exit 1
        }
    done
}
