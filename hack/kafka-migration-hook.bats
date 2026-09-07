#!/usr/bin/env bats
# Behavioural tests for the ZooKeeper->KRaft migration hook of
# packages/apps/kafka. The shell script is extracted from the rendered Job and
# executed against a stub kubectl, so the fail-closed reads, the atomic
# namespace-collision guard, the node-pool creation and the finalize guard are
# exercised for real. The helm-unittest cases in
# packages/apps/kafka/tests/migration-hook_test.yaml only match the script's
# source TEXT; they cannot say whether a stuck migration actually refuses to
# stamp kraft=enabled.
#
# The hook manages the node pools itself (there is no regular KafkaNodePool
# manifest), so it runs on install AND upgrade and creates/updates the pools on
# every run. `helm template` (no live lookup) resolves the broker pool to the
# fresh "b-<hash>" name; tests that must exercise the migration "kafka" pool
# force the resolved value with force_kafka_pool.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests). No
# setup/teardown, no EXIT traps: each @test builds its own fixture and removes it
# at the end of the body, so a failing run leaves its temp dir behind to read.
# cozytest.sh rewrites every line that is exactly `}` into `return 0` + `}`, the
# heredoc included, so the stub below uses only case/if (no shell functions) and
# every helper's closing brace is indented.

CHART=packages/apps/kafka

# Extract the migration Job's script into $1.
render_script() {
  helm template test-kafka "$CHART" \
    --namespace tenant-test \
    --show-only templates/migration-hook.yaml 2>"$1.err" |
    yq 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' - > "$1"
  if [ ! -s "$1" ]; then
    echo "FAIL: the chart rendered no migration Job script" >&2
    grep -v 'found symbolic link in path' "$1.err" >&2 || true
    return 1
  fi
  }

# Force the render-resolved broker pool from the fresh "b-<hash>" to the
# migration "kafka" name, so the namespace-collision / create-CAS branch (gated
# on brokerPool == "kafka") is exercised. `helm template` cannot seed the live
# lookup that would resolve it, so rewrite the assignment in the extracted script.
force_kafka_pool() {
  sed 's/brokerPool="b-[0-9a-f]\{8\}"/brokerPool="kafka"/' "$1" > "$1.k" && mv "$1.k" "$1"
  }

# A stub kubectl (and a no-op sleep so the finalize-timeout path runs instantly)
# in $1. The stub answers each probe; scenarios are driven by env vars.
# kafkaMetadataState advances through $STUB_STATES so a single run can return
# ZooKeeper, then KRaftPostMigration, then KRaft.
make_bin() {
  mkdir -p "$1"
  printf '#!/bin/sh\nexit 0\n' > "$1/sleep"
  chmod 0755 "$1/sleep"
  cat > "$1/kubectl" <<'STUB'
#!/bin/sh
args="$*"
echo "$args" >> "${KUBECTL_LOG:-/dev/null}"

# jsonpath reads are matched before the --ignore-not-found found-check, because
# the owner read also carries --ignore-not-found; it is distinguished by its
# jsonpath (...cluster}), the CR found-check by its bare "-o name".
case "$args" in
  *"create -f -"*)
    [ -n "${STUB_CREATE_FAIL:-}" ] && { echo "AlreadyExists" >&2; exit 1; }
    exit 0
    ;;
  *"apply -f -"*)
    exit 0
    ;;
  *kafkaMetadataState*)
    [ -n "${STUB_STATE_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    i=$(cat "${STATE_COUNTER}" 2>/dev/null)
    [ -n "$i" ] || i=0
    i=$((i + 1))
    echo "$i" > "${STATE_COUNTER}"
    printf '%s\n' "${STUB_STATES:-}" | awk -v n="$i" '{a[NR]=$0} END{print (n<=NR)?a[n]:a[NR]}'
    exit 0
    ;;
  *cluster}*)
    [ -n "${STUB_OWNER_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    printf '%s\n' "${STUB_OWNER:-}"
    exit 0
    ;;
  *kraft}*)
    [ -n "${STUB_KRAFT_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    printf '%s\n' "${STUB_KRAFT:-}"
    exit 0
    ;;
  *--ignore-not-found*)
    [ -n "${STUB_FOUND_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    printf '%s' "${STUB_FOUND:-}"
    [ -n "${STUB_FOUND:-}" ] && echo
    exit 0
    ;;
esac
exit 0
STUB
  chmod 0755 "$1/kubectl"
  }

setup_case() {
  tmp=$(mktemp -d) || return 1
  render_script "$tmp/s.sh" || return 1
  make_bin "$tmp/bin" || return 1
  }

# Run the hook with the scenario env vars already set in the caller, leaving
# merged output in $tmp/out, every kubectl invocation in $tmp/kubectl.log, and
# the exit status in $rc. KUBECONFIG is neutered so a stub lost from PATH cannot
# reach a real cluster.
run_hook() {
  rc=0
  : > "$tmp/kubectl.log"
  : > "$tmp/state.counter"
  KUBECONFIG=/dev/null \
  KUBECTL_LOG="$tmp/kubectl.log" \
  STATE_COUNTER="$tmp/state.counter" \
  STUB_FOUND="${STUB_FOUND:-}" \
  STUB_FOUND_FAIL="${STUB_FOUND_FAIL:-}" \
  STUB_KRAFT="${STUB_KRAFT:-}" \
  STUB_STATES="${STUB_STATES:-}" \
  STUB_OWNER="${STUB_OWNER:-}" \
  STUB_OWNER_FAIL="${STUB_OWNER_FAIL:-}" \
  STUB_KRAFT_FAIL="${STUB_KRAFT_FAIL:-}" \
  STUB_STATE_FAIL="${STUB_STATE_FAIL:-}" \
  STUB_CREATE_FAIL="${STUB_CREATE_FAIL:-}" \
  PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" > "$tmp/out" 2>&1 || rc=$?
  [ -s "$tmp/kubectl.log" ] || return 1
  }

@test "a NotFound Kafka CR is a fresh install: pools ensured, no migration" {
  setup_case
  STUB_FOUND="" run_hook

  [ "$rc" = 0 ]
  grep -qF 'fresh install' "$tmp/out"
  # A fresh install ensures the node pools exist but must not drive migration.
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: the fresh-install path drove the migration state machine"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a read error reading the Kafka CR fails closed, never mistaken for fresh" {
  setup_case
  STUB_FOUND_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'reading Kafka CR failed' "$tmp/out"
  if grep -qF 'fresh install' "$tmp/out"; then
    echo "FAIL: an apiserver error was read as a fresh install"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: a failed read still drove the migration"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a cluster already annotated kraft=enabled is not driven through migration" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" run_hook

  [ "$rc" = 0 ]
  grep -qF 'already KRaft' "$tmp/out"
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: an already-KRaft cluster was driven through the state machine"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a cluster already in kafkaMetadataState=KRaft is not driven through migration" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_STATES="KRaft" run_hook

  [ "$rc" = 0 ]
  grep -qF 'Already in KRaft' "$tmp/out"
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: a cluster already in KRaft was driven through the state machine"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a kafka pool owned by another cluster fails closed before any mutation" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" \
    STUB_STATES="ZooKeeper" STUB_OWNER="other-kafka" run_hook

  [ "$rc" != 0 ]
  grep -qF 'already belongs to cluster' "$tmp/out"
  # The atomic guard must fire before creating/applying/annotating anything.
  if grep -qE 'apply -f -|create -f -|annotate' "$tmp/kubectl.log"; then
    echo "FAIL: a foreign-owned kafka pool did not abort before mutating"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed read of the kafka pool owner fails closed, never waiving the collision check" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" \
    STUB_STATES="ZooKeeper" STUB_OWNER_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'owner label failed' "$tmp/out"
  if grep -qE 'apply -f -|create -f -|annotate' "$tmp/kubectl.log"; then
    echo "FAIL: proceeded to mutate the cluster after a failed owner read"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "an absent kafka pool is created via the atomic create, then migration proceeds" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_OWNER="" \
    STUB_STATES="ZooKeeper
KRaftPostMigration
KRaft" run_hook

  [ "$rc" = 0 ]
  grep -qF 'Creating broker pool "kafka"' "$tmp/out"
  grep -qF 'create -f -' "$tmp/kubectl.log"
  grep -qF 'Migration complete' "$tmp/out"

  rm -rf "$tmp"
}

@test "a failed read of the kraft annotation fails closed, never re-issuing kraft=migration" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'strimzi.io/kraft annotation failed' "$tmp/out"
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: drove the migration after a failed kraft-annotation read"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed read of kafkaMetadataState fails closed" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_STATE_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'kafkaMetadataState failed' "$tmp/out"
  if grep -qF 'annotate' "$tmp/kubectl.log"; then
    echo "FAIL: drove the migration after a failed kafkaMetadataState read"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a classic ZooKeeper cluster migrates and finalizes to KRaft" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_OWNER="" \
    STUB_STATES="ZooKeeper
KRaftPostMigration
KRaft" run_hook

  [ "$rc" = 0 ]
  grep -qF 'Migration complete' "$tmp/out"
  # It enables node pools, starts the migration, and only then finalizes.
  grep -qF 'node-pools=enabled' "$tmp/kubectl.log"
  grep -qF 'kraft=migration' "$tmp/kubectl.log"
  grep -qF 'kraft=enabled' "$tmp/kubectl.log"

  rm -rf "$tmp"
}

@test "a migration stuck before KRaftPostMigration refuses to stamp kraft=enabled" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_OWNER="" \
    STUB_STATES="ZooKeeper" run_hook

  [ "$rc" != 0 ]
  grep -qF 'Refusing to apply' "$tmp/out"
  # The finalize guard: kraft=enabled must never be applied from an unsafe state.
  if grep -qF 'kraft=enabled' "$tmp/kubectl.log"; then
    echo "FAIL: kraft=enabled was stamped while the cluster was still on ZooKeeper"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a retry already past the ZooKeeper phase does not re-issue kraft=migration" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" STUB_OWNER="" \
    STUB_STATES="KRaftPostMigration
KRaftPostMigration
KRaft" run_hook

  [ "$rc" = 0 ]
  grep -qF 'not re-issuing kraft=migration' "$tmp/out"
  grep -qF 'Migration complete' "$tmp/out"
  grep -qF 'kraft=enabled' "$tmp/kubectl.log"
  # Re-issuing kraft=migration from KRaftPostMigration is an invalid backward
  # transition Strimzi rejects, so the retry must not attempt it.
  if grep -qF 'kraft=migration' "$tmp/kubectl.log"; then
    echo "FAIL: kraft=migration was re-issued from a post-ZooKeeper state"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}
