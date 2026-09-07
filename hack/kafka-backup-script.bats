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
mode=""; topic=""; parts=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --list) mode=list ;;
    --describe) mode=describe ;;
    --create) mode=create ;;
    --alter) mode=alter ;;
    --topic) topic="\$2"; shift ;;
    --partitions) parts="\$2"; shift ;;
  esac
  shift
done
if [ "\$mode" = list ]; then
  [ -n "\${LIST_FAIL:-}" ] && exit 3
  cat "$STATE/topics" 2>/dev/null || true
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
if [ "\$mode" = describe ]; then cat "$STATE/cfg.\$name" 2>/dev/null || true; exit 0; fi
if [ "\$mode" = alter ]; then echo "\$name \$add" >> "$STATE/configs_applied"; exit 0; fi
exit 0
STUB

  # curl stub: records argv, simulates a tiny S3 (PUT stores the body, GET serves
  # it back), and prints an HTTP code for the -w cleanup DELETE.
  cat > "$PATHDIR/curl" <<STUB
#!/usr/bin/env bash
set -eu
printf '%s\n' "\$*" >> "$STATE/curl_args"
up=""; out=""; wfmt=""; prev=""
for a in "\$@"; do
  case "\$prev" in --upload-file) up="\$a" ;; -o) out="\$a" ;; -w) wfmt="\$a" ;; esac
  prev="\$a"
done
if [ -n "\$wfmt" ]; then echo "\${DELETE_CODE:-204}"; exit 0; fi
if [ -n "\$up" ]; then cp "\$up" "$STATE/s3_object"; exit 0; fi
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
  ! grep -qE -- '(^| )-k( |$)' "$STATE/curl_args"
}

@test "default-port endpoint needs no --connect-to" {
  init_stubs; seed_three
  MODE=backup S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
  ! grep -qF -- '--connect-to' "$STATE/curl_args"
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

# ---- cleanup --------------------------------------------------------------

@test "cleanup treats 204 as success" {
  init_stubs
  MODE=cleanup DELETE_CODE=204 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}

@test "cleanup treats a 500 as a retryable failure" {
  init_stubs
  expect_fail env MODE=cleanup DELETE_CODE=500 S3_ENDPOINT="https://s3.example.org" bash "$SCRIPT"
}
