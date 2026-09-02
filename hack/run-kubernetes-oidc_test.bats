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
            *" get keycloakclient.v1.edp.epam.com tenant-test-kubernetes-demo") return 1 ;;
            *" get keycloakclientscope.v1.edp.epam.com tenant-test-kubernetes-demo-audience") return 1 ;;
            *" get secret kubernetes-demo-oidc-kubeconfig") return 1 ;;
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
