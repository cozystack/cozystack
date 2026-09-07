#!/usr/bin/env bats
# Unit coverage for the OIDC lifecycle folded into kubernetes-latest.
# Sourcing run-kubernetes.sh only defines functions; every kubectl interaction
# below is replaced with a shell stub, so this file needs no cluster.

@test "HelmRelease upgrade wait rejects stale Ready and stale observedGeneration" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    state_file=$(mktemp)
    printf '0\n' > "$state_file"
    kubectl() {
        count=$(sed -n '1p' "$state_file")
        count=$(( count + 1 ))
        printf '%s\n' "$count" > "$state_file"
        case "$count" in
            1) printf '7|7|True|' ;;
            2) printf '8|7|True|' ;;
            *) printf '8|8|True|' ;;
        esac
    }
    sleep() { :; }

    cozy_wait_helmrelease_upgrade tenant-test kubernetes-demo 7 5 >/dev/null
    [ "$(sed -n '1p' "$state_file")" -eq 3 ] || {
        echo "the wait accepted Ready before both generation gates advanced" >&2
        exit 1
    }
}

@test "HelmRelease upgrade wait fails immediately on Stalled" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    calls=$(mktemp)
    kubectl() {
        printf '%s\n' "$*" >> "$calls"
        case "$*" in
            *" get helmrelease "*) printf '8|8|False|True' ;;
            *" describe helmrelease "*) return 0 ;;
            *) return 1 ;;
        esac
    }
    sleep() { echo "sleep must not run after Stalled=True" >&2; return 1; }

    if cozy_wait_helmrelease_upgrade tenant-test kubernetes-demo 7 600 >/dev/null 2>&1; then
        echo "a Stalled HelmRelease was accepted" >&2
        exit 1
    fi
    [ "$(wc -l < "$calls")" -eq 2 ] || {
        echo "the Stalled path did not stop after status plus diagnostics" >&2
        cat "$calls" >&2
        exit 1
    }
}

@test "System OIDC assertion proves rendered objects bootstrap kubeconfig and tenant RBAC" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl() {
        case "$*" in
            *" wait job kubernetes-demo-oidc-bootstrap "*) return 0 ;;
            *" get kamajicontrolplane kubernetes-demo "*)
                printf '[--authentication-config=/etc/kubernetes/authentication-config/config.yaml]'
                ;;
            *" get secret kubernetes-demo-oidc-authn-config "*)
                printf 'url: https://keycloak.example.test/realms/cozy\naudiences:\n- tenant-test-kubernetes-demo\n' | base64 | tr -d '\n'
                ;;
            *" get keycloakclient.v1.edp.epam.com tenant-test-kubernetes-demo "*) printf 'true' ;;
            *" get keycloakclientscope.v1.edp.epam.com tenant-test-kubernetes-demo-audience "*) printf 'oidc-audience-mapper' ;;
            *" get secret kubernetes-demo-oidc-kubeconfig "*)
                printf 'args:\n- oidc-login\n- --oidc-client-id=tenant-test-kubernetes-demo\n' | base64 | tr -d '\n'
                ;;
            *) echo "unexpected kubectl call: $*" >&2; return 1 ;;
        esac
    }
    cozy_oidc_bindings() {
        printf 'e2e-admin@example.test\tcluster-admin\ne2e-viewer@example.test\tview\n'
    }

    cozy_assert_oidc_system demo
}

@test "CustomConfig transition fails when it cannot ask whether System leftovers are gone" {
    # The teardown probes used `if kubectl get ... >/dev/null 2>&1`, which reads
    # every non-zero exit as "the object is gone" -- including an RBAC denial, a
    # timeout or a missing CRD -- so the suite reported a clean System teardown
    # for never having observed one. --ignore-not-found separates the two.
    #
    # One probe is broken per iteration and the other two answer "absent", so
    # each of the three is pinned individually: reverting any single one of them
    # to the bare form lets that iteration pass and reddens this test. A test
    # that merely broke all three would pass on any one surviving probe.
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    cozy_wait_helmrelease_upgrade() { :; }
    cozy_oidc_bindings() { printf 'byo-admin@example.test\tcluster-admin\n'; }

    for unaskable in \
        'keycloakclient.v1.edp.epam.com tenant-test-kubernetes-demo' \
        'keycloakclientscope.v1.edp.epam.com tenant-test-kubernetes-demo-audience' \
        'secret kubernetes-demo-oidc-kubeconfig'; do
        COZY_TEST_UNASKABLE="$unaskable"
        kubectl() {
            case "$*" in
                *" get helmrelease kubernetes-demo "*) printf '7' ;;
                *" patch kuberneteses.apps.cozystack.io demo "*) : ;;
                *" wait job kubernetes-demo-oidc-bootstrap "*) return 0 ;;
                *" get kamajicontrolplane kubernetes-demo "*)
                    printf '[--authentication-config=/etc/kubernetes/authentication-config/config.yaml]'
                    ;;
                *" get secret kubernetes-demo-oidc-authn-config "*)
                    printf 'url: https://idp.byo.example.test\naudiences:\n- cozystack-byo-demo\n' | base64 | tr -d '\n'
                    ;;
                *"${COZY_TEST_UNASKABLE}"*--ignore-not-found*)
                    # Cannot ask: an RBAC denial or an unreachable apiserver.
                    return 1
                    ;;
                # Absent, the way kubectl reports it with the flag: exit 0, no output.
                *--ignore-not-found*) : ;;
                *) echo "unexpected kubectl call: $*" >&2; return 1 ;;
            esac
        }

        if cozy_switch_and_assert_oidc_custom_config demo; then
            echo "the transition passed while the probe for ${unaskable} could not answer" >&2
            return 1
        fi
    done
}

@test "CustomConfig transition patches the live cluster waits for the new generation and rejects System leftovers" {
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    calls=$(mktemp)
    wait_args=$(mktemp)
    patch_body=$(mktemp)
    kubectl() {
        printf '%s\n' "$*" >> "$calls"
        case "$*" in
            *" get helmrelease kubernetes-demo "*) printf '7' ;;
            *" patch kuberneteses.apps.cozystack.io demo "*)
                previous=
                for argument in "$@"; do
                    if [ "$previous" = --patch ]; then
                        printf '%s\n' "$argument" > "$patch_body"
                        break
                    fi
                    previous="$argument"
                done
                ;;
            *" wait job kubernetes-demo-oidc-bootstrap "*) return 0 ;;
            *" get kamajicontrolplane kubernetes-demo "*)
                printf '[--authentication-config=/etc/kubernetes/authentication-config/config.yaml]'
                ;;
            *" get secret kubernetes-demo-oidc-authn-config "*)
                printf 'url: https://idp.byo.example.test\naudiences:\n- cozystack-byo-demo\n' | base64 | tr -d '\n'
                ;;
            # Absence the way kubectl reports it under --ignore-not-found: exit 0
            # and no output. The fail-closed half is pinned by the next test,
            # not by this one -- here a bare probe would read the catch-all's
            # non-zero exit as absence and pass.
            *" get keycloakclient.v1.edp.epam.com tenant-test-kubernetes-demo --ignore-not-found"*) : ;;
            *" get keycloakclientscope.v1.edp.epam.com tenant-test-kubernetes-demo-audience --ignore-not-found"*) : ;;
            *" get secret kubernetes-demo-oidc-kubeconfig --ignore-not-found"*) : ;;
            *) echo "unexpected kubectl call: $*" >&2; return 1 ;;
        esac
    }
    cozy_wait_helmrelease_upgrade() { printf '%s\n' "$*" > "$wait_args"; }
    cozy_oidc_bindings() { printf 'byo-admin@example.test\tcluster-admin\n'; }

    cozy_switch_and_assert_oidc_custom_config demo
    [ "$(sed -n '1p' "$wait_args")" = 'tenant-test kubernetes-demo 7 600' ] || {
        echo "the transition did not wait from the pre-patch generation" >&2
        cat "$wait_args" >&2
        exit 1
    }
    jq -e '.spec.oidc.mode == "CustomConfig"' "$patch_body" >/dev/null
    jq -e '.spec.oidc.users == [{"email":"byo-admin@example.test","role":"admin"}]' "$patch_body" >/dev/null
    jq -er '.spec.oidc.customConfig.config' "$patch_body" | grep -q 'https://idp.byo.example.test'
    jq -er '.spec.oidc.customConfig.config' "$patch_body" | grep -q 'cozystack-byo-demo'
}
