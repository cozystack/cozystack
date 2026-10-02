#!/usr/bin/env bats
# Behavioural unit test for the Kafka topic-data Job strategy script: the bash
# body embedded in examples/backups/kafka/01-create-strategy.sh. Its only other
# coverage is chainsaw/kafka-2-backup-roundtrip, a cluster test that drives the
# happy path plus one refusal, so the script's fail-closed guards were unpinned:
# any one of them could be deleted and nothing in the tree would go red. This
# test EXECUTES the script exactly as step 01 renders it, against stubbed
# kafka-*.sh (BIN override), a stubbed curl (on PATH) and a directory standing in
# for S3, with fault injection for the broker behaviours the guards exist for.
# Same shape as hack/kafka-backup-script.bats.
#
# Runner note: hack/cozytest.sh knows only @test + bash - no setup()/teardown(),
# no run/$status/$output, and a `!`-negated command is exempt from its set -e -
# so each test calls init_stubs itself, and negative assertions go through
# expect_fail or an explicit if/return 1.

REPO="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
EXAMPLE="$REPO/examples/backups/kafka"

# init_stubs renders the strategy through a kubectl stub, lifts the container
# body out of it, and builds the stub CLIs. Sets STATE, BINDIR, PATHDIR, S3DIR.
init_stubs() {
  STATE="$(mktemp -d)"
  BINDIR="$STATE/bin"; PATHDIR="$STATE/path"; S3DIR="$STATE/s3"
  mkdir -p "$BINDIR" "$PATHDIR" "$S3DIR" "$STATE/scratch"

  cat > "$PATHDIR/kubectl" <<STUB
#!/usr/bin/env bash
cat > "$STATE/rendered.yaml"
STUB
  chmod +x "$PATHDIR/kubectl"
  PATH="$PATHDIR:$PATH" KAFKA_IMAGE=stub/kafka:test bash "$EXAMPLE/01-create-strategy.sh" >/dev/null 2>&1
  yq '.spec.template.spec.containers[0].args[0]' "$STATE/rendered.yaml" > "$STATE/strategy.sh"
  grep -q 'kafka-console-producer' "$STATE/strategy.sh"

  # Fake broker: $BROKER/<topic>/rf, one file per partition (p<N>, one record
  # per line) and an optional begin<N> offset. Set at run time by run_strategy.
  cat > "$BINDIR/kafka-topics.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
mode=""; topic=""; parts=""; rf=""
while [ $# -gt 0 ]; do
  case "$1" in
    --list) mode=list ;;
    --describe) mode=describe ;;
    --create) mode=create ;;
    --delete) mode=delete ;;
    --topic) topic="$2"; shift ;;
    --partitions) parts="$2"; shift ;;
    --replication-factor) rf="$2"; shift ;;
    --bootstrap-server) shift ;;
  esac
  shift
done
# --topic is a Java regex for list/describe/delete; \Q..\E pins a literal.
# (Closing brace indented on purpose: hack/cozytest.sh rewrites every
# column-0 "}" line of the .bats file, heredoc bodies included.)
match() {
  case "$topic" in
    '\Q'*'\E') lit="${topic#\\Q}"; lit="${lit%\\E}"; [ "$1" = "$lit" ] ;;
    *) printf '%s' "$1" | grep -Eqx -- "$topic" ;;
  esac
  }
case "$mode" in
  list) ls "$BROKER" ;;
  describe)
    for d in "$BROKER"/*/; do t="${d%/}"; t="${t##*/}"; match "$t" || continue
      pc=$(ls "$BROKER/$t" | grep -c '^p[0-9]*$')
      printf 'Topic: %s\tTopicId: stub\tPartitionCount: %s\tReplicationFactor: %s\tConfigs: \n' "$t" "$pc" "$(cat "$BROKER/$t/rf")"
    done ;;
  create)
    [ -d "$BROKER/$topic" ] && exit 0
    if [ "$rf" -gt "${BROKERS:-3}" ]; then
      echo "Error: Replication factor: $rf larger than available brokers: ${BROKERS:-3}." >&2; exit 1
    fi
    mkdir -p "$BROKER/$topic"; echo "$rf" > "$BROKER/$topic/rf"
    i=0; while [ "$i" -lt "$parts" ]; do : > "$BROKER/$topic/p$i"; i=$((i + 1)); done ;;
  delete) for d in "$BROKER"/*/; do t="${d%/}"; t="${t##*/}"; match "$t" && rm -rf "$BROKER/$t"; done ;;
esac
exit 0
STUB

  # FAULT_SKIP_PARTITION=<topic>:<p> reproduces a partition lost to a leader
  # election: GetOffsetShell prints the others, logs the skip and exits 0.
  cat > "$BINDIR/kafka-get-offsets.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
topic=""; when=""
while [ $# -gt 0 ]; do
  case "$1" in --topic) topic="$2"; shift ;; --time) when="$2"; shift ;; --bootstrap-server) shift ;; esac
  shift
done
case "$topic" in '\Q'*'\E') lit="${topic#\\Q}"; lit="${lit%\\E}" ;; *) lit="" ;; esac
for d in "$BROKER"/*/; do t="${d%/}"; t="${t##*/}"
  if [ -n "$lit" ]; then [ "$t" = "$lit" ] || continue
  else printf '%s' "$t" | grep -Eqx -- "$topic" || continue; fi
  for f in "$BROKER/$t"/p*; do p="${f##*/p}"
    if [ "${FAULT_SKIP_PARTITION:-}" = "$t:$p" ]; then
      echo "Skip getting offsets for topic-partition $t-$p due to error: leader not available" >&2; continue
    fi
    begin=0; [ -f "$BROKER/$t/begin$p" ] && begin=$(cat "$BROKER/$t/begin$p")
    end=$((begin + $(wc -l < "$f")))
    if [ "$when" = -1 ]; then echo "$t:$p:$end"; else echo "$t:$p:$begin"; fi
  done
done
STUB

  # FAULT_SHORT_READ=<n> caps every drain at n lines, the way a --timeout-ms
  # expiry does, and still exits 0.
  cat > "$BINDIR/kafka-console-consumer.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
topic=""; p=""; off=0; max=0
while [ $# -gt 0 ]; do
  case "$1" in
    --topic) topic="$2"; shift ;; --partition) p="$2"; shift ;; --offset) off="$2"; shift ;;
    --max-messages) max="$2"; shift ;; --bootstrap-server|--timeout-ms|--property) shift ;;
  esac
  shift
done
begin=0; [ -f "$BROKER/$topic/begin$p" ] && begin=$(cat "$BROKER/$topic/begin$p")
tail -n +$((off - begin + 1)) "$BROKER/$topic/p$p" | head -n "${FAULT_SHORT_READ:-$max}"
STUB

  # FAULT_DROP_WRITES=1 is a broker rejecting every acks=all write: the
  # console producer logs each one and exits 0 regardless.
  cat > "$BINDIR/kafka-console-producer.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
topic=""
while [ $# -gt 0 ]; do
  case "$1" in --topic) topic="$2"; shift ;; --bootstrap-server|--property) shift ;; esac
  shift
done
[ -d "$BROKER/$topic" ] || { echo "no such topic $topic" >&2; exit 1; }
pc=$(ls "$BROKER/$topic" | grep -c '^p[0-9]*$')
while IFS= read -r line; do
  if [ "${FAULT_DROP_WRITES:-0}" = 1 ]; then
    echo "ERROR Error when sending message to topic $topic: NOT_ENOUGH_REPLICAS" >&2; continue
  fi
  key="${line%%	*}"
  h=$(printf '%s' "$key" | cksum | cut -d' ' -f1)
  printf '%s\n' "$line" >> "$BROKER/$topic/p$((h % pc))"
done
exit 0
STUB

  # MISR=<n> is the broker-level min.insync.replicas the effective topic
  # config reports through --describe --all.
  cat > "$BINDIR/kafka-configs.sh" <<'STUB'
#!/usr/bin/env bash
set -eu
printf '  min.insync.replicas=%s sensitive=false synonyms={DEFAULT_CONFIG:min.insync.replicas=%s}\n' "${MISR:-1}" "${MISR:-1}"
STUB

  # curl stub: records argv, PUT stores the body under the object key, GET
  # serves it back.
  cat > "$PATHDIR/curl" <<'STUB'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$STATE/curl_args"
up=""; out=""; url=""; prev=""
for a in "$@"; do
  case "$prev" in --upload-file) up="$a" ;; -o) out="$a" ;; esac
  case "$a" in http://*|https://*) url="$a" ;; esac
  prev="$a"
done
[ -n "$url" ] || { echo "curl stub: no url" >&2; exit 2; }
key="${url#*://}"; key="${key#*/}"
mkdir -p "$S3DIR/$(dirname "$key")"
if [ -n "$up" ]; then cp "$up" "$S3DIR/$key"; exit 0; fi
[ -f "$S3DIR/$key" ] || { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
cp "$S3DIR/$key" "$out"
STUB
  chmod +x "$BINDIR"/*.sh "$PATHDIR/curl"
}

# run_strategy <mode> [VAR=value ...]: runs the rendered script against the
# broker at $BROKER (default $STATE/broker); its output lands in $STATE/log.
# The explicit return keeps the script's exit code: hack/cozytest.sh appends a
# `return 0` to every function in this file.
run_strategy() {
  local mode="$1"; shift
  env STATE="$STATE" BROKER="${BROKER:-$STATE/broker}" S3DIR="$S3DIR" \
      BIN="$BINDIR" SCRATCH="$STATE/scratch" CA_FILE="$STATE/ca.crt" PATH="$PATHDIR:$PATH" \
      MODE="$mode" BOOTSTRAP="kafka-x-kafka-bootstrap.tenant-root.svc:9092" \
      S3_ENDPOINT="seaweedfs-s3.tenant-root.svc:8333" S3_REGION="us-east-1" S3_BUCKET="bkt" \
      AWS_ACCESS_KEY_ID="k" AWS_SECRET_ACCESS_KEY="s" \
      SRC_BACKUP="kafka-test" SRC_RESTORE="kafka-test" SRC_NAMESPACE="tenant-root" \
      TOPICS="${TOPICS-orders.v1}" REPLICATION_FACTOR="${REPLICATION_FACTOR-}" "$@" \
      bash "$STATE/strategy.sh" > "$STATE/log" 2>&1 || return $?
}

# seed_broker <dir>: orders.v1 with 3 partitions, rf=2 and 30 keyed records,
# plus the decoy orders-v1 (1 partition, rf=2, 5 records) that "orders.v1"
# matches as a regex and not as a literal.
seed_broker() {
  rm -rf "$1"; mkdir -p "$1/orders.v1" "$1/orders-v1"
  echo 2 > "$1/orders.v1/rf"; echo 2 > "$1/orders-v1/rf"
  : > "$1/orders.v1/p0"; : > "$1/orders.v1/p1"; : > "$1/orders.v1/p2"
  local i
  for i in $(seq 1 30); do printf 'k-%s\torder-%s\n' "$i" "$i" >> "$1/orders.v1/p$((i % 3))"; done
  for i in $(seq 1 5); do printf 'd-%s\tdecoy-%s\n' "$i" "$i"; done > "$1/orders-v1/p0"
}

# records <broker-dir> <topic>: record count over the topic's partitions.
records() {
  cat "$1/$2"/p* | wc -l | tr -d ' '
}

# backup_then_target: seeds a source, takes a backup into the fake S3, and
# leaves an empty target broker at $STATE/tgt for the restore under test.
backup_then_target() {
  seed_broker "$STATE/src"
  BROKER="$STATE/src" run_strategy backup
  rm -rf "$STATE/tgt"; mkdir -p "$STATE/tgt"
}

expect_fail() {
  if "$@"; then
    echo "expected a non-zero exit, got success; log:" >&2; cat "$STATE/log" >&2; return 1
  fi
  return 0
}

expect_log() {
  grep -qF -- "$1" "$STATE/log" || { echo "log lacks: $1" >&2; cat "$STATE/log" >&2; return 1; }
}

expect_no_log() {
  if grep -qF -- "$1" "$STATE/log"; then echo "log unexpectedly has: $1" >&2; cat "$STATE/log" >&2; return 1; fi
  return 0
}

# ---- backup ---------------------------------------------------------------

@test "backup records each topic's partition count and replication factor and leaves the regex sibling alone" {
  init_stubs; seed_broker "$STATE/broker"
  run_strategy backup
  expect_log "orders.v1: 3 partition(s), replication factor 2"
  mkdir -p "$STATE/x"; tar -C "$STATE/x" -xf "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar"
  [ "$(wc -l < "$STATE/x/manifest.txt" | tr -d ' ')" = 3 ]
  grep -qE '^orders\.v1 0 0 [0-9]+ 2$' "$STATE/x/manifest.txt"
  [ "$(cat "$STATE/x"/data-orders.v1-*.tsv | wc -l | tr -d ' ')" = 30 ]
  # A stripped \Q pins "orders.v1" to its regex meaning and pulls orders-v1 in.
  [ ! -e "$STATE/x/data-orders-v1-0.tsv" ]
  expect_no_log "orders-v1"
}

@test "backup refuses an offset listing short of the partition count and uploads nothing" {
  init_stubs; seed_broker "$STATE/broker"
  expect_fail run_strategy backup FAULT_SKIP_PARTITION=orders.v1:1
  expect_log "offset listing returned 2 of 3 partition(s) for orders.v1; refusing to upload an incomplete backup"
  [ ! -e "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar" ]
}

@test "backup refuses a partial drain and uploads nothing" {
  init_stubs; seed_broker "$STATE/broker"
  expect_fail run_strategy backup FAULT_SHORT_READ=1
  expect_log "partial drain on orders.v1:"
  [ ! -e "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar" ]
}

@test "backup refuses a duplicate, a glob and an illegal topic name" {
  init_stubs; seed_broker "$STATE/broker"
  expect_fail run_strategy backup TOPICS="orders.v1, orders.v1"
  expect_log "topic orders.v1 is listed more than once"
  # With globbing on, "*" would expand against the working directory and the
  # refusal would name whatever file it found instead of the parameter.
  expect_fail run_strategy backup TOPICS='*'
  expect_log "topic name '*' carries a character outside Kafka's"
  expect_fail run_strategy backup TOPICS='orders.v1 a;b'
  expect_log "topic name 'a;b' carries a character outside Kafka's"
  [ ! -e "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar" ]
}

@test "all-topics backup skips double-underscore internals and keeps single-underscore user topics" {
  init_stubs; seed_broker "$STATE/broker"
  mkdir -p "$STATE/broker/__consumer_offsets" "$STATE/broker/_schemas"
  echo 3 > "$STATE/broker/__consumer_offsets/rf"; : > "$STATE/broker/__consumer_offsets/p0"
  echo 2 > "$STATE/broker/_schemas/rf"; printf 's-1\tschema-1\n' > "$STATE/broker/_schemas/p0"
  run_strategy backup TOPICS=
  expect_log "backing up topics [ _schemas orders-v1 orders.v1]"
  mkdir -p "$STATE/x"; tar -C "$STATE/x" -xf "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar"
  [ -e "$STATE/x/data-_schemas-0.tsv" ]
  [ -e "$STATE/x/data-orders-v1-0.tsv" ]
  [ ! -e "$STATE/x/data-__consumer_offsets-0.tsv" ]
}

# ---- restore --------------------------------------------------------------

@test "restore recreates the topic with the captured replication factor and replays every record" {
  init_stubs; backup_then_target
  BROKER="$STATE/tgt" run_strategy restore MISR=2
  expect_log "replaying orders.v1"
  expect_log "orders.v1: restored 30 record(s)"
  expect_log "restore complete"
  [ "$(cat "$STATE/tgt/orders.v1/rf")" = 2 ]
  [ "$(records "$STATE/tgt" orders.v1)" = 30 ]
  # The replay re-partitions by key, so compare the record sets, not the files.
  cat "$STATE/src/orders.v1"/p* | sort > "$STATE/src.sorted"
  cat "$STATE/tgt/orders.v1"/p* | sort > "$STATE/tgt.sorted"
  diff "$STATE/src.sorted" "$STATE/tgt.sorted"
}

@test "replicationFactor overrides the captured value, and one the target cannot host fails at create" {
  init_stubs; backup_then_target
  BROKER="$STATE/tgt" run_strategy restore REPLICATION_FACTOR=1 BROKERS=1
  expect_log "restore complete"
  [ "$(cat "$STATE/tgt/orders.v1/rf")" = 1 ]
  rm -rf "$STATE/tgt"; mkdir -p "$STATE/tgt"
  BROKER="$STATE/tgt" expect_fail run_strategy restore BROKERS=1
  expect_log "larger than available brokers"
  expect_no_log "replaying"
}

@test "restore refuses a populated target before replaying anything" {
  init_stubs; backup_then_target
  BROKER="$STATE/tgt" run_strategy restore MISR=2
  [ "$(records "$STATE/tgt" orders.v1)" = 30 ]
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "topic orders.v1 already holds 30 record(s)"
  expect_no_log "replaying"
  [ "$(records "$STATE/tgt" orders.v1)" = 30 ]
}

@test "restore refuses a pre-replay listing short of the partition count instead of reading it as empty" {
  init_stubs; backup_then_target
  # A live topic whose records sit in one partition: with that partition
  # dropped from the listing, the surviving partitions sum to zero and only
  # the count guard stands between the replay and a second copy.
  mkdir -p "$STATE/tgt/orders.v1"; echo 2 > "$STATE/tgt/orders.v1/rf"
  : > "$STATE/tgt/orders.v1/p1"; : > "$STATE/tgt/orders.v1/p2"
  printf 'live-1\tx\nlive-2\tx\nlive-3\tx\n' > "$STATE/tgt/orders.v1/p0"
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2 FAULT_SKIP_PARTITION=orders.v1:0
  expect_log "offset listing returned 2 of 3 partition(s) for orders.v1; refusing to act on an incomplete listing"
  expect_no_log "replaying"
  [ "$(records "$STATE/tgt" orders.v1)" = 3 ]
}

@test "restore refuses a pre-existing topic with a different partition count or replication factor" {
  init_stubs; backup_then_target
  mkdir -p "$STATE/tgt/orders.v1"; echo 2 > "$STATE/tgt/orders.v1/rf"
  : > "$STATE/tgt/orders.v1/p0"; : > "$STATE/tgt/orders.v1/p1"
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "topic orders.v1 has 2 partition(s), backup recorded 3"
  rm -rf "$STATE/tgt"; mkdir -p "$STATE/tgt/orders.v1"; echo 1 > "$STATE/tgt/orders.v1/rf"
  : > "$STATE/tgt/orders.v1/p0"; : > "$STATE/tgt/orders.v1/p1"; : > "$STATE/tgt/orders.v1/p2"
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "topic orders.v1 has replication factor 1, restore expects 2"
  expect_no_log "replaying"
}

@test "restore refuses a replication factor below the broker's min.insync.replicas before replaying" {
  init_stubs; backup_then_target
  # A single-broker source's rf=1 backup restored into a two-broker target,
  # whose chart-level min.insync.replicas is 2.
  BROKER="$STATE/tgt" expect_fail run_strategy restore REPLICATION_FACTOR=1 MISR=2
  expect_log "topic orders.v1 has replication factor 1 but the broker requires min.insync.replicas=2"
  expect_log "Set replicationFactor to at least 2"
  expect_no_log "replaying"
  [ "$(records "$STATE/tgt" orders.v1)" = 0 ]
}

@test "restore fails on writes the broker rejected" {
  init_stubs; backup_then_target
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2 FAULT_DROP_WRITES=1
  expect_log "restore verification failed for orders.v1: topic holds 0 record(s), backup recorded 30"
}

# repack <edit-command>: unpacks the uploaded tarball, runs the edit inside it,
# packs it back, and resets the target - the untrusted-tarball cases.
repack() {
  rm -rf "$STATE/x"; mkdir -p "$STATE/x"
  tar -C "$STATE/x" -xf "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar"
  (cd "$STATE/x" && eval "$1")
  tar -C "$STATE/x" -cf "$S3DIR/bkt/tenant-root/kafka-test/kafka-topics.tar" .
  rm -rf "$STATE/tgt"; mkdir -p "$STATE/tgt"
}

@test "restore refuses a tarball whose manifest and data files disagree, before touching the target" {
  init_stubs; backup_then_target
  repack 'cp data-orders.v1-0.tsv data-orders.v1-9.tsv'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "data file data-orders.v1-9.tsv, which the manifest does not list"
  repack 'rm data-orders.v1-9.tsv data-orders.v1-1.tsv'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "but carries no data file for it"
  [ ! -d "$STATE/tgt/orders.v1" ]
  repack 'printf "orders.v1 0 0 10\n" > manifest.txt'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "non-numeric field ''"
  # Lists exactly the data files left by the previous repack (partitions 0
  # and 2), so the replication-factor disagreement is what fires.
  repack 'printf "orders.v1 0 0 10 2\norders.v1 2 0 10 1\n" > manifest.txt'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "manifest lists orders.v1 with replication factors 2 and 1"
  repack ': > manifest.txt'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "manifest is empty"
  # A topic name that is a glob would otherwise iterate the working directory.
  repack 'printf "* 0 0 0 1\n" > manifest.txt; rm -f data-*.tsv'
  BROKER="$STATE/tgt" expect_fail run_strategy restore MISR=2
  expect_log "topic name '*' carries a character outside Kafka's"
  expect_no_log "replaying"
  [ -z "$(ls "$STATE/tgt")" ]
}

# ---- S3 transport -----------------------------------------------------------

@test "S3 calls verify against the projected CA when present and never pass -k" {
  init_stubs; seed_broker "$STATE/broker"
  printf 'STUB PEM\n' > "$STATE/ca.crt"
  run_strategy backup
  grep -qF -- "--cacert $STATE/ca.crt --connect-to seaweedfs-s3.tenant-root.svc:443:seaweedfs-s3.tenant-root.svc:8333 --aws-sigv4" "$STATE/curl_args"
  rm -f "$STATE/ca.crt" "$STATE/curl_args"
  run_strategy backup
  grep -qF -- "-fsS --connect-to seaweedfs-s3.tenant-root.svc:443" "$STATE/curl_args"
  if grep -qE -- '(^| )-k( |$)|--cacert' "$STATE/curl_args"; then
    echo "unexpected TLS flags without a CA file: $(cat "$STATE/curl_args")" >&2; return 1
  fi
}
