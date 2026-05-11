#!/bin/bash
# Step 01: Create the cluster-scoped Altinity backup strategy. The strategy
# drives Altinity's clickhouse-backup HTTP API exposed by a sidecar inside
# every chi-* Pod (see packages/apps/clickhouse/templates/clickhouse.yaml).
# The strategy Pod itself is a tiny curl/jq client that POSTs to the sidecar
# and polls the action log.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 01: Create Altinity backup strategy"
log_command "kubectl apply -f - (Altinity strategy: $STRATEGY_NAME)"

kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Altinity
metadata:
  name: ${STRATEGY_NAME}
spec:
  jobTemplate:
    spec:
      restartPolicy: Never
      containers:
        - name: ch-backup-client
          image: alpine:3.19
          imagePullPolicy: IfNotPresent
          # API_USERNAME / API_PASSWORD pulled from the chart-emitted
          # backup-api-auth Secret in the application's namespace. The
          # sidecar rejects unauthenticated requests; without these env
          # vars every curl below would 401. Secret name follows the
          # actual Helm release shape (clickhouse-rd prefixes the user-
          # facing app name with "clickhouse-").
          env:
            - name: API_USERNAME
              valueFrom:
                secretKeyRef:
                  name: clickhouse-{{ .Release.Name }}-backup-api-auth
                  key: username
            - name: API_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: clickhouse-{{ .Release.Name }}-backup-api-auth
                  key: password
          command: ["/bin/sh","-c"]
          args:
            - |
              set -euo pipefail
              apk add --no-cache curl jq >/dev/null

              host="chi-clickhouse-{{ .Release.Name }}-clickhouse-0-0.{{ .Release.Namespace }}.svc"
              api="http://\${host}:7171"
              # \${API_USERNAME} / \${API_PASSWORD} come from the
              # backup-api-auth Secret mounted via env (see above).
              auth="-u \${API_USERNAME}:\${API_PASSWORD}"

              # Sidecar may still be coming up when the strategy Pod fires.
              # Block on /backup/status until it answers (max ~5 minutes).
              for _ in \$(seq 1 60); do
                if curl -fsS \${auth} --max-time 2 "\${api}/backup/status" >/dev/null 2>&1; then break; fi
                sleep 5
              done

              # Backup names embed the source release prefix. The sidecar's
              # S3_PATH already scopes list/remote to this release's prefix
              # inside the bucket; the prefix on the name is defence in
              # depth - a misconfigured tenant sharing S3_PATH across
              # releases (or a to-copy restore where s3PathOverride pins
              # the destination at the source's prefix) still picks the
              # right archive.
              #
              # Backup mode: .Release.Name is the source app, names use
              # that prefix. Restore mode: .Release.Name is the *target*
              # app (in-place=source, to-copy=destination), but we filter
              # by the *source* via .Backup.ApplicationRef.Name so to-copy
              # also lands on the correct archive.
              if [ "{{ .Mode }}" = "backup" ]; then
                prefix="clickhouse-{{ .Release.Name }}-"
                target="\${prefix}\$(date -u +%Y%m%dT%H%M%SZ)"
                echo "create_remote \${target}"
                curl -fsS \${auth} -X POST "\${api}/backup/create_remote?name=\${target}" >/dev/null
              else
                prefix="clickhouse-{{ .Backup.ApplicationRef.Name }}-"
                # Pick the latest archive whose name starts with the
                # source-release prefix. Falls through to "no remote
                # backup found" if S3_PATH is misconfigured AND none of
                # the listed names match.
                target=\$(curl -fsS \${auth} "\${api}/backup/list/remote" \\
                  | jq -rs --arg prefix "\${prefix}" \\
                      '[.[] | select((.name // "") | startswith(\$prefix))] | sort_by(.created // "") | last.name // ""')
                if [ -z "\${target}" ] || [ "\${target}" = "null" ]; then
                  echo "no remote backup found" >&2
                  exit 1
                fi
                echo "restore_remote \${target}"
                # Caller is responsible for dropping the target table(s)
                # before invoking this strategy in restore mode (see
                # 06-restore-in-place.sh).
                # The clickhouse-backup HTTP API surfaces restore_remote as
                # POST /backup/restore_remote/{name} (path-encoded). The
                # query-string form returns 404. Omitting ?schema and ?data
                # restores both schema and data (the default); pass them
                # only to restrict the operation. This script does not pass
                # ?rm: callers are expected to drop conflicting tables
                # themselves (see 06-restore-in-place.sh, which DROPs the
                # sentinel before submitting the RestoreJob).
                curl -fsS \${auth} -X POST "\${api}/backup/restore_remote/\${target}" >/dev/null
              fi

              # Poll the action log. clickhouse-backup emits NDJSON; we match
              # on the backup name suffix because the embedded flags vary
              # between operations (e.g. "restore_remote --schema --data X"
              # vs. "create_remote X"). The strategy Pod runs one operation
              # at a time, so the most recent matching action is ours.
              while true; do
                row=\$(curl -fsS \${auth} "\${api}/backup/actions" \\
                  | jq -rs --arg suffix "\${target}" \\
                      '[.[] | select((.command // "") | endswith(\$suffix))] | sort_by(.start) | last')
                status=\$(echo "\${row}" | jq -r '.status // ""')
                case "\${status}" in
                  success) echo "operation succeeded" ; exit 0 ;;
                  error)
                    err=\$(echo "\${row}" | jq -r '.error // "unknown"')
                    echo "operation failed: \${err}" >&2
                    exit 1
                    ;;
                esac
                sleep 5
              done
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 200m
              memory: 128Mi
          securityContext:
            # Pod runs apk add at startup; that needs root. Other fields stay
            # locked down (no privilege escalation, no extra caps, default
            # seccomp profile). Tenants that enforce PSA "restricted" should
            # swap this image for one with curl+jq pre-baked and re-enable
            # runAsNonRoot on the strategy CR.
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
EOF

log_success "Altinity strategy '${STRATEGY_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-backupclass.sh"
