#!/bin/bash
# Backup and in-place restore of the `sysbucket` ClickHouse through the
# cozy-default BackupClass, against the platform system bucket. Run from the
# repository root by the clickhouse-3-system-bucket-roundtrip Test, which has
# already started the release with no cozy-backups-creds in its namespace.
#
# The system bucket is addressed as https://s3.example.org, the S3 Ingress host
# the COSI driver advertises. In CI that name does not resolve and its ACME
# certificate is never issued, so for the duration of this script, and only of
# this script, two shared objects are changed and the EXIT trap below puts both
# back:
#
#   - the S3 Ingress TLS Secret seaweedfs-s3-ingress-tls is created from the
#     Test's own certificate (SeaweedFS CA, s3.example.org). It relies on
#     cert-manager leaving a Secret's data alone while the Certificate that
#     names it has no completed issuance, verified with cert-manager v1.20.2:
#     a fresh ACME order would replace this certificate, and ingress-nginx
#     would then serve one the sidecar does not trust, which fails the probe
#     below rather than the backup.
#   - the host CoreDNS Corefile gets one marked `rewrite name exact` line for
#     s3.example.org. The host CoreDNS is Talos-managed and imports no snippet
#     files, so the block goes into the live Corefile the way ouroboros writes
#     its own; ouroboros is off in CI and no other suite uses this name.
set -euo pipefail

source examples/backups/clickhouse/00-helpers.sh
source hack/e2e-chainsaw/clickhouse/system-bucket-shared-state.sh

: "${NAMESPACE:?}"
CLICKHOUSE_NAME=sysbucket
BACKUPJOB_NAME=sysbucket-backup
RESTOREJOB_NAME=sysbucket-restore
CERT_SECRET=e2e-system-bucket-s3-tls
PROBE_POD=e2e-system-bucket-probe
DNS_TARGET="root-ingress-controller.${NAMESPACE}.svc.cozy.local"
# Kubelet refreshes a ConfigMap volume within about a minute and CoreDNS's
# reload plugin polls every 30s; the rest is the 30s answer cache.
PROBE_TIMEOUT=240
# Each covers the driver's 2m hold after the credentials change plus the
# strategy Job itself.
JOB_TIMEOUT=480

workdir=$(mktemp -d)

on_exit() {
    local rc=$?
    set +e
    restore_shared_state || rc=1
    rm -rf "$workdir"
    exit "$rc"
}

if kubectl -n "$NAMESPACE" get secret "$INGRESS_SECRET" -o name >/dev/null 2>&1; then
    log_error "Secret ${NAMESPACE}/${INGRESS_SECRET} already exists; this Test serves its own certificate under that name and will not overwrite one it did not create"
    exit 1
fi
if corefile_has_block; then
    log_error "kube-system/coredns already carries the ${S3_HOST} rewrite block, left by an earlier run; remove it by hand"
    exit 1
fi
trap on_exit EXIT

log_step "Serving the Test certificate for ${S3_HOST} through the platform S3 Ingress"
kubectl -n "$NAMESPACE" get secret "$CERT_SECRET" -o json \
    | jq --arg name "$INGRESS_SECRET" '{apiVersion, kind, type,
        metadata: {name: $name, labels: {"cozystack.io/e2e-owner": "clickhouse-system-bucket"}},
        data: {"tls.crt": .data["tls.crt"], "tls.key": .data["tls.key"], "ca.crt": .data["ca.crt"]}}' \
    | kubectl -n "$NAMESPACE" create -f -

log_step "Rewriting ${S3_HOST} to ${DNS_TARGET} in the host CoreDNS"
kubectl -n kube-system get configmap coredns -o json >"$workdir/coredns.json"
jq -e '.data.Corefile | startswith(".:53 {\n")' "$workdir/coredns.json" >/dev/null \
    || { log_error "kube-system/coredns Corefile does not open with '.:53 {'; refusing to edit it"; exit 1; }
# The resourceVersion read above travels with the replace, so a concurrent
# writer makes it fail instead of being overwritten.
jq --arg block "${DNS_BEGIN}"$'\n'"    rewrite name exact ${S3_HOST} ${DNS_TARGET}"$'\n'"${DNS_END}" \
    '.data.Corefile |= sub("^\\.:53 \\{\n"; ".:53 {\n" + $block + "\n")' \
    "$workdir/coredns.json" >"$workdir/coredns-new.json"
kubectl replace -f "$workdir/coredns-new.json" >/dev/null

log_step "Checking that ${S3_HOST} serves the Test certificate from inside ${NAMESPACE}"
kubectl -n "$NAMESPACE" get secret "$CERT_SECRET" -o jsonpath='{.data.ca\.crt}' | base64 -d >"$workdir/ca.crt"
want=$(kubectl -n "$NAMESPACE" get secret "$CERT_SECRET" -o jsonpath='{.data.tls\.crt}' | base64 -d \
    | openssl x509 -noout -fingerprint -sha256)
deadline=$((SECONDS + PROBE_TIMEOUT))
while true; do
    # --cacert makes curl verify both the chain and the host name; %{certs}
    # prints the chain it was served, leaf first.
    if served=$(timeout 30 kubectl -n "$NAMESPACE" exec -i "$PROBE_POD" -- sh -c \
            "cat >/tmp/ca.crt && curl -sS -o /dev/null --max-time 10 --cacert /tmp/ca.crt -w '%{certs}' https://${S3_HOST}/" \
            <"$workdir/ca.crt" 2>"$workdir/probe.err"); then
        got=$(printf '%s\n' "$served" | sed -n '/BEGIN CERTIFICATE/,/END CERTIFICATE/p;/END CERTIFICATE/q' \
            | openssl x509 -noout -fingerprint -sha256)
        if [ "$got" = "$want" ]; then
            log_success "${S3_HOST} resolves in ${NAMESPACE} and serves the Test certificate"
            break
        fi
        echo "served ${got}, want ${want}" >"$workdir/probe.err"
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
        log_error "${S3_HOST} did not serve the Test certificate within ${PROBE_TIMEOUT}s: $(cat "$workdir/probe.err")"
        kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' >&2 || true
        kubectl -n "$NAMESPACE" describe ingress ingress-seaweedfs-s3 >&2 || true
        exit 1
    fi
    # TODO(e2e-replace-fixed-timeouts): no condition reports that the host
    # CoreDNS answers with the rewrite and the Ingress serves the Test
    # certificate, as seen from this namespace; poll until one does.
    sleep 10
done

# A restarted Pod reads its credentials afresh at start, which is what the
# old environment-based sidecar needed; the round-trip proves the fix only if
# the sidecar that started without them is the one that serves both jobs.
sidecar_identity() {
    kubectl -n "$NAMESPACE" get pod "chi-clickhouse-${CLICKHOUSE_NAME}-clickhouse-0-0-0" \
        -o jsonpath='{.metadata.uid} {.status.containerStatuses[?(@.name=="clickhouse-backup")].restartCount}'
}
sidecar_before=$(sidecar_identity)
[[ "$sidecar_before" =~ ^[0-9a-f-]+\ [0-9]+$ ]] \
    || { log_error "could not read the ClickHouse Pod uid and clickhouse-backup restart count (got '${sidecar_before}')"; exit 1; }

# Restore picks the newest remote backup of the release, so only a row no
# earlier run wrote shows that the archive restored is this run's.
sentinel="before-backup-$(date -u +%Y%m%dT%H%M%SZ)-${RANDOM}"
log_step "Writing the sentinel row ${sentinel}"
clickhouse_query "$CLICKHOUSE_NAME" "CREATE TABLE IF NOT EXISTS default.sentinel (id UInt32, name String) ENGINE = MergeTree ORDER BY id"
clickhouse_query "$CLICKHOUSE_NAME" "INSERT INTO default.sentinel VALUES (1, '${sentinel}')"

log_step "Backing up through cozy-default"
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${BACKUPJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: ClickHouse
    name: ${CLICKHOUSE_NAME}
  backupClassName: cozy-default
EOF
wait_for_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" "$JOB_TIMEOUT" Failed
backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
[ -n "$backup_ref" ] || { log_error "BackupJob ${BACKUPJOB_NAME} succeeded with an empty backupRef"; exit 1; }

# The sidecar started before this Secret existed; a backup that succeeded
# against it is the behaviour under test, and the key it read is this one.
endpoint=$(kubectl -n "$NAMESPACE" get secret cozy-backups-creds -o jsonpath='{.data.clickhouse-backup\.yml}' | base64 -d \
    | sed -n 's/^ *endpoint: *//p')
[ "$endpoint" = "https://${S3_HOST}" ] \
    || { log_error "cozy-backups-creds clickhouse-backup.yml endpoint is '${endpoint}', expected https://${S3_HOST}"; exit 1; }

log_step "Restoring in place through cozy-default"
clickhouse_query "$CLICKHOUSE_NAME" "DROP TABLE IF EXISTS default.sentinel SYNC"
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${backup_ref}
EOF
wait_for_field restorejob "$RESTOREJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" "$JOB_TIMEOUT" Failed

count=$(clickhouse_query "$CLICKHOUSE_NAME" "SELECT count() FROM default.sentinel WHERE name = '${sentinel}'" | tr -d '[:space:]')
[ "$count" = 1 ] || { log_error "after restore default.sentinel holds ${count} row(s) named ${sentinel}; expected 1"; exit 1; }
sidecar_after=$(sidecar_identity)
[ "$sidecar_after" = "$sidecar_before" ] \
    || { log_error "the ClickHouse Pod or its clickhouse-backup container restarted during the round-trip (uid restarts: '${sidecar_before}' then '${sidecar_after}')"; exit 1; }
log_success "System-bucket round-trip verified: sentinel row ${sentinel} restored"
