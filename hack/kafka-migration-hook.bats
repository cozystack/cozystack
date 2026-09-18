#!/usr/bin/env bats
# Behavioural tests for the ZooKeeper->KRaft migration hook of
# packages/apps/kafka. The shell script is extracted from the rendered Job and
# executed against a stub kubectl, so the fail-closed reads, the atomic
# namespace-collision guard, the node-pool creation, the finalize guard and the
# "free the kafka name" drain are exercised for real. The helm-unittest cases in
# packages/apps/kafka/tests/migration-hook_test.yaml only match the script's
# source TEXT; they cannot say whether a stuck migration refuses to stamp
# kraft=enabled, or whether the drain drives the rebalance to completion before
# deleting the pool.
#
# The hook manages the node pools itself (no regular KafkaNodePool manifest), so
# it runs on install AND upgrade. `helm template` (no live lookup) resolves the
# broker pool to the fresh "b-<hash>" name; tests that must exercise the
# migration "kafka" pool (collision guard, create-CAS, name-freeing) force the
# resolved value with force_kafka_pool.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests). No
# setup/teardown, no EXIT traps: each @test builds its own fixture and removes it
# at the end of the body. cozytest.sh rewrites every line that is exactly `}`
# into `return 0` + `}`, the heredoc included, so the stub uses only case/if (no
# shell functions) and every helper's closing brace is indented.

CHART=packages/apps/kafka

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

# Force the render-resolved broker pool from "b-<hash>" to "kafka", so the
# collision / create-CAS / name-freeing branches (gated on brokerPool == "kafka")
# run. `helm template` cannot seed the live lookup that resolves it.
force_kafka_pool() {
  sed 's/brokerPool="b-[0-9a-f]\{8\}"/brokerPool="kafka"/' "$1" > "$1.k" && mv "$1.k" "$1"
  }

make_bin() {
  mkdir -p "$1"
  printf '#!/bin/sh\nexit 0\n' > "$1/sleep"
  chmod 0755 "$1/sleep"
  cat > "$1/kubectl" <<'STUB'
#!/bin/sh
args="$*"
echo "$args" >> "${KUBECTL_LOG:-/dev/null}"

# Order matters: match the more specific jsonpath reads before the generic
# owner/kraft/found reads that share substrings.
case "$args" in
  *"create -f -"*)
    cat >> "${KUBECTL_LOG:-/dev/null}"
    [ -n "${STUB_CREATE_FAIL:-}" ] && { echo "AlreadyExists" >&2; exit 1; }
    exit 0
    ;;
  *"apply -f -"*)
    cat >> "${KUBECTL_LOG:-/dev/null}"
    exit 0
    ;;
  *"range .items"*)
    # kafkas list for the "other ZooKeeper clusters remain" count.
    [ -n "${STUB_OTHERS_LIST_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    printf '%s\n' "${STUB_OTHERS_LIST:-}"
    exit 0
    ;;
  *nodeIds*)
    printf '%s' "${STUB_KAFKA_IDS:-}"
    exit 0
    ;;
  *].reason*)
    # KafkaRebalance NotReady reason (retriable vs terminal).
    printf '%s' "${STUB_REBALANCE_REASON:-}"
    exit 0
    ;;
  *conditions*)
    # KafkaRebalance status.conditions[*].type, advanced via STUB_REBALANCE.
    i=$(cat "${REBALANCE_COUNTER}" 2>/dev/null)
    [ -n "$i" ] || i=0
    i=$((i + 1))
    echo "$i" > "${REBALANCE_COUNTER}"
    printf '%s\n' "${STUB_REBALANCE:-}" | awk -v n="$i" '{a[NR]=$0} END{print (n<=NR)?a[n]:a[NR]}'
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

run_hook() {
  rc=0
  : > "$tmp/kubectl.log"
  : > "$tmp/state.counter"
  : > "$tmp/rebalance.counter"
  KUBECONFIG=/dev/null \
  KUBECTL_LOG="$tmp/kubectl.log" \
  STATE_COUNTER="$tmp/state.counter" \
  REBALANCE_COUNTER="$tmp/rebalance.counter" \
  STUB_FOUND="${STUB_FOUND:-}" \
  STUB_FOUND_FAIL="${STUB_FOUND_FAIL:-}" \
  STUB_KRAFT="${STUB_KRAFT:-}" \
  STUB_STATES="${STUB_STATES:-}" \
  STUB_OWNER="${STUB_OWNER:-}" \
  STUB_OWNER_FAIL="${STUB_OWNER_FAIL:-}" \
  STUB_KRAFT_FAIL="${STUB_KRAFT_FAIL:-}" \
  STUB_STATE_FAIL="${STUB_STATE_FAIL:-}" \
  STUB_CREATE_FAIL="${STUB_CREATE_FAIL:-}" \
  STUB_OTHERS_LIST="${STUB_OTHERS_LIST:-}" \
  STUB_OTHERS_LIST_FAIL="${STUB_OTHERS_LIST_FAIL:-}" \
  STUB_KAFKA_IDS="${STUB_KAFKA_IDS:-}" \
  STUB_REBALANCE="${STUB_REBALANCE:-}" \
  STUB_REBALANCE_REASON="${STUB_REBALANCE_REASON:-}" \
  PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" > "$tmp/out" 2>&1 || rc=$?
  [ -s "$tmp/kubectl.log" ] || return 1
  }

@test "a NotFound Kafka CR is a fresh install: pools ensured, no migration" {
  setup_case
  STUB_FOUND="" run_hook

  [ "$rc" = 0 ]
  grep -qF 'fresh install' "$tmp/out"
  if grep -qE 'kraft=(migration|enabled)|node-pools=enabled' "$tmp/kubectl.log"; then
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

  rm -rf "$tmp"
}

@test "an already-KRaft cluster on b-<hash> is a no-op with nothing to free" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" run_hook

  [ "$rc" = 0 ]
  grep -qF 'already KRaft' "$tmp/out"
  grep -qF 'nothing to free' "$tmp/out"
  if grep -qE 'kraft=(migration|enabled)|node-pools=enabled' "$tmp/kubectl.log"; then
    echo "FAIL: an already-KRaft cluster was driven through the state machine"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a kafka pool owned by another cluster fails fast before any mutation" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" \
    STUB_STATES="ZooKeeper" STUB_OWNER="other-kafka" run_hook

  [ "$rc" != 0 ]
  grep -qF 'already belongs to cluster' "$tmp/out"
  if grep -qE 'apply -f -|create -f -|kraft=|node-pools=' "$tmp/kubectl.log"; then
    echo "FAIL: a foreign-owned kafka pool did not abort before mutating"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed read of the kafka pool owner fails closed" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="" \
    STUB_STATES="ZooKeeper" STUB_OWNER_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'owner label failed' "$tmp/out"
  if grep -qE 'apply -f -|create -f -|kraft=|node-pools=' "$tmp/kubectl.log"; then
    echo "FAIL: proceeded to mutate the cluster after a failed owner read"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed read of the kraft annotation fails closed" {
  setup_case
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'strimzi.io/kraft annotation failed' "$tmp/out"
  if grep -qE 'kraft=(migration|enabled)|node-pools=enabled' "$tmp/kubectl.log"; then
    echo "FAIL: drove the migration after a failed kraft-annotation read"
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
  grep -qF 'kraft=enabled' "$tmp/kubectl.log"
  if grep -qF 'kraft=migration' "$tmp/kubectl.log"; then
    echo "FAIL: kraft=migration was re-issued from a post-ZooKeeper state"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

# --- Name-freeing (multi Kafka per namespace) -------------------------------

@test "the sole migrated cluster keeps the kafka pool, no drain" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  # Already KRaft on kafka; no other ZooKeeper clusters in the namespace.
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST="test-kafka=enabled" run_hook

  [ "$rc" = 0 ]
  grep -qF 'keeping the "kafka" pool' "$tmp/out"
  # No drain: no rebalance, no cruiseControl, no pool deletion.
  if grep -qE 'KafkaRebalance|cruiseControl|delete .*kafkanodepools' "$tmp/kubectl.log"; then
    echo "FAIL: a sole cluster ran the name-freeing drain"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed read of the other-clusters list fails closed, not mistaken for none" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  # Already KRaft on kafka; the list read that decides whether others wait errors.
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST_FAIL=1 run_hook

  [ "$rc" != 0 ]
  grep -qF 'could not list Kafka CRs' "$tmp/out"
  if grep -qF 'keeping the "kafka" pool' "$tmp/out"; then
    echo "FAIL: a failed list read was mistaken for 'no other clusters' and kept the pool"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a migrated cluster with others waiting drains and frees the kafka name" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  # Already KRaft on kafka; one other cluster still on ZooKeeper.
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST="test-kafka=enabled
other-zk=" \
    STUB_KAFKA_IDS="[0,1,2]" \
    STUB_REBALANCE="PendingProposal
ProposalReady
Ready" run_hook

  [ "$rc" = 0 ]
  grep -qF 'freeing the "kafka" pool name' "$tmp/out"
  grep -qF 'Freed the "kafka" pool name' "$tmp/out"
  # Full drain sequence, in order: target pool, Cruise Control, rebalance,
  # approval, delete kafka pool + PVCs.
  grep -qF '1000-1099' "$tmp/kubectl.log"   # applied the b-<hash> target pool
  grep -qF 'cruiseControl' "$tmp/kubectl.log"
  grep -qF 'rebalance=approve' "$tmp/kubectl.log"
  grep -qF 'delete kafkanodepools.kafka.strimzi.io kafka' "$tmp/kubectl.log"
  grep -qF 'delete pvc data-0-test-kafka-kafka-0' "$tmp/kubectl.log"
  grep -qF 'delete pvc data-0-test-kafka-kafka-2' "$tmp/kubectl.log"

  rm -rf "$tmp"
}

@test "the drain refuses to delete the kafka pool if the rebalance never completes" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST="test-kafka=enabled
other-zk=" \
    STUB_KAFKA_IDS="[0,1,2]" \
    STUB_REBALANCE="PendingProposal" run_hook

  [ "$rc" != 0 ]
  grep -qF 'will resume on the next retry' "$tmp/out"
  # Must NOT delete the pool (which still holds all the data) before the drain.
  if grep -qF 'delete kafkanodepools.kafka.strimzi.io kafka' "$tmp/kubectl.log"; then
    echo "FAIL: deleted the kafka pool before the rebalance finished — data loss"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

# Cruise Control rejects proposals transiently while it settles — the pod/cert
# are still coming up (retriable connect), or the capacity model over-estimates
# load from noisy samples and trips a goal. The drain must refresh the proposal
# and keep polling, never treat a NotReady as terminal. Covers both reasons.
@test "the drain refreshes a transient-connection NotReady and proceeds" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST="test-kafka=enabled
other-zk=" \
    STUB_KAFKA_IDS="[0,1,2]" \
    STUB_REBALANCE_REASON="CruiseControlRetriableConnectionException" \
    STUB_REBALANCE="PendingProposal
NotReady
ProposalReady
Ready" run_hook

  [ "$rc" = 0 ]
  grep -qF 'refreshing the proposal' "$tmp/out"
  grep -qF 'rebalance=refresh' "$tmp/kubectl.log"
  grep -qF 'Freed the "kafka" pool name' "$tmp/out"
  grep -qF 'delete kafkanodepools.kafka.strimzi.io kafka' "$tmp/kubectl.log"

  rm -rf "$tmp"
}

@test "the drain refreshes and recovers from a capacity-goal NotReady" {
  setup_case
  force_kafka_pool "$tmp/s.sh"
  STUB_FOUND="kafka.kafka.strimzi.io/test-kafka" STUB_KRAFT="enabled" \
    STUB_OTHERS_LIST="test-kafka=enabled
other-zk=" \
    STUB_KAFKA_IDS="[0,1,2]" \
    STUB_REBALANCE_REASON="OptimizationFailureException" \
    STUB_REBALANCE="PendingProposal
NotReady
ProposalReady
Ready" run_hook

  [ "$rc" = 0 ]
  grep -qF 'refreshing the proposal' "$tmp/out"
  grep -qF 'rebalance=refresh' "$tmp/kubectl.log"
  grep -qF 'Freed the "kafka" pool name' "$tmp/out"

  rm -rf "$tmp"
}
