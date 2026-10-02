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
# is read ONCE per partition, and the consumer drains only up to that offset,
# so everything produced after the read is excluded. The reads happen per
# topic (inside the topic loop), so the cut is atomic across a topic's own
# partitions but NOT across topics - Kafka offers no cross-topic guarantee
# regardless. This is a logical DATA
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
            # topics to back up (empty = all non-internal topics) and an
            # optional replication-factor override for the topics restore
            # recreates (empty = each topic's replication factor as captured
            # at backup time). On restore they are read back from the Backup's
            # driverMetadata, so the same values apply round-trip.
            - name: TOPICS
              value: '{{ default "" (index .Parameters "topics") }}'
            - name: REPLICATION_FACTOR
              value: '{{ default "" (index .Parameters "replicationFactor") }}'
            # .Release is the application being acted on: the source on backup,
            # the restore target on restore (in-place=source, to-copy=the new
            # app). .Release.Name is the apps.cozystack.io/Kafka CR name; the
            # chart names the Strimzi cluster "kafka-<name>", and Strimzi
            # exposes its plaintext bootstrap Service as
            # "kafka-<name>-kafka-bootstrap" on port 9092 (no TLS/auth on the
            # internal plain listener).
            - name: BOOTSTRAP
              value: "kafka-{{ .Release.Name }}-kafka-bootstrap.{{ .Release.Namespace }}.svc:9092"
            # S3 object key is scoped by the SOURCE namespace + app name so a
            # to-copy restore reads what the source wrote and two same-named
            # apps in different namespaces sharing one bucket do not collide on
            # <app>/kafka-topics.tar. On backup the source is .Release.Name; on
            # restore it is .Backup.ApplicationRef.Name (.Backup is only set on
            # restore - the guard keeps backup mode from rendering "<no value>"
            # into an unused var). The namespace is .Release.Namespace in both
            # modes and always names the source: a to-copy target is a
            # TypedLocalObjectReference, so it shares the RestoreJob's (source's)
            # namespace, and an in-place restore targets the source itself.
            - name: SRC_BACKUP
              value: "{{ .Release.Name }}"
            - name: SRC_RESTORE
              value: "{{ if .Backup }}{{ .Backup.ApplicationRef.Name }}{{ end }}"
            - name: SRC_NAMESPACE
              value: "{{ .Release.Namespace }}"
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
          # The same Secret's optional ca.crt (the S3 endpoint's CA when it is
          # not publicly trusted; create_s3_secret copies the in-cluster
          # seaweedfs CA into it) is projected as a file so curl can verify the
          # endpoint against it. The key is optional: without it the file is
          # absent and curl falls back to the image's trust store.
          volumeMounts:
            - name: s3-ca
              mountPath: /etc/kafka-backup-s3
              readOnly: true
          command: ["/usr/bin/bash", "-c"]
          args:
            - |
              set -eu
              # Overridable only so hack/kafka-topic-backup-strategy.bats can
              # execute this body outside the Pod against stub CLIs; the Pod
              # sets none of them.
              BIN="\${BIN:-/opt/kafka/bin}"
              SCRATCH="\${SCRATCH:-/tmp}"
              CA_FILE="\${CA_FILE:-/etc/kafka-backup-s3/ca.crt}"
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
              # Demo-scope parse: handles the "scheme://host[:port]" that
              # BucketInfo advertises. An IPv6 literal ([::1]:8333) or an
              # endpoint carrying a path prefix would be mis-split - out of
              # scope here.
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
              KEY="\${SRC_NAMESPACE}/\${SRC}/kafka-topics.tar"
              OBJ_URL="\${SCHEME}://\${HOST}/\${S3_BUCKET}/\${KEY}"

              # curl --aws-sigv4 signs the request (SigV4) so no separate S3
              # client image is needed. The certificate is verified: against the
              # CA projected from the Secret when the tenant supplied one, else
              # against the image's trust store. \${CA_OPT} and \${CONNECT_TO}
              # are deliberately unquoted so an empty value expands to no
              # argument.
              CA_OPT=""
              if [ -s "\${CA_FILE}" ]; then CA_OPT="--cacert \${CA_FILE}"; fi
              s3() { curl -fsS \${CA_OPT} \${CONNECT_TO} --aws-sigv4 "aws:amz:\${S3_REGION}:s3" --user "\${AWS_ACCESS_KEY_ID}:\${AWS_SECRET_ACCESS_KEY}" "\$@"; }

              # Records a topic currently holds, summed over its partitions as
              # end - begin. kafka-get-offsets prints only the partitions whose
              # lookup succeeded and still exits 0, so the listing is held to
              # the partition count the caller read off the topic itself: a
              # partition lost to a leader election would otherwise drop out of
              # the sum and pass a populated topic off as empty.
              #
              # --topic is a Java regex in kafka-topics and kafka-get-offsets,
              # so an unquoted name also matches its siblings ("audit.events"
              # matches "audit-events") and would mix another topic's
              # partitions into the count. \Q...\E pins it to a literal.
              #
              # Called as \$(topic_records ...) || exit 1, and a function run
              # as the left operand of || has errexit switched off inside it,
              # so every command here carries its own || return 1.
              topic_records() {
                local t=\$1 want_parts=\$2 ends begins seen e ekey eo bo b total
                ends=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\Q\${t}\E" --time -1) || return 1
                begins=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\Q\${t}\E" --time -2) || return 1
                seen=0
                for e in \${ends}; do seen=\$((seen + 1)); done
                if [ "\${seen}" -ne "\${want_parts}" ]; then
                  echo "offset listing returned \${seen} of \${want_parts} partition(s) for \${t}; refusing to act on an incomplete listing" >&2
                  return 1
                fi
                total=0
                for e in \${ends}; do
                  ekey=\${e%:*}
                  eo=\${e##*:}
                  # Match the whole topic:partition key, not the partition
                  # index alone, so a line belonging to another topic cannot
                  # supply this partition's begin offset. A partition missing
                  # from "begins" defaults to 0, which can only overstate.
                  bo=0
                  for b in \${begins}; do
                    if [ "\${b%:*}" = "\${ekey}" ]; then bo=\${b##*:}; break; fi
                  done
                  total=\$((total + eo - bo))
                done
                echo "\${total}"
              }

              WORK="\${SCRATCH}/kafka-dump"
              TARBALL="\${SCRATCH}/kafka-topics.tar"
              rm -rf "\${WORK}"; mkdir -p "\${WORK}"

              if [ "\${MODE}" = backup ]; then
                # Resolve the topic list: explicit TOPICS (comma/space
                # separated) or every non-internal topic. Kafka's internal
                # topics are double-underscore (__consumer_offsets,
                # __transaction_state); single-underscore names such as
                # _schemas are user data and must not be filtered.
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
                  TLIST=\$(printf '%s\n' "\${raw}" | grep -v '^__' || true)
                fi
                # TOPICS is operator input reaching unquoted expansions. Kafka
                # topic names are [a-zA-Z0-9._-], so anything else is refused
                # before it can glob (set -f keeps a literal "*" out of the
                # loop below) or escape the \Q...\E pins. A name listed twice
                # would run the per-topic block twice: the manifest gains a
                # second set of lines while the data files overwrite, and the
                # restore's count check can then never balance - so a
                # duplicate is refused here rather than reported as a
                # Succeeded backup nothing can restore.
                set -f
                UNIQ=""
                for t in \${TLIST}; do
                  case "\${t}" in
                    *[!A-Za-z0-9._-]*) echo "topic name '\${t}' carries a character outside Kafka's [a-zA-Z0-9._-]; refusing" >&2; exit 1 ;;
                  esac
                  case " \${UNIQ} " in
                    *" \${t} "*) echo "topic \${t} is listed more than once in topics; refusing to back it up twice" >&2; exit 1 ;;
                  esac
                  UNIQ="\${UNIQ} \${t}"
                done
                TLIST=\${UNIQ}
                echo "backing up topics [\${TLIST}] from \${BOOT}"
                MANIFEST="\${WORK}/manifest.txt"
                : > "\${MANIFEST}"
                for t in \${TLIST}; do
                  # Freeze the consistency cut: read each partition's end offset
                  # (--time -1) ONCE here, and its begin offset (--time -2).
                  # Draining only up to the frozen end excludes anything
                  # produced while the backup runs.
                  ends=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\Q\${t}\E" --time -1)
                  begins=\$("\${BIN}"/kafka-get-offsets.sh --bootstrap-server "\${BOOT}" --topic "\Q\${t}\E" --time -2)
                  # The partition SET is not self-validating (see
                  # topic_records): re-derive the count from the topic itself
                  # and fail closed when the listing is short, or a partition
                  # lost to a leader election would get no manifest line, no
                  # data file, and never reach the drain check below - a whole
                  # partition missing from a backup that still reports
                  # Succeeded. (A short "begins" needs no separate check: its
                  # per-partition default of 0 can only overstate n, which the
                  # drain check then catches.)
                  #
                  # The same --describe line carries ReplicationFactor. It goes
                  # into the manifest so restore recreates the topic as the
                  # source had it: a broker whose min.insync.replicas exceeds
                  # the recreated topic's replication factor rejects every
                  # acks=all write, and kafka-console-producer logs the
                  # rejection without failing, so a replay into an
                  # under-replicated topic lands nothing.
                  desc=\$("\${BIN}"/kafka-topics.sh --bootstrap-server "\${BOOT}" --describe --topic "\Q\${t}\E")
                  pc=\$(printf '%s\n' "\${desc}" | sed -n 's/.*PartitionCount: *\([0-9][0-9]*\).*/\1/p' | head -1)
                  rf=\$(printf '%s\n' "\${desc}" | sed -n 's/.*ReplicationFactor: *\([0-9][0-9]*\).*/\1/p' | head -1)
                  case "\${pc}" in
                    ''|*[!0-9]*) echo "could not read PartitionCount for \${t}" >&2; exit 1 ;;
                  esac
                  case "\${rf}" in
                    ''|*[!0-9]*) echo "could not read ReplicationFactor for \${t}" >&2; exit 1 ;;
                  esac
                  seen=0
                  for e in \${ends}; do seen=\$((seen + 1)); done
                  if [ "\${seen}" -ne "\${pc}" ]; then
                    echo "offset listing returned \${seen} of \${pc} partition(s) for \${t}; refusing to upload an incomplete backup" >&2
                    exit 1
                  fi
                  for e in \${ends}; do
                    ekey=\${e%:*}
                    p=\${ekey##*:}
                    end=\${e##*:}
                    begin=0
                    for b in \${begins}; do
                      # Match the whole topic:partition key, not the partition
                      # index alone. Keyed on the index, a line belonging to
                      # another topic supplies this partition's begin offset and
                      # silently shifts the cut - and the drain check below
                      # cannot see it, because n is derived from that same wrong
                      # begin. This holds whatever order the listing arrives in.
                      if [ "\${b%:*}" = "\${ekey}" ]; then begin=\${b##*:}; break; fi
                    done
                    n=\$((end - begin))
                    printf '%s %s %s %s %s\n' "\${t}" "\${p}" "\${begin}" "\${end}" "\${rf}" >> "\${MANIFEST}"
                    if [ "\${n}" -gt 0 ]; then
                      "\${BIN}"/kafka-console-consumer.sh --bootstrap-server "\${BOOT}" \
                        --topic "\${t}" --partition "\${p}" --offset "\${begin}" --max-messages "\${n}" \
                        --timeout-ms 120000 --property print.key=true --property print.timestamp=false \
                        > "\${WORK}/data-\${t}-\${p}.tsv"
                      # kafka-console-consumer exits 0 even on a short read: a
                      # --timeout-ms expiry is swallowed inside ConsoleConsumer,
                      # so a truncated drain is indistinguishable from a full one
                      # by exit status. One newline-free record is one line (the
                      # README's key/value fidelity constraint), so a line-count
                      # short of n means the [begin,end) cut was not fully
                      # consumable - transactional control batches or a compacted
                      # topic, both out of scope - and the backup must fail
                      # rather than PUT a partial tarball reported as Succeeded.
                      got=\$(wc -l < "\${WORK}/data-\${t}-\${p}.tsv")
                      if [ "\${got}" -ne "\${n}" ]; then
                        echo "partial drain on \${t}:\${p}: captured \${got} of \${n} record(s) in [\${begin},\${end}); refusing to upload an incomplete backup" >&2
                        exit 1
                      fi
                    fi
                    echo "  \${t}:\${p} [\${begin},\${end}) -> \${n} record(s)"
                  done
                  echo "  \${t}: \${pc} partition(s), replication factor \${rf}"
                done
                tar -C "\${WORK}" -cf "\${TARBALL}" .
                s3 -X PUT --upload-file "\${TARBALL}" "\${OBJ_URL}"
                echo "uploaded s3://\${S3_BUCKET}/\${KEY}"
              else
                echo "restoring topics into \${BOOT} from s3://\${S3_BUCKET}/\${KEY}"
                s3 -o "\${TARBALL}" "\${OBJ_URL}"
                tar -C "\${WORK}" -xf "\${TARBALL}"
                [ -f "\${WORK}/manifest.txt" ] || { echo "manifest missing in backup" >&2; exit 1; }
                # The manifest arrives inside the downloaded tarball, so treat it
                # as untrusted and validate before any field is used as a number.
                # A non-numeric partition would otherwise slip through the
                # numeric test below: an if condition is exempt from errexit, so
                # test exits 2, the term reads false, parts under-counts and the
                # topic is created smaller - which the shape check then compares
                # against itself and passes. An empty manifest is refused
                # too: both verification loops would run zero times and the Pod
                # would report a successful restore of nothing.
                lines=0
                while read -r mt mp mb me mr; do
                  lines=\$((lines + 1))
                  [ -n "\${mt}" ] || { echo "manifest line \${lines}: missing topic" >&2; exit 1; }
                  # The topic name reaches unquoted expansions and a case
                  # pattern below, with globbing on in this branch; hold it to
                  # Kafka's name charset the way the backup side holds TOPICS.
                  case "\${mt}" in
                    *[!A-Za-z0-9._-]*)
                      echo "manifest line \${lines}: topic name '\${mt}' carries a character outside Kafka's [a-zA-Z0-9._-]; refusing to restore from a malformed backup" >&2
                      exit 1 ;;
                  esac
                  for v in "\${mp}" "\${mb}" "\${me}" "\${mr}"; do
                    case "\${v}" in
                      ''|*[!0-9]*)
                        echo "manifest line \${lines} (topic \${mt}): non-numeric field '\${v}'; refusing to restore from a malformed backup" >&2
                        exit 1 ;;
                    esac
                  done
                done < "\${WORK}/manifest.txt"
                [ "\${lines}" -gt 0 ] || { echo "manifest is empty: the backup recorded no partitions, so there is nothing to restore" >&2; exit 1; }
                # Data files and manifest must agree both ways, checked before
                # anything is created or replayed. The file names come out of
                # the same untrusted tarball as the manifest: a file the
                # manifest never listed would be data that skipped the create,
                # shape, emptiness and post-replay checks (or, ignored by the
                # manifest-driven replay below, silently not restored), and a
                # listed partition with records but no file would be found
                # only after earlier partitions had already been replayed.
                for f in "\${WORK}"/data-*.tsv; do
                  [ -e "\${f}" ] || continue
                  base=\${f##*/data-}; base=\${base%.tsv}
                  ft=\${base%-*}
                  fp=\${base##*-}
                  listed=0
                  while read -r mt mp mb me mr; do
                    if [ "\${mt}" = "\${ft}" ] && [ "\${mp}" = "\${fp}" ]; then listed=1; break; fi
                  done < "\${WORK}/manifest.txt"
                  if [ "\${listed}" -ne 1 ]; then
                    echo "backup contains data file \${f##*/}, which the manifest does not list; refusing to restore an inconsistent backup" >&2
                    exit 1
                  fi
                done
                while read -r mt mp mb me mr; do
                  [ "\${me}" -gt "\${mb}" ] || continue
                  if [ ! -f "\${WORK}/data-\${mt}-\${mp}.tsv" ]; then
                    echo "backup records \$((me - mb)) record(s) for \${mt}:\${mp} but carries no data file for it; refusing to restore an inconsistent backup" >&2
                    exit 1
                  fi
                done < "\${WORK}/manifest.txt"
                # Recreate every backed-up topic with its original partition
                # count (needed so keyed records re-produce into the same
                # partition) and, unless the replicationFactor parameter
                # overrides it, its original replication factor; existing
                # topics are left as-is. Topic configs, ACLs and consumer
                # offsets are out of scope - this restores DATA only.
                SEEN=""
                while read -r mt mp mb me mr; do
                  case " \${SEEN} " in *" \${mt} "*) ;; *) SEEN="\${SEEN} \${mt}" ;; esac
                done < "\${WORK}/manifest.txt"
                for t in \${SEEN}; do
                  parts=0
                  rf=""
                  while read -r mt mp mb me mr; do
                    [ "\${mt}" = "\${t}" ] || continue
                    if [ "\${mp}" -ge "\${parts}" ]; then parts=\$((mp + 1)); fi
                    if [ -n "\${rf}" ] && [ "\${rf}" != "\${mr}" ]; then
                      echo "manifest lists \${t} with replication factors \${rf} and \${mr}; refusing to restore from an inconsistent backup" >&2
                      exit 1
                    fi
                    rf=\${mr}
                  done < "\${WORK}/manifest.txt"
                  if [ -n "\${REPLICATION_FACTOR}" ]; then rf=\${REPLICATION_FACTOR}; fi
                  # A replication factor the target cannot host (more replicas
                  # than brokers) fails right here, in Kafka's own words, rather
                  # than after a replay; set replicationFactor in the
                  # BackupClass to restore into a smaller cluster.
                  "\${BIN}"/kafka-topics.sh --bootstrap-server "\${BOOT}" --create --if-not-exists \
                    --topic "\${t}" --partitions "\${parts}" --replication-factor "\${rf}"
                  # --if-not-exists leaves a pre-existing topic's partition
                  # count and replication factor alone. The post-replay check
                  # below compares only the per-topic total, so replaying into
                  # a topic with a different partition count would silently
                  # re-place every keyed record and still add up, and a
                  # pre-existing topic with fewer replicas than the broker's
                  # min.insync.replicas would take the replay and land nothing.
                  # Confirm both against what the topic really has.
                  desc=\$("\${BIN}"/kafka-topics.sh --bootstrap-server "\${BOOT}" --describe --topic "\Q\${t}\E")
                  have_parts=\$(printf '%s\n' "\${desc}" | sed -n 's/.*PartitionCount: *\([0-9][0-9]*\).*/\1/p' | head -1)
                  have_rf=\$(printf '%s\n' "\${desc}" | sed -n 's/.*ReplicationFactor: *\([0-9][0-9]*\).*/\1/p' | head -1)
                  case "\${have_parts}" in
                    ''|*[!0-9]*) echo "could not read PartitionCount for \${t} after create" >&2; exit 1 ;;
                  esac
                  case "\${have_rf}" in
                    ''|*[!0-9]*) echo "could not read ReplicationFactor for \${t} after create" >&2; exit 1 ;;
                  esac
                  if [ "\${have_parts}" != "\${parts}" ]; then
                    echo "topic \${t} has \${have_parts} partition(s), backup recorded \${parts}; refusing to restore into a differently shaped topic" >&2
                    exit 1
                  fi
                  if [ "\${have_rf}" != "\${rf}" ]; then
                    echo "topic \${t} has replication factor \${have_rf}, restore expects \${rf}; refusing to replay into a pre-existing topic with a different replication factor" >&2
                    exit 1
                  fi
                  # kafka-console-producer writes with acks=all, and a broker
                  # rejects every such write into a topic whose replication
                  # factor is below its min.insync.replicas - while the
                  # producer only logs the rejection and exits 0. --describe
                  # --all prints the effective value, broker default included,
                  # so the mismatch is named here, with its remedy, instead of
                  # surfacing as a count shortfall after an empty replay.
                  misr=\$("\${BIN}"/kafka-configs.sh --bootstrap-server "\${BOOT}" --describe --all --entity-type topics --entity-name "\${t}" \
                    | sed -n 's/.*min\.insync\.replicas=\([0-9][0-9]*\).*/\1/p' | head -1)
                  case "\${misr}" in
                    ''|*[!0-9]*) echo "could not read min.insync.replicas for \${t}" >&2; exit 1 ;;
                  esac
                  if [ "\${have_rf}" -lt "\${misr}" ]; then
                    echo "topic \${t} has replication factor \${have_rf} but the broker requires min.insync.replicas=\${misr}, so every replayed write would be rejected. Set replicationFactor to at least \${misr} in the BackupClass (or restore into a cluster whose min.insync.replicas fits) and re-run." >&2
                    exit 1
                  fi
                  # Refuse a non-empty target BEFORE replaying, not after. The
                  # replay appends, and the driver retries a Pod that dies
                  # mid-replay, so checking only afterwards means each attempt
                  # has already written another full copy into a live topic and
                  # the count check merely reports the damage. Checking here
                  # leaves the topic untouched instead. Remedy: empty or delete
                  # the topic (the in-place step deletes it) and re-run.
                  held=\$(topic_records "\${t}" "\${have_parts}") || exit 1
                  if [ "\${held}" -ne 0 ]; then
                    echo "topic \${t} already holds \${held} record(s); this restore appends and is supported only against an absent or empty topic. Empty or delete it and re-run." >&2
                    exit 1
                  fi
                  echo "  \${t}: created or verified empty, \${parts} partition(s), replication factor \${rf}"
                done
                # Replay topic by topic in manifest order, announcing each one,
                # so the Pod log doubles as a resume map: a retry after a Pod
                # died mid-replay stops at the emptiness check above on the
                # first topic that was already replayed, and this log says which
                # topics landed and which still need the target emptied.
                # parse.key restores the original key so the default partitioner
                # reproduces the original partition placement.
                for t in \${SEEN}; do
                  echo "replaying \${t}"
                  while read -r mt mp mb me mr; do
                    [ "\${mt}" = "\${t}" ] || continue
                    [ "\${me}" -gt "\${mb}" ] || continue
                    "\${BIN}"/kafka-console-producer.sh --bootstrap-server "\${BOOT}" \
                      --topic "\${t}" --property parse.key=true < "\${WORK}/data-\${t}-\${mp}.tsv"
                  done < "\${WORK}/manifest.txt"
                  echo "  \${t}: replayed"
                done
                # Verify the replay landed. kafka-console-producer, like the
                # consumer on backup, can exit 0 without every record being
                # accepted (a broker-side reject prints but does not fail the
                # process), so confirm each restored topic now holds exactly the
                # record count the manifest recorded (sum of end-begin per
                # partition). A shortfall means the replay dropped records; an
                # overshoot means the target was not empty - this DATA restore
                # appends, so it is supported only against an absent or empty
                # topic. Either way, fail rather than report a lossy or
                # duplicated restore as complete.
                for t in \${SEEN}; do
                  want=0
                  parts=0
                  while read -r mt mp mb me mr; do
                    [ "\${mt}" = "\${t}" ] || continue
                    want=\$((want + me - mb))
                    if [ "\${mp}" -ge "\${parts}" ]; then parts=\$((mp + 1)); fi
                  done < "\${WORK}/manifest.txt"
                  have=\$(topic_records "\${t}" "\${parts}") || exit 1
                  if [ "\${have}" -ne "\${want}" ]; then
                    echo "restore verification failed for \${t}: topic holds \${have} record(s), backup recorded \${want}; refusing to report a lossy or duplicated restore as complete" >&2
                    exit 1
                  fi
                  echo "  \${t}: restored \${have} record(s)"
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
            # restricted-clean. The Strimzi image's default user is a non-root
            # numeric UID (1001), so runAsNonRoot passes without pinning
            # runAsUser to a value specific to this image tag.
            runAsNonRoot: true
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
      volumes:
        - name: s3-ca
          secret:
            secretName: "{{ .Release.Name }}-backup-s3"
            optional: true
            items:
              - key: ca.crt
                path: ca.crt
EOF

log_success "Job strategy '${STRATEGY_NAME}' created."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-backupclass.sh"
