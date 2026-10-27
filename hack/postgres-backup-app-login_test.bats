#!/usr/bin/env bats
# Unit tests for the app-login helpers the postgres backup example uses to prove
# the restored copy authenticates. The password comes from a mint on the
# account's Credential, named <release>.<account> after the release (== the CNPG
# cluster, `postgres-<name>`), not the short app name: resolving it from anything
# but the cluster mints on a credential that does not exist, and the login can
# never pass. Others: a genuine failure names the credential on timeout; a login
# printing '1' is a success even with stderr noise; a username with a dot maps to
# its account name; and a run mints once, since a second mint replaces the
# password the first one returned.
#
# 00-helpers.sh is `#!/bin/bash` (it uses arrays and process substitution), so it
# is exercised in a bash subshell rather than sourced into this runner, which is
# /bin/sh (dash in CI). It is sourced first, then the stubs override its
# cluster-touching functions. Each test gets its own TMPDIR, where the helper
# caches the minted password.

load test_helper

MINT=/apis/core.cozystack.io/v1alpha1/namespaces/tenant-test/credentials

@test "psql_app_exec mints on the credential named after the cluster, not the app name" {
    calls=$(mktemp)
    CALLS="$calls" MINT="$MINT" TMPDIR=$(mktemp -d) bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            printf "kubectl %s\n" "$*" >> "$CALLS"
            case "$*" in
                *"create --raw $MINT/postgres-pg-target.app/mint"*) printf "%s" "{\"status\":{\"password\":\"pass\"}}" ;;
                *"create --raw "*) echo "WRONG credential minted: $*" >&2; return 1 ;;
                *" exec "*) echo 1 ;;
                *) return 0 ;;
            esac
        }
        psql_app_exec postgres-pg-target app demo "SELECT 1;" >/dev/null
    '
    grep -q "create --raw $MINT/postgres-pg-target.app/mint" "$calls"
    if grep -q "credentials/pg-target.app/" "$calls"; then
        echo "resolved the credential from the app name, not the cluster" >&2
        exit 1
    fi
}

@test "wait_for_app_login names the failing credential on timeout instead of swallowing it" {
    out=$(mktemp)
    TMPDIR=$(mktemp -d) bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            case "$*" in
                # The mint answers without a password; the helper log_errors
                # naming the user, and the wait loop must surface that on timeout.
                *"create --raw "*) printf "%s" "{\"status\":{}}" ;;
                *) return 0 ;;
            esac
        }
        wait_for_app_login postgres-pg-target app demo 0
    ' >"$out" 2>&1 && { echo "wait_for_app_login unexpectedly passed with no password" >&2; exit 1; }
    grep -q "no password minted for user 'app' of postgres-pg-target" "$out"
}

@test "psql_app_exec maps a username containing a dot to its account name" {
    calls=$(mktemp)
    CALLS="$calls" MINT="$MINT" TMPDIR=$(mktemp -d) bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            printf "kubectl %s\n" "$*" >> "$CALLS"
            case "$*" in
                *"create --raw $MINT/postgres-pg-target.app-user/mint"*) printf "%s" "{\"status\":{\"password\":\"pass\"}}" ;;
                *" exec "*) echo 1 ;;
                *) return 0 ;;
            esac
        }
        psql_app_exec postgres-pg-target app.User demo "SELECT 1;" >/dev/null
    '
    grep -q "create --raw $MINT/postgres-pg-target.app-user/mint" "$calls"
}

@test "psql_app_exec mints once per run" {
    calls=$(mktemp)
    CALLS="$calls" TMPDIR=$(mktemp -d) bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            printf "kubectl %s\n" "$*" >> "$CALLS"
            case "$*" in
                *"create --raw "*) printf "%s" "{\"status\":{\"password\":\"pass\"}}" ;;
                *" exec "*) echo 1 ;;
                *) return 0 ;;
            esac
        }
        psql_app_exec postgres-pg-target app demo "SELECT 1;" >/dev/null
        psql_app_exec postgres-pg-target app demo "SELECT 1;" >/dev/null
    '
    [ "$(grep -c 'create --raw ' "$calls")" -eq 1 ]
}

@test "wait_for_app_login treats a login that prints '1' as success even with stderr noise" {
    TMPDIR=$(mktemp -d) bash -uc '
        NAMESPACE=tenant-test
        . examples/backups/postgres/00-helpers.sh
        cnpg_primary_pod() { echo "${1}-1"; }
        kubectl() {
            case "$*" in
                *"create --raw "*) printf "%s" "{\"status\":{\"password\":\"pass\"}}" ;;
                # A successful SELECT 1 prints 1 on stdout; a NOTICE on stderr
                # must not be read as a failure (success is stdout-only).
                *" exec "*) echo 1; echo "NOTICE: something" >&2 ;;
                *) return 0 ;;
            esac
        }
        wait_for_app_login postgres-pg-target app demo 5
    ' >/dev/null 2>&1 || { echo "a successful login was misread as a failure because of stderr noise" >&2; exit 1; }
}
