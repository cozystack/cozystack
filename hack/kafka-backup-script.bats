#!/usr/bin/env bats
# Behavioural unit test for the Kafka topic-metadata strategy driver script
# (packages/system/backupstrategy-controller/files/kafka-backup.sh), the shell
# embedded verbatim into strategy-kafka-default.yaml.
#
# The chart's helm-unittest suite can only assert the rendered Strategy CR's
# shape (kind, name, artifactURITemplate); the driver script that does the
# actual backup/restore/cleanup work is otherwise unpinned, so a regression in
# it (a stripped \Q, a reinstated -k, a fail-open --list) passes the chart suite
# untouched. This test closes that gap the way the contract is really defined:
# it EXECUTES the script against stubbed kafka-topics.sh / kafka-configs.sh (BIN
# override) and a stubbed curl (on PATH), and asserts what the script does. It
# is not a template grep — a comment cannot satisfy it.
#
# Runner note: the repo's bats runner is hack/cozytest.sh, which knows only
# @test + bash — no setup()/teardown() and no run/$status/$output. So each test
# calls init_stubs itself, and assertions use plain command exit codes and
# $(...) capture (set -e makes a failed command fail the test).
#
# Each named guard below corresponds to a mutation the script must not survive:
# \Q literal-match on describe and alter, the --connect-to port workaround with
# no -k (keyed off the request host in both addressing modes), the fail-closed
# --list, the comma-value bracket grouping, the replication-factor divergence
# guard, and the cleanup delete's HTTP handling.

SCRIPT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)/packages/system/backupstrategy-controller/files/kafka-backup.sh"

# init_stubs builds a fresh workspace with stub kafka CLIs (the script's $BIN)
# and a stub curl (first on PATH), and exports the common env. It sets STATE,
# BINDIR, PATHDIR in the caller's shell — each @test runs in its own subshell,
# so calling it first gives that test an isolated sandbox.
init_stubs() {
  STATE="$(mktemp -d)"
  BINDIR="$STATE/bin"
  PATHDIR="$STATE/path"
  mkdir -p "$BINDIR" "$PATHDIR"

  cat > "$BINDIR/kafka-topics.sh" <<STUB
#!/usr/bin/env bash
set -eu
mode=""; topic=""; parts=""; excl=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --list) mode=list ;;
    --describe) mode=describe ;;
    --create) mode=create ;;
    --alter) mode=alter ;;
    --exclude-internal) excl=1 ;;
    --topic) topic="\$2"; shift ;;
    --partitions) parts="\$2"; shift ;;
  esac
  shift
done
if [ "\$mode" = list ]; then
  [ -n "\${LIST_FAIL:-}" ] && exit 3
  if [ -n "\$excl" ]; then
    grep -v '^__' "$STATE/topics" 2>/dev/null || true
  else
    cat "$STATE/topics" 2>/dev/null || true
  fi
  exit 0
fi
if [ "\$mode" = describe ]; then
  echo "\$topic" >> "$STATE/describe_args"
  name="\${topic#'\\Q'}"; name="\${name%'\\E'}"
  if [ "\$name" = "\$topic" ]; then
    # No \\Q...\\E wrapping: --topic is a Java regex, so emit every stored topic
    # whose name matches it as an ERE (the collision a stripped \\Q reintroduces).
    hit=0
    for f in "$STATE"/desc.*; do
      [ -e "\$f" ] || continue
      n="\${f##*/desc.}"
      if printf '%s\n' "\$n" | grep -qE "^\${name}\$"; then cat "\$f"; hit=1; fi
    done
    [ "\$hit" = 1 ] || exit 1
    exit 0
  fi
  [ -f "$STATE/desc.\$name" ] || exit 1
  cat "$STATE/desc.\$name"
  exit 0
fi
if [ "\$mode" = create ]; then echo "create \$topic \$parts" >> "$STATE/actions"; exit 0; fi
if [ "\$mode" = alter ]; then echo "alter \$topic \$parts" >> "$STATE/actions"; exit 0; fi
exit 0
STUB

  cat > "$BINDIR/kafka-configs.sh" <<STUB
#!/usr/bin/env bash
set -eu
mode=""; name=""; add=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --describe) mode=describe ;;
    --alter) mode=alter ;;
    --entity-name) name="\$2"; shift ;;
    --add-config) add="\$2"; shift ;;
  esac
  shift
done
if [ "\$mode" = describe ]; then
  [ -n "\${CFG_FAIL:-}" ] && exit 3
  cat "$STATE/cfg.\$name" 2>/dev/null || true; exit 0
fi
if [ "\$mode" = alter ]; then echo "\$name \$add" >> "$STATE/configs_applied"; exit 0; fi
exit 0
STUB

  # curl stub: records argv, simulates a tiny S3 (PUT stores the body, GET serves
  # it back), and prints an HTTP code for the -w cleanup DELETE.
  cat > "$PATHDIR/curl" <<STUB
#!/usr/bin/env bash
set -eu
printf '%s\n' "\$*" >> "$STATE/curl_args"
up=""; out=""; wfmt=""; prev=""; hasf=""
for a in "\$@"; do
  case "\$prev" in --upload-file) up="\$a" ;; -o) out="\$a" ;; -w) wfmt="\$a" ;; esac
  case "\$a" in -fsS|-f) hasf=1 ;; esac
  prev="\$a"
done
if [ -n "\$wfmt" ]; then echo "\${DELETE_CODE:-204}"; exit 0; fi
if [ -n "\$up" ]; then
  # PUT_FAIL simulates a 403 on upload: with -f curl errors (exit 22) and stores
  # nothing; without -f it would exit 0 and a bad backup would report success.
  if [ -n "\${PUT_FAIL:-}" ]; then
    [ -n "\$hasf" ] && exit 22
    exit 0
  fi
  cp "\$up" "$STATE/s3_object"; exit 0
fi
if [ -n "\$out" ]; then
  [ -f "$STATE/s3_object" ] || exit 22
  cp "$STATE/s3_object" "\$out"; exit 0
fi
exit 0
STUB
  chmod +x "$BINDIR"/*.sh "$PATHDIR/curl"

  export BIN="$BINDIR"
  export PATH="$PATHDIR:$PATH"
  export BOOTSTRAP="kafka-x-kafka-bootstrap.tenant-root.svc:9092"
  export S3_REGION="us-east-1"
  export S3_FORCE_PATH_STYLE="true"
  export AWS_ACCESS_KEY_ID="k"
  export AWS_SECRET_ACCESS_KEY="s"
  export ARTIFACT_URI="s3://bkt/ns/app/run/kafka-metadata.txt"
}

seed_three() {
  printf 'orders\naudit.events\naudit-events\n' > "$STATE/topics"
  printf 'Topic: orders\tPartitionCount: 3\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  printf 'Topic: audit.events\tPartitionCount: 2\tReplicationFactor: 1\n' > "$STATE/desc.audit.events"
  printf 'Topic: audit-events\tPartitionCount: 5\tReplicationFactor: 1\n' > "$STATE/desc.audit-events"
  printf '  retention.ms=1234567890000 sensitive=false\n' > "$STATE/cfg.orders"
}

# expect_fail <env-prefixed command...>: succeeds when the command exits non-zero.
expect_fail() {
  if "$@" >/dev/null 2>&1; then
    echo "expected a non-zero exit, got success" >&2; return 1
  fi
  return 0
}

# ---- backup ---------------------------------------------------------------

@test "backup records each colliding topic's own partition count (literal Q match)" {
  init_stubs; seed_three
  MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  # The uploaded object must carry each topic's true partition count. A stripped
  # \Q makes the describe of audit.events match audit-events too, taking the
  # wrong first line — this assertion goes red then.
  grep -qxF "$(printf 'T\torders\t3\t1')" "$STATE/s3_object"
  grep -qxF "$(printf 'T\taudit.events\t2\t1')" "$STATE/s3_object"
  grep -qxF "$(printf 'T\taudit-events\t5\t1')" "$STATE/s3_object"
  # And the describe was invoked with the literal \Q...\E wrapper.
  grep -qxF '\Qaudit.events\E' "$STATE/describe_args"
}

@test "backup fails closed when --list errors (no empty backup as success)" {
  init_stubs; seed_three
  expect_fail env MODE=backup LIST_FAIL=1 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/s3_object" ]
}

@test "backup stores a comma-valued config in its own tab field" {
  init_stubs
  printf 'orders\n' > "$STATE/topics"
  printf 'Topic: orders\tPartitionCount: 1\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  printf '  cleanup.policy=compact,delete sensitive=false\n' > "$STATE/cfg.orders"
  MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  grep -qxF "$(printf 'C\torders\tcleanup.policy\tcompact,delete')" "$STATE/s3_object"
}

# ---- S3 signing (curl 7.76.1 port workaround, no -k) -----------------------

@test "ported endpoint signs with --connect-to and never -k" {
  init_stubs; seed_three
  MODE=backup S3_ENDPOINT="https://s3.example.org:8333" bash "$SCRIPT"
  grep -qF -- '--connect-to s3.example.org:443:s3.example.org:8333' "$STATE/curl_args"
  # Positive form, not `! grep`: hack/cozytest.sh wraps each @test in `set -e`
  # + a trailing `return 0`, and bash exempts a `!`-negated command from errexit,
  # so a `! grep` guard can never fail under the runner CI uses (only real bats).
  if grep -qE -- '(^| )-k( |$)' "$STATE/curl_args"; then
    echo "regression: -k (insecure TLS) in curl argv: $(cat "$STATE/curl_args")" >&2; return 1
  fi
}

@test "default-port endpoint needs no --connect-to" {
  init_stubs; seed_three
  MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  # Positive form (see the -k note above): a `! grep` is vacuous under cozytest.
  if grep -qF -- '--connect-to' "$STATE/curl_args"; then
    echo "unexpected --connect-to on a default-port endpoint: $(cat "$STATE/curl_args")" >&2; return 1
  fi
}

@test "virtual-hosted ported endpoint keys --connect-to off the bucket host" {
  init_stubs; seed_three
  MODE=backup S3_FORCE_PATH_STYLE=false S3_ENDPOINT="https://s3.example.org:8333" bash "$SCRIPT"
  # HOST1 must be the request host (bkt.s3.example.org), else curl never
  # redirects and the request hits the wrong port.
  grep -qF -- '--connect-to bkt.s3.example.org:443:s3.example.org:8333' "$STATE/curl_args"
  grep -qF 'https://bkt.s3.example.org/ns/app/run/kafka-metadata.txt' "$STATE/curl_args"
}

# ---- restore --------------------------------------------------------------

write_backup_object() {
  printf 'T\torders\t3\t1\nC\torders\tcleanup.policy\tcompact,delete\n' > "$STATE/s3_object"
}

@test "restore of an absent topic creates it and brackets a comma-valued config" {
  init_stubs; write_backup_object
  : > "$STATE/topics"   # nothing live -> create path
  MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  grep -qxF "create orders 3" "$STATE/actions"
  # comma value must be bracket-grouped so kafka-configs does not split it.
  grep -qxF "orders cleanup.policy=[compact,delete]" "$STATE/configs_applied"
}

@test "restore fails loudly on a replication-factor mismatch" {
  init_stubs
  printf 'T\torders\t3\t2\n' > "$STATE/s3_object"   # backup wants RF 2
  printf 'orders\n' > "$STATE/topics"               # live exists
  printf 'Topic: orders\tPartitionCount: 3\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  out=""
  if out=$(MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT" 2>&1); then
    echo "restore unexpectedly succeeded: $out" >&2; return 1
  fi
  printf '%s\n' "$out" | grep -q "replication factor differs"
}

@test "restore aborts when the broker is unreachable (fail-closed --list)" {
  init_stubs; write_backup_object
  expect_fail env MODE=restore LIST_FAIL=1 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  # Must not have routed the unreachable broker into --create.
  [ ! -f "$STATE/actions" ]
}

@test "restore of an existing under-partitioned topic alters it with the literal-Q wrapper" {
  init_stubs
  printf 'T\tpay.events\t7\t1\n' > "$STATE/s3_object"   # backup wants 7 partitions
  printf 'pay.events\npay-events\n' > "$STATE/topics"    # both live -> the collision
  printf 'Topic: pay.events\tPartitionCount: 2\tReplicationFactor: 1\n' > "$STATE/desc.pay.events"
  printf 'Topic: pay-events\tPartitionCount: 2\tReplicationFactor: 1\n' > "$STATE/desc.pay-events"
  MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  # The alter must target the literal topic (\Q...\E). A stripped wrapper sends a
  # regex that re-partitions the colliding sibling permanently — the destructive
  # bug this guards. Dropping \Q on the restore --alter reddens here.
  grep -qxF 'alter \Qpay.events\E 7' "$STATE/actions"
}

@test "restore refuses to shrink partitions" {
  init_stubs
  printf 'T\torders\t2\t1\n' > "$STATE/s3_object"   # backup wants 2
  printf 'orders\n' > "$STATE/topics"
  printf 'Topic: orders\tPartitionCount: 5\tReplicationFactor: 1\n' > "$STATE/desc.orders"  # live 5 > 2
  out=""
  if out=$(MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT" 2>&1); then
    echo "restore unexpectedly succeeded: $out" >&2; return 1
  fi
  printf '%s\n' "$out" | grep -q 'Kafka cannot decrease partitions'
}

@test "backup fails closed when the S3 upload is rejected (curl -f)" {
  init_stubs; seed_three
  # PUT_FAIL simulates a 403; -fsS must turn that into a non-zero exit so the
  # backup does not report success against an object that never landed.
  expect_fail env MODE=backup PUT_FAIL=1 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/s3_object" ]
}

@test "backup excludes Kafka internal topics" {
  init_stubs
  printf 'orders\n__consumer_offsets\n' > "$STATE/topics"
  printf 'Topic: orders\tPartitionCount: 3\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  # No desc.__consumer_offsets on purpose: with --exclude-internal it is filtered
  # from --list; drop the flag and the backup instead tries to describe it and
  # fails — either way it must never land in the object.
  MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  grep -qxF "$(printf 'T\torders\t3\t1')" "$STATE/s3_object"
  if grep -qF '__consumer_offsets' "$STATE/s3_object"; then echo "internal topic captured" >&2; return 1; fi
}

# ---- cleanup --------------------------------------------------------------

@test "cleanup treats 204 as success" {
  init_stubs
  MODE=cleanup DELETE_CODE=204 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}

@test "cleanup treats a 500 as a retryable failure" {
  init_stubs
  expect_fail env MODE=cleanup DELETE_CODE=500 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}

@test "cleanup treats a 404 as success (idempotent delete)" {
  init_stubs
  # Dropping 404 from the success case routes it to the `*)` failure arm; this
  # would then requeue the delete forever against an already-gone object.
  MODE=cleanup DELETE_CODE=404 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}

# ---- fail-closed guards (each reddens when its guard is removed) -----------

@test "unknown MODE fails closed instead of running the restore branch" {
  init_stubs; write_backup_object
  : > "$STATE/topics"
  # internal/template returns a leaf unchanged when its render fails, so an
  # unrendered {{ .Mode }} reaches the pod literally; without the MODE case the
  # bare `else` would run the (mutating) restore branch during a backup. With a
  # valid object seeded, dropping the guard makes the script exit 0 here.
  expect_fail env MODE='{{ .Mode }}' S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/actions" ]
}

@test "non-s3 ARTIFACT_URI fails closed" {
  init_stubs; seed_three
  # Without the s3:// prefix guard the `${ARTIFACT_URI#s3://}` split produces a
  # garbage bucket/key and the backup uploads to the wrong place, exiting 0.
  expect_fail env MODE=backup ARTIFACT_URI="https://bkt/ns/app/run/x.txt" S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}

@test "backup fails closed on an unparsable topic describe" {
  init_stubs
  printf 'orders\n' > "$STATE/topics"
  printf 'Topic: orders no-partition-count-here\n' > "$STATE/desc.orders"
  # Empty parts/rf must abort, not write the record `T\torders\t\t` that a later
  # restore feeds to `--partitions ""`.
  expect_fail env MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/s3_object" ]
}

@test "backup fails closed when kafka-configs --describe errors" {
  init_stubs
  printf 'orders\n' > "$STATE/topics"
  printf 'Topic: orders\tPartitionCount: 1\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  # A configs describe that errors (not "no non-default configs") must abort, not
  # silently drop the topic's configs and report success.
  expect_fail env MODE=backup CFG_FAIL=1 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/s3_object" ]
}

@test "restore fails closed on an unparsable live describe" {
  init_stubs
  printf 'T\torders\t3\t1\n' > "$STATE/s3_object"   # backup wants 3 partitions, RF 1
  printf 'orders\n' > "$STATE/topics"               # live exists
  printf 'Topic: orders\tReplicationFactor: 1\n' > "$STATE/desc.orders"  # RF matches, no PartitionCount
  # An unparsable live describe must abort the restore, not report the topic
  # restored without altering it. (set -euo pipefail already aborts at the
  # `lp=$(describe_field ...)` assignment; the explicit guard is belt-and-braces.)
  expect_fail env MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/actions" ]
}

@test "restore fails closed on a non-numeric recorded partition count" {
  init_stubs
  printf 'T\torders\tabc\t1\n' > "$STATE/s3_object"   # recorded parts is not a number
  printf 'orders\n' > "$STATE/topics"                 # live exists at 3 partitions
  printf 'Topic: orders\tPartitionCount: 3\tReplicationFactor: 1\n' > "$STATE/desc.orders"
  # `[ 3 -lt abc ]` / `[ 3 -gt abc ]` exit 2 and an `if` condition is exempt from
  # errexit, so without the recorded-shape guard the topic is skipped yet the
  # script prints its success line and exits 0.
  expect_fail env MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/actions" ]
}

@test "restore fails closed on an unrecognised record kind" {
  init_stubs
  printf 'X\torders\t3\t1\n' > "$STATE/s3_object"   # neither T nor C
  : > "$STATE/topics"
  # A well-formed but wrong-content object (curl -f catches transport errors, not
  # a wrong-content 200) must not restore a subset and report success.
  expect_fail env MODE=restore S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  [ ! -f "$STATE/actions" ]
}
