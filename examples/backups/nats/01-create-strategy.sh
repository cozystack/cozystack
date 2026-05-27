#!/bin/bash
# Step 01: Create the cluster-scoped generic Job backup strategy for NATS
# JetStream. Unlike the app-specific drivers (Altinity, CNPG, ...), the Job
# strategy runs an arbitrary Pod the operator supplies. Here that Pod is a
# stock natsio/nats-box image (which carries the `nats` CLI plus curl and tar)
# running a single shell script that branches on `.Mode`:
#   backup : `nats stream backup` -> tar -> PUT the tarball to S3
#   restore: GET the tarball from S3 -> untar -> `nats stream restore`
#
# Everything the template injects is funnelled through container env vars so
# the script body stays plain shell. Note how the same PodTemplateSpec serves
# both directions: the engine templates each string leaf independently (it
# cannot add/remove containers per mode), so the *one* image branches at
# runtime on $MODE.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 01: Create Job backup strategy '${STRATEGY_NAME}'"
log_command "kubectl apply -f - (Job strategy: $STRATEGY_NAME)"

kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Job
metadata:
  name: ${STRATEGY_NAME}
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: nats-backup
          image: ${NATS_BOX_IMAGE}
          imagePullPolicy: IfNotPresent
          env:
            # .Parameters carry the static knobs from the BackupClass: which
            # JetStream stream to back up and which NATS user to authenticate
            # as. On restore they are read back from the Backup's
            # driverMetadata, so the same values apply round-trip.
            - name: STREAM
              value: '{{ default "ORDERS" (index .Parameters "stream") }}'
            - name: NATS_USER
              value: '{{ default "backup" (index .Parameters "natsUser") }}'
            # .Release is the application being acted on: the source on backup,
            # the restore target on restore (in-place=source, to-copy=the new
            # app). The NATS app exposes its client Service as <name>.<ns>.svc
            # and stores per-user passwords in <name>-credentials.
            - name: NATS_HOST
              value: "{{ .Release.Name }}.{{ .Release.Namespace }}.svc"
            - name: NATS_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-credentials"
                  key: '{{ default "backup" (index .Parameters "natsUser") }}'
            # S3 object key is scoped by the SOURCE app name so a to-copy
            # restore reads what the source wrote. On backup the source is
            # .Release.Name; on restore it is .Backup.ApplicationRef.Name
            # (.Backup is only set on restore - the guard keeps backup mode
            # from rendering "<no value>" into an unused var).
            - name: SRC_BACKUP
              value: "{{ .Release.Name }}"
            - name: SRC_RESTORE
              value: "{{ if .Backup }}{{ .Backup.ApplicationRef.Name }}{{ end }}"
            - name: MODE
              value: "{{ .Mode }}"
            # S3 coordinates from the tenant-provided <release>-backup-s3 Secret
            # (created by create_s3_secret in 00-helpers.sh from the Bucket).
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-backup-s3"
                  key: accessKey
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-backup-s3"
                  key: secretKey
            - name: S3_ENDPOINT
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-backup-s3"
                  key: endpoint
            - name: S3_REGION
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-backup-s3"
                  key: region
            - name: S3_BUCKET
              valueFrom:
                secretKeyRef:
                  name: "{{ .Release.Name }}-backup-s3"
                  key: bucket
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              NATS_URL="nats://\${NATS_USER}:\${NATS_PASSWORD}@\${NATS_HOST}:4222"

              # BucketInfo endpoints are bare host:port; COSI/seaweedfs serves
              # S3 over HTTPS, so default to https:// when no scheme is given.
              case "\${S3_ENDPOINT}" in
                http://*|https://*) BASE="\${S3_ENDPOINT}" ;;
                *) BASE="https://\${S3_ENDPOINT}" ;;
              esac

              if [ "\${MODE}" = backup ]; then SRC="\${SRC_BACKUP}"; else SRC="\${SRC_RESTORE}"; fi
              KEY="\${SRC}/\${STREAM}.tar"
              OBJ_URL="\${BASE}/\${S3_BUCKET}/\${KEY}"

              # curl --aws-sigv4 signs the request (SigV4) so no separate S3
              # client image is needed. -k accepts seaweedfs's internal
              # self-signed cert; a production strategy would mount the tenant
              # CA and drop -k.
              s3() { curl -fsS -k --aws-sigv4 "aws:amz:\${S3_REGION}:s3" --user "\${AWS_ACCESS_KEY_ID}:\${AWS_SECRET_ACCESS_KEY}" "\$@"; }

              rm -rf /tmp/bk && mkdir -p /tmp/bk
              if [ "\${MODE}" = backup ]; then
                echo "backing up JetStream stream \${STREAM} from \${NATS_HOST}"
                nats --server "\${NATS_URL}" stream backup "\${STREAM}" /tmp/bk
                tar -C /tmp/bk -cf /tmp/backup.tar .
                s3 -X PUT --upload-file /tmp/backup.tar "\${OBJ_URL}"
                echo "uploaded s3://\${S3_BUCKET}/\${KEY}"
              else
                echo "restoring JetStream stream \${STREAM} into \${NATS_HOST} from s3://\${S3_BUCKET}/\${KEY}"
                s3 -o /tmp/backup.tar "\${OBJ_URL}"
                tar -C /tmp/bk -xf /tmp/backup.tar
                nats --server "\${NATS_URL}" stream restore /tmp/bk
                echo "restore complete"
              fi
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 200m
              memory: 128Mi
          securityContext:
            # Minimal hardening, matching the ClickHouse example. nats-box runs
            # the script as its default user; tenants enforcing PSA "restricted"
            # should add runAsNonRoot/runAsUser for the image they pin.
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
EOF

log_success "Job strategy '${STRATEGY_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-backupclass.sh"
