#!/bin/bash
# The two shared objects system-bucket-roundtrip.sh changes for the duration
# of its run, and how to put them back. Sourced by that script, whose EXIT trap
# restores them, and by the finally of its Chainsaw step, which restores them
# when the step ceiling killed the script before the trap could run. Needs
# NAMESPACE and log_success/log_error from examples/backups/clickhouse/00-helpers.sh.

S3_HOST=s3.example.org
INGRESS_SECRET=seaweedfs-s3-ingress-tls
OWNER_LABEL=cozystack.io/e2e-owner=clickhouse-system-bucket
DNS_BEGIN='    # === BEGIN e2e-clickhouse-system-bucket ==='
DNS_END='    # === END e2e-clickhouse-system-bucket ==='

corefile_has_block() {
    kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' | grep -qF -- "$DNS_BEGIN"
}

remove_dns_block() {
    local dir
    dir=$(mktemp -d)
    kubectl -n kube-system get configmap coredns -o json >"$dir/coredns.json" || { rm -rf "$dir"; return 1; }
    local begins
    begins=$(jq -r '.data.Corefile' "$dir/coredns.json" | grep -cF -- "$DNS_BEGIN" || true)
    if [ "$begins" = 0 ]; then
        rm -rf "$dir"
        return 0
    fi
    if [ "$begins" != 1 ] || ! jq -r '.data.Corefile' "$dir/coredns.json" | grep -qF -- "$DNS_END"; then
        log_error "kube-system/coredns holds ${begins} '${DNS_BEGIN# *}' markers or no END marker; reconcile the Corefile by hand"
        rm -rf "$dir"
        return 1
    fi
    jq --arg b "$DNS_BEGIN" --arg e "$DNS_END" \
        '.data.Corefile |= (split("\n") | (index($b)) as $i | (index($e)) as $j | .[:$i] + .[$j+1:] | join("\n"))' \
        "$dir/coredns.json" >"$dir/coredns-clean.json"
    # Callers run this with errexit off, so a refused replace has to return.
    if ! kubectl replace -f "$dir/coredns-clean.json" >/dev/null; then
        log_error "could not remove the ${S3_HOST} rewrite from kube-system/coredns; remove the marked block by hand"
        rm -rf "$dir"
        return 1
    fi
    log_success "Removed the ${S3_HOST} rewrite from kube-system/coredns"
    rm -rf "$dir"
}

# Returns non-zero when either object could not be put back.
restore_shared_state() {
    local rc=0
    remove_dns_block || rc=1
    if kubectl -n "$NAMESPACE" get secret "$INGRESS_SECRET" -l "$OWNER_LABEL" -o name 2>/dev/null | grep -q .; then
        kubectl -n "$NAMESPACE" delete secret "$INGRESS_SECRET" --wait=true --timeout=60s || rc=1
    fi
    return "$rc"
}
