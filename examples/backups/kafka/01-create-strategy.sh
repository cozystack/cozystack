#!/bin/bash
# Step 01: Create the cluster-scoped generic Job backup strategy for Kafka
# topic data. Unlike the app-specific drivers (Altinity, CNPG, ...), the Job
# strategy runs an arbitrary Pod the operator supplies. Here that Pod is a
# stock Strimzi Kafka image (which carries the kafka-*.sh CLI plus bash, curl
# and tar) running a single shell script that branches on `.Mode`:
#   backup : freeze each partition's end offset, drain [begin,end) to files,
#            tar -> PUT the tarball to S3
#   restore: GET the tarball from S3 -> untar -> recreate the topics -> replay
#            every partition file with kafka-console-producer
#
# The consistency model is a frozen-end-offset cut: kafka-get-offsets --time -1
# is read ONCE per partition at backup start, and the consumer drains only up
# to that offset, so everything produced during the backup is excluded and all
# partitions are captured as of the same instant. This is a logical DATA
# backup: topic configs, ACLs, SCRAM users, and consumer-group offsets are NOT
# captured, and restore re-produces records so they receive fresh offsets.
#
# Everything the template injects is funnelled through container env vars so
# the script body stays plain shell. The same PodTemplateSpec serves both
# directions: the engine templates each string leaf independently (it cannot
# add/remove containers per mode), so the one image branches at runtime on
# $MODE.
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
        - name: kafka-backup
          image: ${KAFKA_IMAGE}
          imagePullPolicy: IfNotPresent
          env:
            # .Parameters carry the static knobs from the BackupClass: which
            # topics to back up (empty = all non-internal topics) and the
            # replication factor used when restore recreates a topic. On
            # restore they are read back from the Backup's driverMetadata, so
            # the same values apply round-trip.
            - name: TOPICS
              value: '{{ default "" (index .Parameters "topics") }}'
            - name: REPLICATION_FACTOR
              value: '{{ default "1" (index .Parameters "replicationFactor") }}'
            # .Release is the application being acted on: the source on backup,
            # the restore target on restore (in-place=source, to-copy=the new
            # app). .Release.Name is the apps.cozystack.io/Kafka CR name; the
            # chart names the Strimzi cluster "kafka-<name>", and Strimzi
            # exposes its plaintext bootstrap Service as
            # "kafka-<name>-kafka-bootstrap" on port 9092 (no TLS/auth on the
            # internal plain listener).
            - name: BOOTSTRAP
              value: "kafka-{{ .Release.Name }}-kafka-bootstrap.{{ .Release.Namespace }}.svc:9092"
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
          command: ["/usr/bin/bash", "-c"]
          args:
            - |
              set -eu
              BIN=/opt/kafka/bin
              BOOT="\${BOOTSTRAP}"

              # BucketInfo endpoints are bare host:port; COSI/seaweedfs serves
              # S3 over HTTPS, so default to https:// when no scheme is given.
              case "\${S3_ENDPOINT}" in
                http://*|https://*) BASE="\${S3_ENDPOINT}" ;;
                *) BASE="https://\${S3_ENDPOINT}" ;;
              esac

              # Split the endpoint into scheme/host/port. The Strimzi image ships
              # curl 7.76.1, whose --aws-sigv4 has a bug fixed only in 7.87.0: it
              # omits a non-default port from the SigV4 canonical Host even though
              # it sends the port in the Host header. seaweedfs recomputes the
              # signature from the received "host:port" and so rejects the PUT
              # with 403 whenever the endpoint carries a non-default port (the
              # e2e harness points S3_ENDPOINT at the :8333 in-cluster Service).
              # Strip the port from the signed/sent URL and redirect the
              # connection to the real port with --connect-to, so the signed,
              # sent and server-side Host all agree on the bare host. This is
              # also correct on curl >= 7.87, which drops the default port too.
              SCHEME="\${BASE%%://*}"
              HOSTPORT="\${BASE#*://}"; HOSTPORT="\${HOSTPORT%%/*}"
              HOST="\${HOSTPORT%%:*}"
              PORT="\${HOSTPORT##*:}"
              if [ "\${PORT}" = "\${HOSTPORT}" ]; then PORT=""; fi
              if [ "\${SCHEME}" = http ]; then DEFPORT=80; else DEFPORT=443; fi
              CONNECT_TO=""
              if [ -n "\${PORT}" ] && [ "\${PORT}" != "\${DEFPORT}" ]; then
                CONNECT_TO="--connect-to \${HOST}:\${DEFPORT}:\${HOST}:\${PORT}"
              fi

              if [ "\${MODE}" = backup ]; then SRC="\${SRC_BACKUP}"; else SRC="\${SRC_RESTORE}"; fi
              KEY="\${SRC}/kafka-topics.tar"
              OBJ_URL="\${SCHEME}://\${HOST}/\${S3_BUCKET}/\${KEY}"

              # curl --aws-sigv4 signs the request (SigV4) so no separate S3
              # client image is needed. -k accepts seaweedfs's internal
              # self-signed cert; a production strategy would mount the tenant
              # CA and drop -k. \${CONNECT_TO} is deliberately unquoted so an
              # empty value expands to no argument.
              s3() { curl -fsS -k \${CONNECT_TO} --aws-sigv4 "aws:amz:\${S3_REGION}:s3" --user "\${AWS_ACCESS_KEY_ID}:\${AWS_SECRET_ACCESS_KEY}" "\$@"; }

              WORK=/tmp/kafka-dump
              rm -rf "\${WORK}"; mkdir -p "\${WORK}"

              if [ "\${MODE}" = backup ]; then
                # Resolve the topic list: explicit TOPICS (comma/space
                # separated) or every non-internal topic (Kafka's own
                # bookkeeping topics start with "_").
                if [ -n "\${TOPICS}" ]; then
                  TLIST=\$(printf '%s' "\${TOPICS}" | tr ', ' '  ')
                else
                  # Fail closed: run --list as its own command so a broker
                  # error (unreachable / auth) aborts the Pod under set -e
                  # instead of the pipe masking the exit status to grep's and
                  # || true swallowing it. Only then filter internal topics, so
                  # an empty TLIST means genuinely no non-internal topics -
                  # never a silently swallowed failure that would PUT an empty
                  # tarball and report success.
                  raw=\$("\${BIN}"/kafka-topics.sh --bootstrap-server "\${BOOT}" --list)
                  TLIST=\$(printf '%s\n' "\${raw}" | grep -v '^_' || true)
                fi
                echo "backing up topics [\${TLIST}] from \${BOOT}"
                MANIFEST="\${WORK}/manifest.txt"
                : > "\${MANIFEST}"
                for t in \${TLIST}; do
                  # Freeze the consistency cut: read each partition's end offset
                  # (--time -1) ONCE here, and its begin offset (--time -2).
                  # Draining only up to the frozen end excludes anything
                  # produced while the backup runs.
                  ends=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\${t}" --time -1)
                  begins=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\${t}" --time -2)
                  for e in \${ends}; do
                    p=\${e%:*}; p=\${p##*:}
                    end=\${e##*:}
                    begin=0
                    for b in \${begins}; do
                      bp=\${b%:*}; bp=\${bp##*:}
                      if [ "\${bp}" = "\${p}" ]; then begin=\${b##*:}; break; fi
                    done
                    n=\$((end - begin))
                    printf '%s %s %s %s\n' "\${t}" "\${p}" "\${begin}" "\${end}" >> "\${MANIFEST}"
                    if [ "\${n}" -gt 0 ]; then
                      "\${BIN}"/kafka-console-consumer.sh --bootstrap-server "\${BOOT}" \
                        --topic "\${t}" --partition "\${p}" --offset "\${begin}" --max-messages "\${n}" \
                        --timeout-ms 120000 --property print.key=true --property print.timestamp=false \
                        > "\${WORK}/data-\${t}-\${p}.tsv"
                    fi
                    echo "  \${t}:\${p} [\${begin},\${end}) -> \${n} record(s)"
                  done
                done
                tar -C "\${WORK}" -cf /tmp/kafka-topics.tar .
                s3 -X PUT --upload-file /tmp/kafka-topics.tar "\${OBJ_URL}"
                echo "uploaded s3://\${S3_BUCKET}/\${KEY}"
              else
                echo "restoring topics into \${BOOT} from s3://\${S3_BUCKET}/\${KEY}"
                s3 -o /tmp/kafka-topics.tar "\${OBJ_URL}"
                tar -C "\${WORK}" -xf /tmp/kafka-topics.tar
                [ -f "\${WORK}/manifest.txt" ] || { echo "manifest missing in backup" >&2; exit 1; }
                # Recreate every backed-up topic with its original partition
                # count (needed so keyed records re-produce into the same
                # partition); existing topics are left as-is. Topic configs,
                # ACLs and consumer offsets are out of scope - this restores
                # DATA only.
                SEEN=""
                while read -r mt mp mb me; do
                  case " \${SEEN} " in *" \${mt} "*) ;; *) SEEN="\${SEEN} \${mt}" ;; esac
                done < "\${WORK}/manifest.txt"
                for t in \${SEEN}; do
                  parts=0
                  while read -r mt mp mb me; do
                    [ "\${mt}" = "\${t}" ] || continue
                    if [ "\${mp}" -ge "\${parts}" ]; then parts=\$((mp + 1)); fi
                  done < "\${WORK}/manifest.txt"
                  "\${BIN}"/kafka-topics.sh --bootstrap-server "\${BOOT}" --create --if-not-exists \
                    --topic "\${t}" --partitions "\${parts}" --replication-factor "\${REPLICATION_FACTOR}"
                done
                # Replay every dumped partition file. parse.key restores the
                # original key so the default partitioner reproduces the
                # original partition placement.
                for f in "\${WORK}"/data-*.tsv; do
                  [ -e "\${f}" ] || continue
                  base=\${f##*/data-}; base=\${base%.tsv}
                  t=\${base%-*}
                  "\${BIN}"/kafka-console-producer.sh --bootstrap-server "\${BOOT}" \
                    --topic "\${t}" --property parse.key=true < "\${f}"
                done
                echo "restore complete"
              fi
          resources:
            requests:
              cpu: 100m
              memory: 256Mi
            limits:
              cpu: "1"
              memory: 1Gi
          securityContext:
            # Minimal hardening, matching the ClickHouse/NATS examples. The
            # Strimzi image runs the CLI as its default user; tenants enforcing
            # PSA "restricted" should add runAsNonRoot/runAsUser for the image
            # they pin.
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
EOF

log_success "Job strategy '${STRATEGY_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-backupclass.sh"
