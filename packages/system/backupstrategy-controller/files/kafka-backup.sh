set -euo pipefail
# BIN is the Kafka CLI directory; the Strimzi image ships it at /opt/kafka/bin.
# Overridable only so hack/kafka-backup-script.bats can point it at stubbed
# kafka-topics.sh / kafka-configs.sh; the Job never sets it.
BIN="${BIN:-/opt/kafka/bin}"
TAB=$(printf '\t')
file=/tmp/kafka-metadata.txt

# Validate MODE up front and fail closed on anything unexpected. internal/
# template renders the input unchanged when it fails, so a broken env render
# would ship the literal "{{ .Mode }}"; without this guard the bare `else`
# below would run the destructive restore path under the guise of a backup.
case "${MODE}" in
  backup|restore|cleanup) ;;
  *) echo "unknown MODE '${MODE}' (expected backup|restore|cleanup)" >&2; exit 1 ;;
esac

case "${S3_ENDPOINT}" in
  http://*|https://*) base="${S3_ENDPOINT}" ;;
  *) base="https://${S3_ENDPOINT}" ;;
esac
# ARTIFACT_URI is s3://<bucket>/<key>. Split it, then build the HTTP
# object URL honouring path-style vs virtual-hosted addressing.
case "${ARTIFACT_URI}" in
  s3://*) rest="${ARTIFACT_URI#s3://}"; bucket="${rest%%/*}"; key="${rest#*/}" ;;
  *) echo "expected an s3:// artifact URI, got '${ARTIFACT_URI}' (set spec.artifactURITemplate)" >&2; exit 1 ;;
esac
# Split the endpoint into scheme, host, optional port and optional path
# prefix. IPv6-safe: a bracketed literal ([::1]) keeps its brackets and only
# a colon OUTSIDE them is the port separator.
scheme="${base%%://*}"
rest2="${base#*://}"
hostport="${rest2%%/*}"
case "${rest2}" in */*) pathpart="/${rest2#*/}" ;; *) pathpart="" ;; esac
case "${hostport}" in
  \[*\]*) host="${hostport%%\]*}]"; after="${hostport##*\]}"; port="${after#:}"; [ "${port}" = "${after}" ] && port="" ;;
  *) host="${hostport%%:*}"; port="${hostport##*:}"; [ "${port}" = "${hostport}" ] && port="" ;;
esac
if [ "${scheme}" = http ]; then defport=80; else defport=443; fi
# curl 7.76.1 (this image) omits a NON-default port from the SigV4 canonical
# host but still sends it in the Host header, so the backend recomputes a
# different signature and 403s every request (fixed in curl 7.86.0). Sign and
# send a portless URL and redirect the connection to the real port with
# --connect-to, so signed, sent and server-side host all agree; correct on
# curl >= 7.86 too (which drops the default port anyway). No -k: TLS is
# verified against the endpoint's certificate (the platform endpoint is the
# ACME-valid ingress), matching strategy-rabbitmq-default.yaml.
if [ "${S3_FORCE_PATH_STYLE}" = "false" ]; then
  reqhost="${bucket}.${host}"
  obj_url="${scheme}://${reqhost}${pathpart}/${key}"
else
  reqhost="${host}"
  obj_url="${scheme}://${reqhost}${pathpart}/${bucket}/${key}"
fi
# --connect-to matches HOST1 against the request host, which differs between
# path-style (${host}) and virtual-hosted (${bucket}.${host}) — key it off the
# host the URL actually uses so the redirect fires in both modes; the real
# connection target stays ${host}:${port}.
connect_to=""
if [ -n "${port}" ] && [ "${port}" != "${defport}" ]; then
  connect_to="--connect-to ${reqhost}:${defport}:${host}:${port}"
fi
# ${connect_to} is unquoted so an empty value expands to no argument.
s3() { curl -fsS ${connect_to} --aws-sigv4 "aws:amz:${S3_REGION}:s3" \
  --user "${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}" "$@"; }

# Cleanup runs when the owning Backup is deleted: delete the object and exit
# before any broker access (the source may already be gone). A missing object
# is success (delete is idempotent), so a non-2xx/404 exits non-zero and the
# controller retries rather than orphaning the object.
if [ "${MODE}" = cleanup ]; then
  echo "deleting ${ARTIFACT_URI}"
  code=$(curl -sS -o /dev/null -w '%{http_code}' ${connect_to} \
    --aws-sigv4 "aws:amz:${S3_REGION}:s3" \
    --user "${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}" \
    -X DELETE "${obj_url}" || true)
  case "${code}" in
    2*|404) echo "deleted (HTTP ${code})"; exit 0 ;;
    *) echo "delete failed: HTTP ${code}" >&2; exit 1 ;;
  esac
fi

# Extract PartitionCount / ReplicationFactor from a --describe summary line;
# empty output means an unparsable describe.
describe_field() { printf '%s' "$1" | grep -oE "$2: [0-9]+" | awk '{print $2}'; }

if [ "${MODE}" = backup ]; then
  echo "exporting Kafka topic metadata from ${BOOTSTRAP}"
  # Fail closed: a failed --list must abort, not read as "no topics" and upload
  # an empty backup that reports success. An empty list after a SUCCESSFUL call
  # is a legitimate "no user topics" state.
  if ! all=$("${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --list --exclude-internal); then
    echo "cannot list topics on ${BOOTSTRAP}" >&2; exit 1
  fi
  : > "${file}"
  # Topic names cannot contain whitespace, so word-splitting is safe and keeps
  # the loop in the current shell (a piped while would swallow exits into a
  # subshell).
  for t in ${all}; do
    [ -n "${t}" ] || continue
    # kafka-topics --topic is a Java REGEX filter, not a literal name:
    # "audit.events" would also match "audit-events". Wrap the name in \Q...\E
    # so it matches exactly the one topic (a missing topic still yields exit 1 /
    # empty, preserved).
    if ! desc=$("${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --describe --topic "\Q${t}\E"); then
      echo "cannot describe topic ${t}" >&2; exit 1
    fi
    line=${desc%%$'\n'*}
    parts=$(describe_field "${line}" PartitionCount)
    rf=$(describe_field "${line}" ReplicationFactor)
    { [ -n "${parts}" ] && [ -n "${rf}" ]; } || { echo "unparsable describe for ${t}: ${line}" >&2; exit 1; }
    printf 'T%s%s%s%s%s%s\n' "${TAB}" "${t}" "${TAB}" "${parts}" "${TAB}" "${rf}" >> "${file}"
    # Non-default configs, ONE per line from kafka-configs --describe
    # ("  key=value sensitive=... synonyms=..."), so a value that contains a
    # comma stays in its own tab-separated field — the comma-joined "Configs:"
    # summary cannot be split unambiguously.
    if ! cfgout=$("${BIN}"/kafka-configs.sh --bootstrap-server "${BOOTSTRAP}" --describe --entity-type topics --entity-name "${t}"); then
      echo "cannot describe configs for ${t}" >&2; exit 1
    fi
    # Fail closed like the --list guard: grep exit 1 is "no non-default configs"
    # (fine); any other exit is an I/O error and must abort, not silently drop
    # the topic's configs.
    if cfglines=$(printf '%s\n' "${cfgout}" | grep -E '^[[:space:]]+[^[:space:]=]+='); then :; else
      rc=$?; [ "${rc}" -eq 1 ] || { echo "cannot parse configs for ${t}" >&2; exit 1; }
      cfglines=""
    fi
    if [ -n "${cfglines}" ]; then
      printf '%s\n' "${cfglines}" | while read -r cl; do
        kv=${cl%% sensitive=*}
        k=${kv%%=*}
        v=${kv#*=}
        printf 'C%s%s%s%s%s%s\n' "${TAB}" "${t}" "${TAB}" "${k}" "${TAB}" "${v}" >> "${file}"
      done
    fi
    echo "  ${t}: partitions=${parts} rf=${rf}"
  done
  s3 -X PUT --upload-file "${file}" "${obj_url}"
  echo "uploaded ${ARTIFACT_URI}"
else
  echo "restoring Kafka topic metadata into ${BOOTSTRAP} from ${ARTIFACT_URI}"
  s3 -o "${file}" "${obj_url}"
  # Probe existence with --list, fail closed: a --describe under 2>/dev/null
  # cannot tell "topic absent" from "broker unreachable" (Kafka 3.9.1 exits 1
  # for both, and the distinguishing text goes to stdout). --list instead fails
  # the whole restore when the broker is unreachable, so a transient outage is
  # never misread as "absent" and routed into --create.
  if ! live=$("${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --list); then
    echo "cannot list topics on ${BOOTSTRAP}" >&2; exit 1
  fi
  # Redirect (not a pipe) so an exit inside aborts the whole script.
  while IFS="${TAB}" read -r kind a b c; do
    case "${kind}" in
    T)
      t=$a; parts=$b; rf=$c
      # grep -F -x: literal whole-line match against the live list.
      if printf '%s\n' "${live}" | grep -qxF -- "${t}"; then
        if ! desc=$("${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --describe --topic "\Q${t}\E"); then
          echo "cannot describe existing topic ${t}" >&2; exit 1
        fi
        line=${desc%%$'\n'*}
        lp=$(describe_field "${line}" PartitionCount)
        lrf=$(describe_field "${line}" ReplicationFactor)
        # Validate the live shape parsed (as the backup path does for parts/rf):
        # an empty lp/lrf makes the numeric comparisons below exit 2, which the
        # if/elif would swallow and report the topic restored without altering.
        { [ -n "${lp}" ] && [ -n "${lrf}" ]; } || { echo "unparsable live describe for ${t}: ${line}" >&2; exit 1; }
        # A restore that cannot reach the recorded shape must fail loudly, not
        # report success against a divergent topic.
        if [ "${lrf}" != "${rf}" ]; then
          echo "topic ${t}: replication factor differs (live=${lrf} backup=${rf}); cannot reconcile" >&2; exit 1
        fi
        if [ "${lp}" -lt "${parts}" ]; then
          "${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --alter --topic "\Q${t}\E" --partitions "${parts}"
        elif [ "${lp}" -gt "${parts}" ]; then
          echo "topic ${t}: live has ${lp} partitions, backup ${parts}; Kafka cannot decrease partitions" >&2; exit 1
        fi
      else
        "${BIN}"/kafka-topics.sh --bootstrap-server "${BOOTSTRAP}" --create --topic "${t}" --partitions "${parts}" --replication-factor "${rf}"
      fi
      echo "  topic ${t}: partitions=${parts} rf=${rf}"
      ;;
    C)
      t=$a; k=$b; v=$c
      # Bracket-group only when the value contains a comma, so kafka-configs
      # does not split it into bogus key=val tokens.
      case "${v}" in
        *,*) add="${k}=[${v}]" ;;
        *)   add="${k}=${v}" ;;
      esac
      "${BIN}"/kafka-configs.sh --bootstrap-server "${BOOTSTRAP}" --alter --entity-type topics --entity-name "${t}" --add-config "${add}"
      echo "    config ${t}: ${k}"
      ;;
    *)
      # Fail closed on an unrecognised record: a well-formed but wrong-content
      # object (curl -f catches transport errors, not a wrong-content 200) must
      # not restore a subset of topics and still report success.
      echo "unrecognised metadata record kind '${kind}' (corrupt or wrong object?)" >&2; exit 1
      ;;
    esac
  done < "${file}"
  echo "restore complete"
fi
