#!/bin/bash
# Step 01: Create the cluster-scoped FoundationDB backup strategy. The
# strategy drives the FoundationDB Kubernetes operator's native
# FoundationDBBackup CRD (continuous backup_agent deployment to a blob
# store). The driver flips backupState=Stopped on prior backups, creates a
# new FoundationDBBackup per Cozystack BackupJob, and waits for the first
# full snapshot to land before stamping the Cozystack Backup artefact.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 01: Create FoundationDB backup strategy '${STRATEGY_NAME}'"
log_command "kubectl apply -f - (FoundationDB strategy: $STRATEGY_NAME)"

# The strategy renders:
#   - blobStoreConfiguration: AccountName/Bucket templated from BackupClass
#     parameters; BackupName is left empty so the driver picks the
#     BackupJob name as the per-run S3 path segment.
#   - urlParameters: secure_connection=0 because seaweedfs-s3 (cozystack's
#     default S3) does not require TLS handshake from in-cluster clients
#     when reached over the cluster service. region is sourced from
#     parameters.
#   - customParameters: --blob_credentials=<path> points the backup_agent
#     at a JSON file mounted into the backup deployment by the
#     PodTemplateSpec below.
#   - backupDeploymentPodTemplateSpec: mounts <app>-fdb-backup-creds as the
#     blob_credentials.json file, and an optional ca-cert volume for
#     self-signed S3 endpoints. The Secret name is templated against the
#     application object so one strategy works for every FoundationDB
#     instance in a tenant.
kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: FoundationDB
metadata:
  name: ${STRATEGY_NAME}
spec:
  template:
    blobStoreConfiguration:
      accountName: "{{ .Parameters.accountName }}"
      bucket: "{{ .Parameters.bucket }}"
      # BackupName left empty: driver fills with the BackupJob name so each
      # Cozystack BackupJob owns a discrete S3 directory.
      urlParameters:
        - "secure_connection={{ .Parameters.secureConnection | default \"0\" }}"
        - "region={{ .Parameters.region | default \"us-east-1\" }}"
    snapshotPeriodSeconds: 3600
    customParameters:
      - "--blob_credentials=/var/fdb-blob-credentials/blob_credentials.json"
    backupDeploymentPodTemplateSpec:
      spec:
        containers:
          - name: foundationdb
            volumeMounts:
              - name: blob-credentials
                mountPath: /var/fdb-blob-credentials
                readOnly: true
            resources:
              requests:
                cpu: 100m
                memory: 128Mi
              limits:
                cpu: 200m
                memory: 256Mi
            # backup_agent does not require root: it reads
            # blob_credentials.json from the projected Secret mount and
            # talks to the cluster file in /var/dynamic-conf and S3
            # over the network. Run as UID 4059 to match the main
            # FoundationDB process (packages/apps/foundationdb/values.yaml
            # securityContext.runAsUser=4059) so the agent uses the same
            # non-root identity as the rest of the cluster.
            securityContext:
              runAsUser: 4059
              runAsGroup: 4059
        volumes:
          - name: blob-credentials
            secret:
              secretName: "{{ .Application.metadata.name }}-fdb-backup-creds"
              items:
                - key: blob_credentials.json
                  path: blob_credentials.json
EOF

log_success "FoundationDB strategy '${STRATEGY_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-bucket.sh"
