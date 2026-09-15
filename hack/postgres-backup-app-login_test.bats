#!/usr/bin/env bats
# Unit tests for the app-login helpers the postgres backup example uses to prove
# the restored copy authenticates. The bug they pin: the credentials Secret is
# named after the release (== the CNPG cluster, `postgres-<name>`), not the short
# app name, so resolving it from anything but the cluster reads a Secret that
# does not exist and the login can never pass. Others: a genuine failure names
# the Secret on timeout; a login printing '1' is a success even with stderr
# noise; and a username with a dot reads its password (jq, not jsonpath).
#
# The helper reads the Secret with `get secret ... -o json | jq`, so the kubectl
# stub returns JSON. 00-helpers.sh is `#!/bin/bash` (it uses arrays), so it is
# exercised in a bash subshell rather than sourced into this runner, which is
# /bin/sh (dash in CI). It is sourced first, then the stubs override its
# cluster-touching functions.

@test "psql_app_exec reads the credentials Secret named after the cluster, not the app name" {
    calls=$(mktemp)
    CALLS="$calls" bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            printf "kubectl %s\n" "$*" >> "$CALLS"
            case "$*" in
                *"get secret postgres-pg-target-credentials -o json"*) printf "%s" "{\"data\":{\"app\":\"cGFzcw==\"}}" ;;
                *"get secret "*) echo "WRONG secret queried: $*" >&2; return 1 ;;
                *" exec "*) echo 1 ;;
                *) return 0 ;;
            esac
        }
        psql_app_exec postgres-pg-target app demo "SELECT 1;" >/dev/null
    '
    grep -q 'get secret postgres-pg-target-credentials -o json' "$calls"
    if grep -q 'get secret pg-target-credentials ' "$calls"; then
        echo "resolved the Secret from the app name, not the cluster" >&2
        exit 1
    fi
}

@test "wait_for_app_login names the failing Secret on timeout instead of swallowing it" {
    out=$(mktemp)
    bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            case "$*" in
                # Secret exists but has no password for app; the helper log_errors
                # naming it, and the wait loop must surface that on timeout.
                *"get secret postgres-pg-target-credentials -o json"*) printf "%s" "{\"data\":{}}" ;;
                *) return 0 ;;
            esac
        }
        wait_for_app_login postgres-pg-target app demo 0
    ' >"$out" 2>&1 && { echo "wait_for_app_login unexpectedly passed with no password" >&2; exit 1; }
    grep -q "no password for user 'app' in postgres-pg-target-credentials" "$out"
}

@test "psql_app_exec reads the password for a username containing a dot" {
    calls=$(mktemp)
    CALLS="$calls" bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            printf "kubectl %s\n" "$*" >> "$CALLS"
            case "$*" in
                # A dotted key is why jsonpath fails and jq is used; return the
                # Secret as JSON so jq can index the literal "app.user" key.
                *"get secret postgres-pg-target-credentials -o json"*) printf "%s" "{\"data\":{\"app.user\":\"cGFzcw==\"}}" ;;
                *" exec "*) echo 1 ;;
                *) return 0 ;;
            esac
        }
        psql_app_exec postgres-pg-target app.user demo "SELECT 1;" >/dev/null
    '
    grep -q 'get secret postgres-pg-target-credentials -o json' "$calls"
}

@test "wait_for_app_login treats a login that prints '1' as success even with stderr noise" {
    bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            case "$*" in
                *"get secret postgres-pg-target-credentials -o json"*) printf "%s" "{\"data\":{\"app\":\"cGFzcw==\"}}" ;;
                # A successful SELECT 1 prints 1 on stdout; a NOTICE on stderr
                # must not be read as a failure (success is stdout-only).
                *" exec "*) echo 1; echo "NOTICE: something" >&2 ;;
                *) return 0 ;;
            esac
        }
        wait_for_app_login postgres-pg-target app demo 5
    ' >/dev/null 2>&1 || { echo "a successful login was misread as a failure because of stderr noise" >&2; exit 1; }
}
