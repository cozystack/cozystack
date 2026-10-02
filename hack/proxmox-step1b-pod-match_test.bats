#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Step 1b of the proxmox pre-delete hook deletes the Pods that hold each tenant
# claim, so the claims' pvc-protection finalizer can clear and the CSI driver
# frees the disks before Step 2 removes it. A render test cannot see how the
# loop behaves, because it never runs kubectl. These tests do: they render the
# hook, cut the Pod loop out of it, and run it against a stub kubectl whose
# `get pods` answers with the listing the loop asks for.
#
# The case that matters is a namespace where one Pod mounts two claims. A
# JSONPath filter on the claim name fails there for every claim in the
# namespace ("can only compare one element at a time"), kubectl prints nothing,
# and no Pod is deleted; the listing-plus-shell-match form does not.
# -----------------------------------------------------------------------------

# hack/cozytest.sh runs @test bodies as POSIX sh functions and knows no
# setup/teardown, so each test calls prepare_loop and removes $work itself.
prepare_loop() {
    work=$(mktemp -d)
    cat > "$work/vals.yaml" <<'VALS'
substrate: proxmox
_namespace:
  etcd: etcd-test
proxmox:
  dnsServers: ["8.8.8.8"]
  ipv4Config:
    addresses: ["10.0.0.160-10.0.0.170"]
    gateway: "10.0.0.1"
    prefix: 24
  ccm:
    credentialsSecretName: proxmox-ccm-creds
  csi:
    enabled: true
    credentialsSecretName: proxmox-csi-creds
VALS
    helm template test-k8s packages/apps/kubernetes -n tenant-test \
        -f packages/apps/kubernetes/tests/values-ci.yaml -f "$work/vals.yaml" \
        --show-only templates/delete.yaml > "$work/hook.yaml"
    # The Job's script, as the container receives it.
    yq -r 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' \
        "$work/hook.yaml" > "$work/script.sh"
    # The Pod loop alone: from the second `echo "$pvcs" | while` to the `done`
    # that closes it at the same indentation.
    awk '
        /echo "\$pvcs" \| while read -r pns pname pv; do/ { n++; if (n == 2) { grab = 1; indent = match($0, /[^ ]/) } }
        grab { print }
        grab && /^[ ]*done[ ]*$/ && match($0, /[^ ]/) == indent { exit }
    ' "$work/script.sh" > "$work/loop.sh"
    [ -s "$work/loop.sh" ] || { echo "could not cut the Pod loop out of the hook" >&2; exit 1; }

    mkdir -p "$work/bin"
    # Stub kubectl, faithful to client-go's JSONPath for the two forms the loop
    # can use. `get pods` with the per-Pod listing prints one line per Pod: its
    # name, then the claims it mounts. `get pods` with a claim-name filter does
    # what kubectl does: as soon as any Pod mounts more than one claim, the
    # filter's left side has several values and the whole expression fails with
    # "can only compare one element at a time", printing nothing. `delete pod`
    # records the Pod. Nothing else may be called from this loop.
    cat > "$work/bin/kubectl" <<'STUB'
#!/bin/sh
case " $* " in
  *" get pods "*)
    case "$*" in
      *'[?(@.spec.volumes'*)
        if awk 'NF > 2 { found = 1 } END { exit !found }' "$PODS"; then
          echo "error: error executing jsonpath: can only compare one element at a time" >&2
          exit 1
        fi
        claim=$(printf '%s' "$*" | sed -n "s/.*claimName=='\([^']*\)'.*/\1/p")
        awk -v c="$claim" '$2 == c { print $1 }' "$PODS" ;;
      *) cat "$PODS" ;;
    esac ;;
  *" delete pod "*) prev=; for a in "$@"; do [ "$prev" = pod ] && echo "$a" >> "$DELETED"; prev=$a; done ;;
  *) echo "unexpected kubectl call: $*" >&2; exit 3 ;;
esac
STUB
    chmod +x "$work/bin/kubectl"
    export PATH="$work/bin:$PATH" PODS="$work/pods" DELETED="$work/deleted"
    : > "$DELETED"
    printf '%s\n' "db-0 data-b wal-b" "web-0 data-a" "cfg-0  " > "$PODS"
}


run_loop_for() {
    sh -c 'kubeconfig=/dev/null; pvcs=$1; . "$2"' _ "$1" "$work/loop.sh"
}

@test "deletes the Pod that mounts two claims, for either claim" {
    prepare_loop
    run_loop_for "app wal-b pv-1"
    got=$(cat "$DELETED"); rm -rf "$work"
    [ "$got" = "db-0" ]
}

@test "deletes a one-claim Pod in a namespace where another Pod mounts two" {
    prepare_loop
    run_loop_for "app data-a pv-2"
    got=$(cat "$DELETED"); rm -rf "$work"
    [ "$got" = "web-0" ]
}

@test "matches whole claim names only" {
    prepare_loop
    run_loop_for "app data pv-3"
    got=$(cat "$DELETED"); rm -rf "$work"
    [ -z "$got" ]
}

@test "leaves a Pod that mounts no claim alone" {
    prepare_loop
    run_loop_for "app data-b pv-4"
    got=$(cat "$DELETED"); rm -rf "$work"
    [ "$got" = "db-0" ]
}
