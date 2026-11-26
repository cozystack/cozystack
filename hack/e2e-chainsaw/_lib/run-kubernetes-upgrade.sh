#!/usr/bin/env bash

cozy_worker_upgrade_required() {
  [ "${COZY_WORKER_UPGRADE_MODE:-off}" = required ]
}

cozy_worker_upgrade_budget() {
  local reserve=$1 deadline=${COZY_WORKER_UPGRADE_DEADLINE:-}
  case "$deadline" in ''|*[!0-9]*) echo "required upgrade deadline is missing or invalid" >&2; return 2 ;; esac
  if [ "$((deadline - $(date +%s)))" -lt "$reserve" ]; then
    echo "required upgrade has insufficient job headroom (${reserve}s needed); not run" >&2
    return 1
  fi
}

# The reserves below are read from every Test of the upgrade suite file, so an
# operation added to any of them or a changed ceiling moves them with it.
COZY_WORKER_UPGRADE_SUITE=${COZY_WORKER_UPGRADE_SUITE:-hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml}
COZY_CHAINSAW_CONFIG=${COZY_CHAINSAW_CONFIG:-hack/e2e-chainsaw/.chainsaw.yaml}
_cozy_worker_upgrade_secs='def secs: if test("^[0-9]+s$") then .[:-1] | tonumber elif test("^[0-9]+m$") then .[:-1] | tonumber * 60 elif test("^[0-9]+h$") then .[:-1] | tonumber * 3600 else error("unsupported duration \(.)") end;'

_cozy_worker_upgrade_test() {
  yq --output-format=json 'select(document_index == 0) | .spec.steps[0]' "$COZY_WORKER_UPGRADE_SUITE"
}

_cozy_worker_upgrade_cleanup() {
  yq --output-format=json '.spec.timeouts.cleanup' "$COZY_CHAINSAW_CONFIG" | jq --exit-status --raw-output "$_cozy_worker_upgrade_secs"'secs'
}

# jq definitions that price one Chainsaw operation: a script by its own
# timeout, an assert or apply by its own or the configured default. Any other
# operation, or a script without a timeout, fails the sum rather than counting
# as free.
_cozy_worker_upgrade_defs() {
  local apply assert
  apply=$(yq '.spec.timeouts.apply' "$COZY_CHAINSAW_CONFIG") || return 1
  assert=$(yq '.spec.timeouts.assert' "$COZY_CHAINSAW_CONFIG") || return 1
  printf '%s def opsecs: if .script then .script.timeout | secs elif .assert then (.assert.timeout // "%s") | secs elif .apply then (.apply.timeout // "%s") | secs else error("operation without a known ceiling: \(keys)") end; def runs($m): (.script.content // "") | contains($m);' \
    "$_cozy_worker_upgrade_secs" "$assert" "$apply"
}

# The other Tests of the suite file run in the same chainsaw invocation, in an
# order Chainsaw does not guarantee, so every reserve carries all of their
# operations and a cleanup for each.
_cozy_worker_upgrade_other_tests() {
  local cleanup defs
  cleanup=$(_cozy_worker_upgrade_cleanup) || return 1
  defs=$(_cozy_worker_upgrade_defs) || return 1
  yq --output-format=json 'select(document_index > 0 and .kind == "Test")' "$COZY_WORKER_UPGRADE_SUITE" \
    | jq --exit-status --slurp --raw-output --argjson cleanup "$cleanup" "$defs"'
      map([.spec.steps[] | (.try[]?, .finally[]?) | opsecs] | add // 0 | . + $cleanup) | add // 0'
}

# Seconds the required operations need from the one running <marker> through
# the teardown, the cleanup Chainsaw waits for once the test ends, and the
# other Tests of the file. The Kairos operation is not counted: it decides on
# its own headroom when it starts.
cozy_worker_upgrade_reserve_from() {
  local ops cleanup others defs
  defs=$(_cozy_worker_upgrade_defs) || return 1
  ops=$(_cozy_worker_upgrade_test | jq --exit-status --raw-output --arg marker "$1" "$defs"'
    .try as $ops | ($ops | map(runs($marker)) | index(true)) as $at
    | if $at == null then error("no operation runs \($marker)") else . end
    | ([$ops[$at:][] | select(runs("run_kairos_pool_test") | not) | opsecs] | add)
      + ([.finally[]? | opsecs] | add // 0)') || return 1
  cleanup=$(_cozy_worker_upgrade_cleanup) || return 1
  others=$(_cozy_worker_upgrade_other_tests) || return 1
  echo $(( ops + cleanup + others ))
}

cozy_worker_upgrade_kairos_ceiling() {
  local defs
  defs=$(_cozy_worker_upgrade_defs) || return 1
  _cozy_worker_upgrade_test | jq --exit-status --raw-output "$defs"'
    [.try[] | select(runs("run_kairos_pool_test"))]
    | if length == 1 then .[0].script.timeout | secs else error("want exactly one Kairos operation, found \(length)") end'
}

# The Kairos test's own bound: past its worst path, which the step comment adds
# up, yet early enough that the kill and the record land inside the ceiling.
cozy_worker_upgrade_kairos_bound() {
  local ceiling
  ceiling=$(cozy_worker_upgrade_kairos_ceiling) || return 1
  echo $(( ceiling - 20 ))
}

# The Kairos operation, the teardown after it, the cleanup Chainsaw waits for
# once the test ends, and the other Tests of the file, which may still be
# ahead of it.
cozy_worker_upgrade_kairos_reserve() {
  local ceiling teardown cleanup others defs
  ceiling=$(cozy_worker_upgrade_kairos_ceiling) || return 1
  defs=$(_cozy_worker_upgrade_defs) || return 1
  teardown=$(_cozy_worker_upgrade_test | jq --exit-status --raw-output "$defs"'[.finally[]? | opsecs] | add // 0') || return 1
  cleanup=$(_cozy_worker_upgrade_cleanup) || return 1
  others=$(_cozy_worker_upgrade_other_tests) || return 1
  echo $(( ceiling + teardown + cleanup + others ))
}

# A required upgrade runs the Kairos pool test only for a change to Kairos
# itself (COZY_WORKER_UPGRADE_KAIROS), and then gates the lane on it: missing
# headroom or a failure fails there. For any other change it does not run. It
# lives inside kubernetes-latest, whose teardown removes the cluster it joins,
# so it cannot wait until the candidate checks and the remaining suites are
# done, and running it first would spend the budget they need.
cozy_worker_upgrade_kairos_operation() {
  local need rc=0 record=_out/cozyreport/snapshots/worker-upgrade/kairos-operation.txt
  mkdir --parents "${record%/*}" || return 1
  if [ "${COZY_WORKER_UPGRADE_KAIROS:-off}" != required ]; then
    echo "skipped: not a Kairos change" > "$record"
    echo "Kairos pool test not run in the required upgrade run: not a Kairos change"
    return 0
  fi
  need=$(cozy_worker_upgrade_kairos_reserve) || return 1
  cozy_worker_upgrade_budget "$need" 2>/dev/null || rc=$?
  if [ "$rc" -eq 2 ]; then
    echo "required upgrade deadline is missing or invalid" >&2
    return 1
  fi
  if [ "$rc" -ne 0 ]; then
    echo "failed: insufficient headroom, less than ${need}s left for the Kairos pool test, the teardown and the other Tests of the suite file" > "$record"
    echo "::error title=kairos-upgrade::Kairos pool test not run in the required upgrade run of a Kairos change: insufficient headroom, less than ${need}s left for it, the teardown and the other Tests of the suite file"
    return 1
  fi
  rc=0
  cozy_worker_upgrade_kairos_bounded "$@" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "failed $rc" > "$record"
    echo "::error title=kairos-upgrade::Kairos pool test failed (exit $rc) in the required upgrade run of a Kairos change"
    return "$rc"
  fi
  echo passed > "$record"
}

# The PATH adapter registers native timeout groups, including POSIX sh calls,
# without changing their flags or individual deadlines. It only owns this
# phase's registered groups, not arbitrary sessions or absolute bypasses.
# Both asynchronous launchers need explicit stdin redirection: Bash otherwise
# substitutes /dev/null and a heredoc script can exit successfully without work.
cozy_worker_upgrade_timeout() (
  local duration=$1 rc=0 monitor='' waited=0 scope owner=0 native group pid pass started inherited=''
  shift
  [[ "$duration" =~ ^[1-9][0-9]*s$ ]] || { echo "invalid positive phase bound: $duration" >&2; return 2; }
  unset -f timeout
  native=${_COZY_TIMEOUT_BIN:-$(command -v timeout)}
  [ -x "$native" ] || return 127
  if [ -n "${_COZY_TIMEOUT_SCOPE:-}" ]; then
    scope=$_COZY_TIMEOUT_SCOPE
    [ -n "${_COZY_TIMEOUT_BIN:-}" ] && [ "$native" != "$scope/bin/timeout" ] && [ -x "$scope/bin/timeout" ] \
      && [ -d "$scope/groups" ] && [ "$(cat "$scope/state" 2>/dev/null)" = running ] || return 125
    inherited=" $(cd "$scope/groups" && echo *) "
  else
    scope=$(mktemp --directory) || return 1
    owner=1
  fi
  _cozy_timeout_cleanup() {
    # Stop admission before reading registrations, including children starting
    # while the outer timeout is already terminating its own group.
    if [ "$owner" -eq 1 ]; then rm --force "$scope/state"; fi
    if [ -n "$monitor" ] && [ "$waited" -eq 0 ]; then
      builtin kill -TERM "$monitor" 2>/dev/null || true
    fi
    # A registered launcher can be between admission and setpgid during the
    # first sweep. Its parent group is then stopped; the second catches its
    # completed transition without following unrelated process trees. A nested
    # bound sweeps only the groups registered after it started: an enclosing
    # bound's stdout is held by whatever ran inside this one.
    # ponytail: assumes nested bounds run one at a time; concurrent siblings
    # would sweep each other's groups.
    for pass in 1 2; do
      for group in "$scope/groups/"*; do
        [ -f "$group" ] || continue
        pid=${group##*/}
        case "$pid" in ''|*[!0-9]*) continue ;; esac
        [ "$pid" -gt 1 ] || continue
        case "$inherited" in *" $pid "*) continue ;; esac
        builtin kill -KILL -- "-$pid" 2>/dev/null || true
        [ "$pass" -eq 1 ] || rm --force "$group"
      done
    done
    if [ "$owner" -eq 1 ]; then
      rm --force "$scope/state" "$scope/bin/timeout"
      rmdir "$scope/groups" "$scope/bin" "$scope" 2>/dev/null || true
    fi
  }
  trap _cozy_timeout_cleanup EXIT
  trap 'exit 143' TERM
  trap 'exit 130' INT
  if [ "$owner" -eq 1 ]; then
    mkdir --parents "$scope/bin" "$scope/groups" || return 1
    printf '%s\n' running > "$scope/state" || return 1
    cat > "$scope/bin/timeout" <<'SCOPE_TIMEOUT' || return 1
#!/usr/bin/env bash
child=''
_scope_timeout_cleanup() {
  if [ -n "$child" ]; then
    builtin kill -KILL -- "-$child" 2>/dev/null || true
    rm --force "$_COZY_TIMEOUT_SCOPE/groups/$child"
  fi
}
trap _scope_timeout_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
bash --noprofile --norc -c '
  printf "%s\n" "$$" > "$_COZY_TIMEOUT_SCOPE/groups/$$" || exit 125
  [ "$(cat "$_COZY_TIMEOUT_SCOPE/state" 2>/dev/null)" = running ] || exit 125
  exec "$_COZY_TIMEOUT_BIN" "$@"
' _ "$@" <&0 &
child=$!
wait "$child"
status=$?
exit "$status"
SCOPE_TIMEOUT
    chmod +x "$scope/bin/timeout" || return 1
  fi
  export _COZY_TIMEOUT_BIN="$native" _COZY_TIMEOUT_SCOPE="$scope" PATH="$scope/bin:$PATH"
  started=$SECONDS
  "$scope/bin/timeout" --signal=KILL "$duration" "$@" <&0 &
  monitor=$!
  wait "$monitor" || rc=$?
  waited=1
  # Only this bound running out reads as 124. A KILL before it came from
  # somewhere else, the OOM killer for one, and keeps its status; a command's
  # own timeout before it is an ordinary failure. SECONDS, not date: tests
  # stand in for date.
  if [ $(( SECONDS - started )) -ge "${duration%s}" ]; then
    [ "$rc" -ne 137 ] || rc=124
  elif [ "$rc" -eq 124 ]; then
    echo "the command timed out on its own before its ${duration} bound" >&2
    rc=1
  fi
  return "$rc"
)

cozy_worker_upgrade_kairos_bounded() {
  local bound rc=0
  bound=$(cozy_worker_upgrade_kairos_bound) || return 1
  cozy_worker_upgrade_timeout "${bound}s" "$@" || rc=$?
  [ "$rc" -ne 124 ] || echo "Kairos pool test exceeded its ${bound}s bound" >&2
  return "$rc"
}

# The passing-path console capture is sized as the last work in its operation,
# and a required upgrade still has to record the preserved workers after it.
cozy_worker_upgrade_decline_green_capture() {
  COZY_GREEN_CAPTURE_RESERVE=$((COZY_OP_CEILING + 1))
}

cozy_worker_upgrade_preflight() {
  local reserve
  reserve=$(cozy_worker_upgrade_reserve_from cozy_worker_upgrade_preflight) || return 1
  cozy_worker_upgrade_budget "$reserve" || return 1
  bash hack/worker-upgrade-inputs.sh values _out/worker-upgrade-baseline/bundle.json >/dev/null || return 1
  [ -f _out/worker-upgrade-baseline/packages/core/installer/Chart.yaml ] || return 1
  COZY_UPGRADE_CONTEXT=$(yq --unwrapScalar '.current-context' "${KUBECONFIG:?}") || return 1
  export COZY_UPGRADE_CONTEXT
  local current expected registrations
  current=$(helm --kube-context "$COZY_UPGRADE_CONTEXT" get values installer --namespace cozy-system --output json) || return 1
  expected=$(bash hack/worker-upgrade-inputs.sh values _out/worker-upgrade-baseline/bundle.json) || return 1
  jq --exit-status --argjson expected "$expected" '.cozystackOperator as $actual | $expected.cozystackOperator | to_entries | all(.[]; .value == $actual[.key])' <<< "$current" >/dev/null || return 1
  registrations=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" get mutatingwebhookconfigurations,validatingwebhookconfigurations --output json) || return 1
  jq --exit-status '[.items[].webhooks[]? | select(any(.rules[]?.resources[]?; . == "kubernetesnodeses"))] | length == 0' <<< "$registrations" >/dev/null || {
    echo "baseline already has worker admission registrations" >&2
    return 1
  }
}

cozy_worker_upgrade_minor() {
  local baseline candidate minor
  baseline=$(yq --unwrapScalar 'keys | .[]' _out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/files/versions.yaml) || return 1
  candidate=$(yq --unwrapScalar 'keys | .[]' packages/apps/kubernetes-nodes/files/versions.yaml) || return 1
  minor=$(printf '%s\n' "$baseline" | grep --fixed-strings --line-regexp --file=<(printf '%s\n' "$candidate") | sort --version-sort | tail -n 1)
  [ -n "$minor" ] || { echo "baseline and candidate share no Kubernetes minor" >&2; return 1; }
  printf '%s\n' "$minor"
}

# kubernetes-latest runs the newest minor that the baseline and the candidate
# share, so a minor only the candidate offers would run in no suite;
# kubernetes-previous takes it instead of the second newest.
cozy_worker_upgrade_previous_version_expr() {
  local newest minor
  if cozy_worker_upgrade_required; then
    minor=$(cozy_worker_upgrade_minor) || return 1
    newest=$(yq --unwrapScalar 'keys | sort_by(.) | .[-1]' packages/apps/kubernetes/files/versions.yaml) || return 1
    if [ "$newest" != "$minor" ]; then
      echo 'keys | sort_by(.) | .[-1]'
      return 0
    fi
  fi
  echo 'keys | sort_by(.) | .[-2]'
}

cozy_worker_upgrade_create_custom() {
  local parent=$1 storage talos version schematic minor
  storage=$(cozy_e2e_storage_class) || return 1
  minor=$(cozy_worker_upgrade_minor) || return 1
  local values=_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/values.yaml
  # yq prints an absent key as the string "null"; it must not reach the pool.
  version=$(yq --exit-status --unwrapScalar '.talos.version | select(tag == "!!str" and . != "")' "$values") || { echo "baseline kubernetes-nodes values have no talos.version" >&2; return 1; }
  schematic=$(yq --exit-status --unwrapScalar '.talos.schematicID | select(tag == "!!str" and . != "")' "$values") || { echo "baseline kubernetes-nodes values have no talos.schematicID" >&2; return 1; }
  talos=$(talos_spec_block) || return 1
  [ -n "$talos" ] || talos='  talos:'
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test apply --filename - <<EOF || return 1
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata:
  name: ${parent}-custom
spec:
  cluster: ${parent}
${talos}
    version: ${version}
    schematicID: ${schematic}
    registryMirrors: {}
  version: ${minor}
  instanceType: ""
  minReplicas: 1
  maxReplicas: 1
  diskSize: 20Gi
  storageClass: ${storage}
  resources: {cpu: 1, memory: 4Gi}
  podCpuLimit: 2
  podCpuRequest: 100m
  kernelModules: []
  roles: [ingress-nginx]
  kubelet:
    systemReservedCpu: 100m
    kubeReservedCpu: 100m
EOF
}

cozy_worker_upgrade_tenant() {
  _cozy_kairos_names "$1"
  cozy_kairos_tenant_kubeconfig "$1" || return 1
  COZY_UPGRADE_TENANT_CONTEXT=$(yq --unwrapScalar '.current-context' "$KAIROS_KC") || return 1
  export COZY_UPGRADE_TENANT_CONTEXT
}

cozy_worker_upgrade_workloads() {
  local parent=$1 machines node pool
  for pool in md0 custom; do
    kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machinedeployments.v1beta1.cluster.x-k8s.io "kubernetes-${parent}-${pool}" >/dev/null || return 1
    kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test wait machinedeployments.v1beta1.cluster.x-k8s.io "kubernetes-${parent}-${pool}" --for=jsonpath='{.status.v1beta2.readyReplicas}'=1 --timeout=5m || return 1
  done
  # Read once both pools are ready: the custom pool may still be converging
  # before, and a Machine replaced during the waits would leave a stale node.
  machines=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machines.v1beta1.cluster.x-k8s.io --selector "cluster.x-k8s.io/cluster-name=kubernetes-${parent}" --output json) || return 1
  [ "$(jq '[.items[] | select(.status.nodeRef.name != null)] | length' <<< "$machines")" -eq 2 ] || return 1
  for pool in md0 custom; do
    node=$(jq --raw-output --arg md "kubernetes-${parent}-${pool}" '.items[] | select(.metadata.labels["cluster.x-k8s.io/deployment-name"] == $md) | .status.nodeRef.name' <<< "$machines") || return 1
    [ -n "$node" ] && [ "$node" != null ] || return 1
    kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" wait node "$node" --for condition=Ready --timeout=3m || return 1
    kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" apply --filename - <<EOF || return 1
apiVersion: v1
kind: Pod
metadata:
  name: worker-upgrade-${pool}
  namespace: default
spec:
  nodeSelector:
    kubernetes.io/hostname: ${node}
  containers:
  - name: nginx
    image: nginx:1.31.3-alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752
    readinessProbe:
      httpGet: {path: /, port: 80}
      periodSeconds: 2
EOF
    kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default wait pod "worker-upgrade-${pool}" --for condition=Ready --timeout=2m || return 1
    local assigned
    assigned=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default get pod "worker-upgrade-${pool}" --output 'jsonpath={.spec.nodeName}') || return 1
    [ "$assigned" = "$node" ] || { echo "upgrade workload for $pool did not schedule on its worker" >&2; return 1; }
  done
}

# Seconds the next snapshot read may wait: what is left of the snapshot's
# ceiling, at most the 20s of every other read here.
_cozy_worker_upgrade_snapshot_read_secs() {
  local left=$(( $1 - $(date +%s) ))
  if [ "$left" -le 0 ]; then
    echo "worker upgrade snapshot: ran out of its ${_cozy_worker_upgrade_snapshot_secs}s ceiling" >&2
    return 1
  fi
  [ "$left" -le 20 ] || left=20
  echo "$left"
}

cozy_worker_upgrade_snapshot() (
  set -euo pipefail
  local parent=$1 output=$2 kind object name template hash nodes node read
  local deadline=$(( $(date +%s) + _cozy_worker_upgrade_snapshot_secs ))
  mkdir --parents "$output" "$output-observations" || exit 1
  local selector="cluster.x-k8s.io/deployment-name in (kubernetes-${parent}-md0,kubernetes-${parent}-custom)"
  for kind in machinedeployments machines virtualmachines virtualmachineinstances; do
    local list_resource=$kind
    case "$kind" in machinedeployments|machines) list_resource="${kind}.v1beta1.cluster.x-k8s.io" ;; esac
    if [ "$kind" = machinedeployments ]; then
      # CAPI puts deployment-name on the selector and template only.
      read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
      object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get "$list_resource" --output json) || exit 1
      object=$(jq --arg default "kubernetes-${parent}-md0" --arg custom "kubernetes-${parent}-custom" '.items |= map(select(.metadata.name == $default or .metadata.name == $custom))' <<< "$object") || exit 1
    elif [ "$kind" = virtualmachines ]; then
      read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
      object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get "$list_resource" --output json) || exit 1
      object=$(jq --arg default "kubernetes-${parent}-md0" --arg custom "kubernetes-${parent}-custom" '.items |= map(select(.spec.template.metadata.labels["cluster.x-k8s.io/deployment-name"] as $name | $name == $default or $name == $custom))' <<< "$object") || exit 1
    else
      read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
      object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get "$list_resource" --selector "$selector" --output json) || exit 1
    fi
    jq --exit-status '.items | length == 2' <<< "$object" >/dev/null || {
      echo "worker upgrade snapshot: want the md0 and custom $kind, found $(jq '.items | length' <<< "$object")" >&2
      exit 1
    }
    local ready_filter
    case "$kind" in
      machines|virtualmachineinstances) ready_filter='all(.items[]; .status.phase == "Running")' ;;
      virtualmachines) ready_filter='all(.items[]; any(.status.conditions[]?; .type == "Ready" and .status == "True"))' ;;
      machinedeployments) ready_filter='all(.items[]; .status.v1beta2.readyReplicas == 1)' ;;
    esac
    if ! jq --exit-status "$ready_filter" <<< "$object" >/dev/null; then
      echo "worker upgrade snapshot: $kind are not ready" >&2
      jq '[.items[] | {name:.metadata.name,phase:.status.phase,readyReplicas:.status.v1beta2.readyReplicas,ready:[.status.conditions[]? | select(.type == "Ready") | .status]}]' <<< "$object" >&2
      exit 1
    fi
    jq --sort-keys '[.items[] | {kind, name:.metadata.name, uid:.metadata.uid, providerID:.spec.providerID, nodeRef:.status.nodeRef}] | sort_by(.name)' <<< "$object" > "$output/$kind.json" || exit 1
    hash=$(jq --sort-keys '[.items[] | {name:.metadata.name,spec:.spec}] | sort_by(.name)' <<< "$object" | sha256sum) || exit 1
    printf '%s\n' "${hash%% *}" > "$output/$kind-spec.sha256" || exit 1
  done
  for name in "$parent" "${parent}-md0" "${parent}-custom"; do
    kind=kubernetesnodeses.apps.cozystack.io
    [ "$name" != "$parent" ] || kind=kuberneteses.apps.cozystack.io
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get "$kind" "$name" --output json) || exit 1
    jq --exit-status '.spec | has("workerClassName") | not' <<< "$object" >/dev/null || exit 1
    hash=$(jq --sort-keys '.spec' <<< "$object" | sha256sum) || exit 1
    # GET defaults may disappear without changing stored Helm values or workers.
    printf '%s\n' "${hash%% *}" > "$output-observations/application-$name.sha256" || exit 1
    jq --sort-keys '{uid:.metadata.uid}' <<< "$object" > "$output/application-$name.json" || exit 1
    local hr="kubernetes-nodes-$name"
    [ "$name" != "$parent" ] || hr="kubernetes-$parent"
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get helmrelease "$hr" --output json) || exit 1
    jq --exit-status 'any(.status.conditions[]?; .type == "Ready" and .status == "True")' <<< "$object" >/dev/null || exit 1
    hash=$(jq --sort-keys '.spec.values' <<< "$object" | sha256sum) || exit 1
    jq --sort-keys --arg hash "${hash%% *}" '{uid:.metadata.uid,valuesSHA256:$hash}' <<< "$object" > "$output/release-$name.json" || exit 1
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    # The guard reads twice, three times on its failure path, and may add its
    # grace to each.
    COZY_DIAG_READ_TIMEOUT=$(( read > 3 ? (read - 2) / 2 : 1 )) COZY_DIAG_READ_GRACE=1 cozy_guard_helmrelease tenant-test "$hr" required || exit 1
  done
  for name in md0 custom; do
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machinedeployments.v1beta1.cluster.x-k8s.io "kubernetes-$parent-$name" --output json) || exit 1
    for kind in bootstrap infrastructure; do
      if [ "$kind" = bootstrap ]; then
        template=$(jq --raw-output '.spec.template.spec.bootstrap.configRef.name' <<< "$object") || exit 1
        local resource=talosconfigtemplates.bootstrap.cluster.x-k8s.io
      else
        template=$(jq --raw-output '.spec.template.spec.infrastructureRef.name' <<< "$object") || exit 1
        local resource=kubevirtmachinetemplates.infrastructure.cluster.x-k8s.io
      fi
      local raw uid
      read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
      raw=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get "$resource" "$template" --output json) || exit 1
      uid=$(jq --raw-output '.metadata.uid' <<< "$raw") || exit 1
      hash=$(jq --sort-keys '.spec' <<< "$raw" | sha256sum) || exit 1
      printf '%s %s %s\n' "$template" "$uid" "${hash%% *}" > "$output/$kind-$name.sha256" || exit 1
    done
  done
  read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
  nodes=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machines.v1beta1.cluster.x-k8s.io --selector "$selector" --output json) || exit 1
  : > "$output/nodes.jsonl" || exit 1
  for node in $(jq --raw-output '.items[].status.nodeRef.name' <<< "$nodes"); do
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" get node "$node" --output json) || exit 1
    jq --exit-status 'any(.status.conditions[]; .type == "Ready" and .status == "True") and (.spec.providerID | test("^kubevirt://.+"))' <<< "$object" >/dev/null || exit 1
    jq --compact-output --sort-keys '{name:.metadata.name,uid:.metadata.uid,providerID:.spec.providerID}' <<< "$object" >> "$output/nodes.jsonl" || exit 1
  done
  sort --output "$output/nodes.jsonl" "$output/nodes.jsonl" || exit 1
  for name in md0 custom; do
    read=$(_cozy_worker_upgrade_snapshot_read_secs "$deadline") || exit 1
    object=$(kubectl --request-timeout="${read}s" --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default get pod "worker-upgrade-$name" --output json) || exit 1
    jq --exit-status 'any(.status.conditions[]; .type == "Ready" and .status == "True") and (.spec.nodeName != null)' <<< "$object" >/dev/null || exit 1
    jq --sort-keys '{uid:.metadata.uid,nodeName:.spec.nodeName}' <<< "$object" > "$output/workload-$name.json" || exit 1
  done
)

cozy_worker_upgrade_before() {
  local parent=$1
  cozy_worker_upgrade_tenant "$parent" || return 1
  cozy_worker_upgrade_workloads "$parent" || return 1
  cozy_worker_upgrade_snapshot "$parent" _out/cozyreport/snapshots/worker-upgrade/before || return 1
  local hr object version
  : > _out/worker-upgrade-release-revisions.jsonl
  for hr in "kubernetes-$parent" "kubernetes-nodes-$parent-md0" "kubernetes-nodes-$parent-custom"; do
    object=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get helmrelease "$hr" --output json) || return 1
    version=$(jq --exit-status --raw-output '.status.history | map(.version) | max | select(type == "number" and . > 0)' <<< "$object") || return 1
    jq --null-input --compact-output --arg name "$hr" --argjson version "$version" '{name:$name,version:$version}' >> _out/worker-upgrade-release-revisions.jsonl || return 1
  done
  cp _out/worker-upgrade-release-revisions.jsonl _out/cozyreport/snapshots/worker-upgrade/actions-before.jsonl || return 1
  cozy_worker_upgrade_platform_before "$COZY_UPGRADE_CONTEXT" || return 1
  cozy_worker_upgrade_platform_history_before "$COZY_UPGRADE_CONTEXT" || return 1
  date +%s > _out/worker-upgrade-before-epoch
}

cozy_worker_upgrade_release_current() {
  jq --exit-status --argjson before "$1" '.status.observedGeneration == .metadata.generation and any(.status.conditions[]?; .type == "Ready" and .status == "True") and ([.status.history[]? | select(.status == "deployed") | .version] | max >= $before)' >/dev/null
}

# Match source-watcher's source-state hash, not the output chart digest:
# https://github.com/fluxcd/source-watcher/blob/789dbb933ea43fc12f55d0a05e13e23f3feac5f6/api/v1beta1/observed_source.go
# source-watcher does not set a controller ownerReference. Its sourceRef and
# generator UID label carry output provenance instead.
cozy_worker_upgrade_artifacts_current() {
  local list source observed repository hashes='{}'
  list=$(cat) || return 1
  [ -n "$1" ] || return 1
  for repository in cozystack-platform cozystack-packages; do
    if source=$(jq --exit-status --raw-output --arg revision "$1" --arg repository "$repository" '
      [.items[] | select(.kind == "OCIRepository" and .metadata.namespace == "cozy-system" and .metadata.name == $repository)]
      | select(length == 1) | .[0]
      | select(.status.observedGeneration == .metadata.generation and any(.status.conditions[]?; .type == "Ready" and .status == "True"))
      | .status.artifact | select(.revision | endswith($revision))
      | select((.digest | type == "string" and length > 0) and (.url | type == "string" and length > 0))
      | "\($repository)=[digest=\(.digest),revision=\(.revision),url=\(.url)]"' <<< "$list"); then
      observed=$(printf '%s' "$source" | sha256sum) || return 1
      hashes=$(jq --compact-output --arg repository "$repository" --arg observed "sha256:${observed%% *}" '. + {($repository):$observed}' <<< "$hashes") || return 1
    fi
  done
  jq --exit-status --argjson hashes "$hashes" --argjson refs "$2" '
    def one: if length == 1 then .[0] else null end;
    .items as $items
    | all($refs[];
      . as $ref
      | ([$items[] | select(.kind == "ExternalArtifact" and .metadata.namespace == $ref.namespace and .metadata.name == $ref.name)] | one) as $ea
      | $ea.spec.sourceRef as $producer
      | ([$items[] | select(.kind == "ArtifactGenerator" and .metadata.namespace == $producer.namespace and .metadata.name == $producer.name)] | one) as $ag
      | ($ag.spec.sources[0].name // "") as $repository
      | $ag != null and $ea != null and ($ag.metadata.uid | type == "string" and length > 0)
        and $producer.apiVersion == "source.extensions.fluxcd.io/v1beta1" and $producer.kind == "ArtifactGenerator" and $producer.namespace == $ref.namespace
        and $ea.metadata.labels["source.extensions.fluxcd.io/generator"] == $ag.metadata.uid
        and ($ref.producer == null or $producer.name == $ref.producer)
        and ($ref.source == null or $repository == $ref.source)
        and $ag.spec.sources == [{alias:$repository,kind:"OCIRepository",name:$repository,namespace:"cozy-system"}]
        and ($hashes[$repository] | type == "string") and $ag.status.observedSourcesDigest == $hashes[$repository]
        and any($ag.status.conditions[]?; .type == "Ready" and .status == "True" and .observedGeneration == $ag.metadata.generation)
        and ($ea.status.artifact.digest | type == "string" and length > 0)
        and any($ag.status.inventory[]?; .name == $ref.name and .namespace == $ref.namespace and .digest == $ea.status.artifact.digest))' <<< "$list" >/dev/null
}

cozy_worker_upgrade_app_artifacts_current() {
  cozy_worker_upgrade_artifacts_current "$1" '[
    {"name":"cozystack-kubernetes-application-kubevirt-kubernetes","namespace":"cozy-system","producer":"cozystack.kubernetes-application","source":"cozystack-packages"},
    {"name":"cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes","namespace":"cozy-system","producer":"cozystack.kubernetes-nodes-application","source":"cozystack-packages"}]'
}

# A stale Ready=True from the baseline satisfies `kubectl wait`, so a release
# counts only once it reconciled the artifact its source holds now. For a
# digest-bearing source helm-controller strips a semantic-version tag, but
# retains a non-semver revision token in lastAttemptedRevisionDigest. A
# HelmChart's version lands in lastAttemptedRevision. Suspended releases never
# reconcile. Tag classification follows helm-controller v1.5.0 and its
# Masterminds/semver v3.4.0 NewVersion parser with coercion enabled.
cozy_worker_upgrade_unconverged() {
  jq --raw-output '
    def uint64:
      sub("^0+"; "") | if . == "" then "0" else . end
      | length < 20 or (length == 20 and . <= "18446744073709551615");
    def semver_tag:
      ((gsub("_"; "+") | try capture("^v?(?<major>[0-9]+)(?:\\.(?<minor>[0-9]+))?(?:\\.(?<patch>[0-9]+))?(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$") catch null) // null) as $version
      | $version != null and ([$version.major, $version.minor // "0", $version.patch // "0"] | all(.[]; uint64));
    def digest:
      if contains(":") then
        if contains("@") and (split("@")[0] | semver_tag) then split("@")[1] else . end
      else null end;
    (.items | map(select(.kind != "HelmRelease") | {key: "\(.kind)/\(.metadata.namespace)/\(.metadata.name)", value: .status.artifact.revision}) | from_entries) as $sources
    | [.items[] | select(.kind == "HelmRelease" and .spec.suspend != true)
      | .metadata.namespace as $ns
      | (if .spec.chartRef then "\(.spec.chartRef.kind)/\(.spec.chartRef.namespace // $ns)/\(.spec.chartRef.name)" else "HelmChart/\(.status.helmChart // "")" end) as $key
      | $sources[$key] as $revision
      | select($revision == null
          or .status.observedGeneration != .metadata.generation
          or (any(.status.conditions[]?; .type == "Ready" and .status == "True") | not)
          or (if ($key | startswith("HelmChart/")) then .status.lastAttemptedRevision != ($revision | split("@")[0])
              else .status.lastAttemptedRevisionDigest != ($revision | digest) end))
      | "\($ns)/\(.metadata.name)"] | sort[]'
}

# Ceilings of the stages cozy_worker_upgrade_apply runs, in seconds. The
# operation's bound is their sum (cozy_worker_upgrade_apply_bound), and the
# stages here take their deadlines and timeouts only from these, which a test
# holds. The tenant connection's waits live in cozy_kairos_tenant_kubeconfig:
# its 60s Service delete, 90s address wait and 60s health wait, and three
# reads, priced here by hand. The convergence ceiling comes from one
# CI measurement: every platform HelmRelease upgrades or installs on the
# candidate artifacts through its dependsOn chain, which took about 600s; 900s
# is that with half again in hand.
_cozy_worker_upgrade_installer_secs=120
_cozy_worker_upgrade_operator_secs=300
_cozy_worker_upgrade_sources_secs=360
_cozy_worker_upgrade_converge_secs=900
_cozy_worker_upgrade_controller_secs=180
_cozy_worker_upgrade_releases_secs=300
_cozy_worker_upgrade_tenant_secs=270
_cozy_worker_upgrade_stable_gap_secs=30
_cozy_worker_upgrade_snapshot_secs=60

cozy_worker_upgrade_apply_bound() {
  local controllers
  controllers=$(cozy_worker_upgrade_controllers | wc -l) || return 1
  echo $(( _cozy_worker_upgrade_installer_secs + _cozy_worker_upgrade_operator_secs + _cozy_worker_upgrade_sources_secs + _cozy_worker_upgrade_converge_secs + controllers * _cozy_worker_upgrade_controller_secs + _cozy_worker_upgrade_releases_secs + _cozy_worker_upgrade_tenant_secs + _cozy_worker_upgrade_stable_gap_secs + 2 * _cozy_worker_upgrade_snapshot_secs ))
}

# Stalled is terminal: Flux does not retry the release, so waiting out a
# deadline only delays the failure.
cozy_worker_upgrade_stalled() {
  jq --raw-output '(.items // [.])[] | select(.kind == "HelmRelease" and .spec.suspend != true) | . as $hr | .status.conditions[]? | select(.type == "Stalled" and .status == "True") | "HelmRelease \($hr.metadata.namespace)/\($hr.metadata.name) stalled while applying generation \($hr.metadata.generation // "<unknown>"): \(.reason // ""): \(.message // "")"'
}

cozy_worker_upgrade_platform_converged() {
  local context=$1 candidate=${2:-} list pending stalled settled refs deadline=$(( $(date +%s) + _cozy_worker_upgrade_converge_secs ))
  while :; do
    list=$(kubectl --request-timeout=20s --context "$context" get helmreleases,externalartifacts,ocirepositories,helmcharts,artifactgenerators --all-namespaces --output json) || return 1
    stalled=$(cozy_worker_upgrade_stalled <<< "$list") || return 1
    [ -z "$stalled" ] || { printf '%s\n' "$stalled" >&2; return 1; }
    settled=$(cozy_worker_upgrade_settled_failures <<< "$list") || return 1
    [ -z "$settled" ] || { printf '%s\n' "$settled" >&2; return 1; }
    refs=$(jq --compact-output '[.items[] | select(.kind == "HelmRelease" and .spec.suspend != true and .spec.chartRef.kind == "ExternalArtifact") | {name:.spec.chartRef.name,namespace:(.spec.chartRef.namespace // .metadata.namespace)}] | unique' <<< "$list") || return 1
    if cozy_worker_upgrade_artifacts_current "$candidate" "$refs" <<< "$list"; then
      pending=$(cozy_worker_upgrade_unconverged <<< "$list") || return 1
    else
      pending="platform artifact producers have not consumed the candidate bundle"
    fi
    if [ -z "$pending" ]; then
      cozy_worker_upgrade_platform_histories <<< "$list"
      return
    fi
    [ "$(date +%s)" -lt "$deadline" ] || { printf 'HelmReleases not reconciled against their current source artifact:\n%s\n' "$pending" >&2; return 1; }
    sleep 5
  done
}

# A release that failed an action and recovered under RetryOnFailure is current
# and Ready; only its history shows the failure. Actions older than the upgrade
# belong to the baseline and are not judged.
cozy_worker_upgrade_platform_histories() {
  local found
  found=$(jq --raw-output --slurpfile before _out/worker-upgrade-platform-history-before.json '
    .items[] | select(.kind == "HelmRelease" and .spec.suspend != true)
    | "\(.metadata.namespace)/\(.metadata.name)" as $key
    | ($before[0][$key] // []) as $known
    | [.status.history[]? | select((.digest as $d | $known | index($d) | not) and (.status == "failed" or .status == "uninstalled"))] as $bad
    | select($bad | length > 0)
    | "\(if .spec.install.strategy.name == "RetryOnFailure" then "FAIL" else "NOTE" end) HelmRelease \($key) (install strategy \(.spec.install.strategy.name // "<unset>")) recorded \($bad | map("v\(.version) \(.status)") | join(", ")) after the upgrade began"') || return 1
  [ -z "$found" ] || printf '%s\n' "$found" >&2
  ! grep --quiet '^FAIL ' <<< "$found"
}

# The part of the history verdict that cannot change any more: a failed or
# uninstalled action with a later one above it. One still at the head of the
# history may be a first install that a retry rewrites to superseded.
cozy_worker_upgrade_settled_failures() {
  jq --raw-output --slurpfile before _out/worker-upgrade-platform-history-before.json '
    .items[] | select(.kind == "HelmRelease" and .spec.suspend != true and .spec.install.strategy.name == "RetryOnFailure")
    | "\(.metadata.namespace)/\(.metadata.name)" as $key
    | ($before[0][$key] // []) as $known
    | [(.status.history // [])[1:][] | select((.digest as $d | $known | index($d) | not) and (.status == "failed" or .status == "uninstalled"))] as $bad
    | select($bad | length > 0)
    | "FAIL HelmRelease \($key) (install strategy RetryOnFailure) recorded \($bad | map("v\(.version) \(.status)") | join(", ")) after the upgrade began"'
}

cozy_worker_upgrade_releases_ready() {
  local parent=$1 hr previous object stalled deadline=$(( $(date +%s) + _cozy_worker_upgrade_releases_secs ))
  : > _out/cozyreport/snapshots/worker-upgrade/actions-after.jsonl
  for hr in "kubernetes-$parent" "kubernetes-nodes-$parent-md0" "kubernetes-nodes-$parent-custom"; do
    previous=$(jq --slurp --exit-status --raw-output --arg name "$hr" '[.[] | select(.name == $name)] | select(length == 1) | .[0].version' _out/worker-upgrade-release-revisions.jsonl) || return 1
    while :; do
      object=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get helmrelease "$hr" --output json) || return 1
      stalled=$(cozy_worker_upgrade_stalled <<< "$object") || return 1
      [ -z "$stalled" ] || { printf '%s\n' "$stalled" >&2; return 1; }
      cozy_guard_helmrelease tenant-test "$hr" required || return 1
      if cozy_worker_upgrade_release_current "$previous" <<< "$object"; then
        jq --compact-output --sort-keys '{name:.metadata.name,lastAttemptedRevision:.status.lastAttemptedRevision,history:[.status.history[] | {version,status,chartVersion}]}' <<< "$object" >> _out/cozyreport/snapshots/worker-upgrade/actions-after.jsonl || return 1
        break
      fi
      [ "$(date +%s)" -lt "$deadline" ] || { echo "HelmRelease $hr has not completed an upgrade after baseline revision $previous" >&2; return 1; }
      sleep 2
    done
  done
}

cozy_worker_upgrade_controllers() {
  printf '%s\n' 'cozy-system kubernetes-worker-classes' 'cozy-cluster-api capi-kairos-bootstrap-controller-manager'
}

# cozy_worker_upgrade_roots_valid BEFORE_JSON < ROOTS_JSON. An intended
# controller the baseline already ran is not created by the upgrade, so only
# those absent from the snapshot taken before it are required among the roots.
cozy_worker_upgrade_roots_valid() {
  local expected
  expected=$(cozy_worker_upgrade_controllers | jq --null-input --raw-input '[inputs | split(" ") | {namespace:.[0], name:.[1]}]') || return 1
  jq --exit-status --argjson expected "$expected" --slurpfile before "$1" '. as $roots | ($before[0] | if type == "array" then . else error("snapshot is not a list") end) as $old | type == "array" and all(.[]; .uid | type == "string" and length > 0) and ([.[].uid] | unique | length) == length and all($expected[] | select(. as $e | [$old[] | select(.namespace == $e.namespace and .name == $e.name)] | length == 0); . as $e | [$roots[] | select(.namespace == $e.namespace and .name == $e.name)] | length == 1)' >/dev/null
}

cozy_worker_upgrade_platform_workloads() {
  local list
  list=$(kubectl --request-timeout=20s --context "$1" get deployments,statefulsets,daemonsets --all-namespaces --output json) || return 1
  jq --exit-status '[.items[] | select(.metadata.namespace | startswith("cozy-")) | {kind, name:.metadata.name, namespace:.metadata.namespace, uid:.metadata.uid}]' <<< "$list"
}

cozy_worker_upgrade_platform_before() {
  cozy_worker_upgrade_platform_workloads "$1" > _out/worker-upgrade-platform-before.json
}

# The Helm actions every platform release had before the upgrade, by Snapshot
# digest, so the history check judges only actions the candidate caused.
# Versions cannot mark the boundary: a reinstall clears the history and starts
# again from version 1.
cozy_worker_upgrade_platform_history_before() {
  local list
  list=$(kubectl --request-timeout=20s --context "$1" get helmreleases --all-namespaces --output json) || return 1
  jq --exit-status '[.items[] | {key:"\(.metadata.namespace)/\(.metadata.name)", value:[.status.history[]?.digest | select(. != null)]}] | from_entries' <<< "$list" > _out/worker-upgrade-platform-history-before.json
}

# The baseline is pinned while main moves, so the upgrade creates every
# platform workload main gained since that release, not only the ones this
# lane is about. All of them are the installation's, not the suite's.
cozy_worker_upgrade_platform_roots() {
  local after roots
  after=$(cozy_worker_upgrade_platform_workloads "$1") || return 1
  roots=$(jq --exit-status --slurpfile before _out/worker-upgrade-platform-before.json '($before[0] | map(.uid)) as $old | map(select(.uid as $uid | $old | index($uid) | not))' <<< "$after") || return 1
  cozy_worker_upgrade_roots_valid _out/worker-upgrade-platform-before.json <<< "$roots" || {
    printf 'the upgrade did not create every intended platform controller (%s); it created:\n%s\n' "$(cozy_worker_upgrade_controllers | paste -s -d, -)" "$roots" >&2
    return 1
  }
  printf '%s\n' "$roots" > _out/cozyreport/snapshots/worker-upgrade/platform-roots.json
}

cozy_worker_upgrade_candidate_ready() {
  local context=$1 digest=$2 deadline=$(( $(date +%s) + _cozy_worker_upgrade_sources_secs )) source object
  for source in cozystack-platform cozystack-packages; do
    while :; do
      object=$(kubectl --request-timeout=20s --context "$context" --namespace cozy-system get ocirepository "$source" --output json) || return 1
      if jq --exit-status --arg digest "$digest" '.status.observedGeneration == .metadata.generation and (.status.artifact.revision | endswith($digest)) and any(.status.conditions[]?; .type == "Ready" and .status == "True")' <<< "$object" >/dev/null; then
        break
      fi
      [ "$(date +%s)" -lt "$deadline" ] || { echo "candidate source $source did not converge" >&2; return 1; }
      sleep 2
    done
  done
  while :; do
    object=$(kubectl --request-timeout=20s --context "$context" get ocirepositories,artifactgenerators,externalartifacts --all-namespaces --output json) || return 1
    if cozy_worker_upgrade_app_artifacts_current "$digest" <<< "$object"; then break; fi
    [ "$(date +%s)" -lt "$deadline" ] || { echo "candidate app artifacts did not converge" >&2; return 1; }
    sleep 2
  done
}

# Run once the platform has converged: the releases behind these controllers
# wait on the platform's dependsOn chain. A provider Deployment comes from the
# CAPI operator reconciling the provider object the release applied, after
# the release is Ready, so each controller still gets its own short deadline.
cozy_worker_upgrade_controllers_ready() {
  local context=$1 namespace deployment object deadline left
  while read -r namespace deployment; do
    deadline=$(( $(date +%s) + _cozy_worker_upgrade_controller_secs ))
    while :; do
      object=$(kubectl --request-timeout=20s --context "$context" --namespace "$namespace" get deployments "$deployment" --ignore-not-found --output json) || return 1
      if [ -n "$object" ]; then break; fi
      [ "$(date +%s)" -lt "$deadline" ] || { echo "candidate deployment $namespace/$deployment is missing" >&2; return 1; }
      sleep 2
    done
    left=$(( deadline - $(date +%s) ))
    [ "$left" -gt 0 ] || left=1
    if ! kubectl --request-timeout=20s --context "$context" --namespace "$namespace" rollout status "deployment/$deployment" --timeout="${left}s"; then
      echo "candidate deployment $namespace/$deployment is not ready" >&2
      return 1
    fi
  done < <(cozy_worker_upgrade_controllers)
}

cozy_worker_upgrade_node_identity() {
  jq --exit-status '(.spec.providerID | test("^kubevirt://.+")) and .metadata.annotations["cluster.x-k8s.io/cluster-namespace"] == "tenant-test" and any(.status.conditions[]?; .type == "Ready" and .status == "True")' >/dev/null
}

# Ceilings of the default-class pool test, in seconds. Its Chainsaw step's
# inner timeout has to outlast their sum (cozy_worker_upgrade_default_bound)
# together with the tenant connection and the pool release wait, which a test
# holds.
_cozy_default_pool_machine_secs=60
_cozy_default_pool_running_secs=600
_cozy_default_pool_node_secs=180
_cozy_default_pool_probe_secs=120
_cozy_default_pool_probe_delete_secs=60
_cozy_default_pool_release_upgrade_secs=120
_cozy_default_pool_drain_secs=300

cozy_worker_upgrade_default_bound() {
  echo $(( _cozy_worker_upgrade_tenant_secs + KAIROS_RELEASE_WAIT + _cozy_default_pool_machine_secs + _cozy_default_pool_running_secs + _cozy_default_pool_node_secs + _cozy_default_pool_probe_secs + _cozy_default_pool_probe_delete_secs + _cozy_default_pool_release_upgrade_secs + _cozy_default_pool_drain_secs ))
}

cozy_worker_upgrade_default_node() {
  local parent=$1 selector="cluster.x-k8s.io/deployment-name=kubernetes-${1}-default-create" machines deadline=$(( $(date +%s) + _cozy_default_pool_machine_secs )) md node
  while :; do
    machines=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machines.v1beta1.cluster.x-k8s.io --selector "$selector" --output json) || return 1
    if jq --exit-status '.items | length == 1' <<< "$machines" >/dev/null; then break; fi
    [ "$(date +%s)" -lt "$deadline" ] || { echo "new default-class Machine was not created" >&2; return 1; }
    sleep 2
  done
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test wait machines.v1beta1.cluster.x-k8s.io --selector "$selector" --for=jsonpath='{.status.phase}'=Running --timeout="${_cozy_default_pool_running_secs}s" || return 1
  machines=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machines.v1beta1.cluster.x-k8s.io --selector "$selector" --output json) || return 1
  COZY_DEFAULT_CLASS_NODE=$(jq --exit-status --raw-output '.items[0].status.nodeRef.name | select(type == "string" and length > 0)' <<< "$machines") || return 1
  md=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machinedeployments.v1beta1.cluster.x-k8s.io "kubernetes-${parent}-default-create" --output json) || return 1
  jq --exit-status '.spec.template.spec.bootstrap.configRef.kind == "TalosConfigTemplate"' <<< "$md" >/dev/null || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" get node "$COZY_DEFAULT_CLASS_NODE" >/dev/null || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" wait node "$COZY_DEFAULT_CLASS_NODE" --for condition=Ready --timeout="${_cozy_default_pool_node_secs}s" || return 1
  node=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" get node "$COZY_DEFAULT_CLASS_NODE" --output json) || return 1
  cozy_worker_upgrade_node_identity <<< "$node" || return 1
  jq --sort-keys '{name:.metadata.name,uid:.metadata.uid,providerID:.spec.providerID,tenantNamespace:.metadata.annotations["cluster.x-k8s.io/cluster-namespace"]}' <<< "$node" > _out/cozyreport/snapshots/worker-upgrade/default-node.json || return 1
}

cozy_worker_upgrade_default_probe() {
  local node hostname pod
  node=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" get node "$COZY_DEFAULT_CLASS_NODE" --output json) || return 1
  hostname=$(jq --exit-status --raw-output '.metadata.labels["kubernetes.io/hostname"] | select(type == "string" and length > 0)' <<< "$node") || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default apply --filename - <<EOF || return 1
apiVersion: v1
kind: Pod
metadata:
  name: default-class-probe
spec:
  nodeSelector:
    kubernetes.io/hostname: ${hostname}
  containers:
  - name: nginx
    image: nginx:1.31.3-alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752
    readinessProbe:
      httpGet: {path: /, port: 80}
      periodSeconds: 2
EOF
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default wait pod default-class-probe --for condition=Ready --timeout="${_cozy_default_pool_probe_secs}s" || return 1
  pod=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default get pod default-class-probe --output json) || return 1
  jq --exit-status --arg node "$COZY_DEFAULT_CLASS_NODE" '.spec.nodeName == $node' <<< "$pod" >/dev/null || return 1
  jq --sort-keys '{uid:.metadata.uid,nodeName:.spec.nodeName,conditions:[.status.conditions[] | {type,status}]}' <<< "$pod" > _out/cozyreport/snapshots/worker-upgrade/default-workload.json
}

cozy_worker_upgrade_default_drain() {
  local parent=$1 selector="cluster.x-k8s.io/deployment-name=kubernetes-${1}-default-create" hr="kubernetes-nodes-${1}-default-create" previous vmis claims object machines vms pods deadline
  vmis=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get virtualmachineinstances --selector "$selector" --output json) || return 1
  jq --exit-status '.items | length == 1' <<< "$vmis" >/dev/null || return 1
  claims=$(jq '[.items[].spec.volumes[]? | .persistentVolumeClaim.claimName // .dataVolume.name // empty] | unique' <<< "$vmis") || return 1
  jq --exit-status 'length > 0' <<< "$claims" >/dev/null || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_TENANT_CONTEXT" --kubeconfig "$KAIROS_KC" --namespace default delete pod default-class-probe --wait=true --timeout="${_cozy_default_pool_probe_delete_secs}s" || return 1
  previous=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get helmrelease "$hr" --output 'jsonpath={.metadata.generation}') || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test patch kubernetesnodeses.apps.cozystack.io "${parent}-default-create" --type merge --patch '{"spec":{"minReplicas":0,"maxReplicas":0}}' || return 1
  cozy_wait_helmrelease_upgrade tenant-test "$hr" "$previous" "$_cozy_default_pool_release_upgrade_secs" || return 1
  cozy_guard_helmrelease tenant-test "$hr" required || return 1
  # The chart leaves replicas to the cluster-autoscaler, which removes a node
  # above the new maximum only once it has been unneeded for its scale-down
  # delay, minutes past this deadline; with the maximum at zero it will not
  # scale the pool back up.
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test scale machinedeployments.v1beta1.cluster.x-k8s.io "kubernetes-${parent}-default-create" --replicas=0 || return 1
  deadline=$(( $(date +%s) + _cozy_default_pool_drain_secs ))
  while :; do
    machines=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get machines.v1beta1.cluster.x-k8s.io --selector "$selector" --output json) || return 1
    vmis=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get virtualmachineinstances --selector "$selector" --output json) || return 1
    vms=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get virtualmachines --output json) || return 1
    pods=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get pods --selector "$selector" --output json) || return 1
    object=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get persistentvolumeclaims --output json) || return 1
    local machine_count vmi_count vm_count pod_count pvc_count
    machine_count=$(jq '.items | length' <<< "$machines") || return 1
    vmi_count=$(jq '.items | length' <<< "$vmis") || return 1
    vm_count=$(jq --arg md "kubernetes-${parent}-default-create" '[.items[] | select(.spec.template.metadata.labels["cluster.x-k8s.io/deployment-name"] == $md)] | length' <<< "$vms") || return 1
    pod_count=$(jq '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed")] | length' <<< "$pods") || return 1
    pvc_count=$(jq --argjson claims "$claims" '[.items[].metadata.name as $name | select($claims | index($name) != null)] | length' <<< "$object") || return 1
    if [ "$machine_count:$vmi_count:$vm_count:$pod_count:$pvc_count" = 0:0:0:0:0 ]; then
      printf '%s\n' drained > _out/cozyreport/snapshots/worker-upgrade/default-drained
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "new default-class guest or storage did not drain before Kairos: machines=$machine_count vmis=$vmi_count vms=$vm_count launchers=$pod_count claims=$pvc_count" >&2
      # Machine conditions name what holds the drain, such as a pod the
      # eviction is waiting on.
      jq --raw-output '.items[] | .metadata.name as $n | (.status.v1beta2.conditions // .status.conditions // [])[] | select(.status != "True") | "  \($n) \(.type): \(.message // .reason // "")"' <<< "$machines" >&2 || true
      return 1
    fi
    sleep 2
  done
}

cozy_worker_upgrade_default_test() {
  local parent=$1 reserve
  reserve=$(cozy_worker_upgrade_reserve_from cozy_worker_upgrade_default_test) || return 1
  cozy_worker_upgrade_budget "$reserve" || return 1
  COZY_UPGRADE_CONTEXT=$(yq --unwrapScalar '.current-context' "${KUBECONFIG:?}") || return 1
  export COZY_UPGRADE_CONTEXT
  cozy_worker_upgrade_tenant "$parent" || return 1
  cozy_worker_upgrade_default_create "$parent" || return 1
  cozy_worker_upgrade_default_node "$parent" || return 1
  cozy_worker_upgrade_default_probe || return 1
  cozy_worker_upgrade_default_drain "$parent"
}

cozy_worker_upgrade_default_create() {
  local parent=$1 storage object minor
  storage=$(cozy_e2e_storage_class) || return 1
  minor=$(cozy_worker_upgrade_minor) || return 1
  kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test apply --filename - <<EOF || return 1
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata:
  name: ${parent}-default-create
spec:
  cluster: ${parent}
  version: ${minor}
  minReplicas: 1
  maxReplicas: 1
  instanceType: ""
  resources: {cpu: 1, memory: 4Gi}
  storageClass: ${storage}
EOF
  object=$(kubectl --request-timeout=20s --context "$COZY_UPGRADE_CONTEXT" --namespace tenant-test get kubernetesnodeses.apps.cozystack.io "${parent}-default-create" --output json) || return 1
  jq --exit-status '.spec.workerClassName == "talos"' <<< "$object" >/dev/null || return 1
  cozy_wait_kairos_pool_release "kubernetes-nodes-${parent}-default-create" || return 1
  cozy_guard_helmrelease tenant-test "kubernetes-nodes-${parent}-default-create" required
}

# The stage a bounded operation is in, so a run stopped by its bound names
# the wait it was in rather than ending on a bare exit status.
cozy_worker_upgrade_stage() {
  mkdir --parents _out/cozyreport/snapshots/worker-upgrade || return 1
  printf '%s\n' "$1" > _out/cozyreport/snapshots/worker-upgrade/apply-stage || return 1
  echo "» worker upgrade: $1" >&2
}

# Runs a command under a bound in seconds; a run that hits it names its stage.
cozy_worker_upgrade_run_bounded() {
  local bound=$1 rc=0 stage
  shift
  cozy_worker_upgrade_timeout "${bound}s" "$@" || rc=$?
  if [ "$rc" -eq 124 ]; then
    stage=$(cat _out/cozyreport/snapshots/worker-upgrade/apply-stage 2>/dev/null) || stage="before its first stage"
    echo "::error title=worker-upgrade::the operation ran out of its ${bound}s bound during: ${stage}"
  fi
  return "$rc"
}

cozy_worker_upgrade_apply() {
  local parent=$1 context candidate
  context=$(yq --unwrapScalar '.current-context' "${KUBECONFIG:?}") || return 1
  COZY_UPGRADE_CONTEXT=$context
  export COZY_UPGRADE_CONTEXT
  candidate=_out/worker-upgrade-candidate-values.json
  yq --output-format=json '.cozystackOperator' packages/core/installer/values.yaml | jq '{cozystackOperator:{image,platformSourceUrl,platformSourceRef}}' > "$candidate" || return 1
  jq --exit-status '.cozystackOperator | (.image | test("@sha256:[0-9a-f]{64}$")) and (.platformSourceRef | test("^digest=sha256:[0-9a-f]{64}$"))' "$candidate" >/dev/null || return 1
  cozy_worker_upgrade_stage "installer upgrade" || return 1
  helm --kube-context "$context" upgrade installer packages/core/installer --namespace cozy-system --reset-then-reuse-values --values "$candidate" --timeout "${_cozy_worker_upgrade_installer_secs}s" || return 1
  cozy_worker_upgrade_stage "operator rollout" || return 1
  kubectl --request-timeout=20s --context "$context" --namespace cozy-system rollout status deployment/cozystack-operator --timeout="${_cozy_worker_upgrade_operator_secs}s" || return 1
  local digest
  digest=$(yq --unwrapScalar '.cozystackOperator.platformSourceRef' "$candidate") || return 1
  cozy_worker_upgrade_stage "candidate sources" || return 1
  cozy_worker_upgrade_candidate_ready "$context" "${digest#digest=}" || return 1
  cozy_worker_upgrade_stage "every HelmRelease on its current artifact" || return 1
  cozy_worker_upgrade_platform_converged "$context" "${digest#digest=}" || return 1
  cozy_worker_upgrade_stage "candidate controllers" || return 1
  cozy_worker_upgrade_controllers_ready "$context" || return 1
  cozy_worker_upgrade_stage "platform roots" || return 1
  cozy_worker_upgrade_platform_roots "$context" || return 1
  cozy_worker_upgrade_stage "tenant Helm actions" || return 1
  cozy_worker_upgrade_releases_ready "$parent" || return 1
  cozy_worker_upgrade_stage "tenant connection" || return 1
  cozy_worker_upgrade_tenant "$parent" || return 1
  cozy_worker_upgrade_stage "after snapshot" || return 1
  cozy_worker_upgrade_snapshot "$parent" _out/cozyreport/snapshots/worker-upgrade/after || return 1
  diff --recursive _out/cozyreport/snapshots/worker-upgrade/before _out/cozyreport/snapshots/worker-upgrade/after || return 1
  sleep "$_cozy_worker_upgrade_stable_gap_secs"
  cozy_worker_upgrade_stage "stable snapshot" || return 1
  cozy_worker_upgrade_snapshot "$parent" _out/cozyreport/snapshots/worker-upgrade/stable || return 1
  diff --recursive _out/cozyreport/snapshots/worker-upgrade/before _out/cozyreport/snapshots/worker-upgrade/stable || return 1
  local before_epoch
  before_epoch=$(cat _out/worker-upgrade-before-epoch) || return 1
  jq --null-input --argjson before "$before_epoch" --argjson after "$(date +%s)" --argjson gap "$_cozy_worker_upgrade_stable_gap_secs" '{beforeEpochUTC:$before,afterEpochUTC:$after,observationSeconds:($after-$before),stableSampleGapSeconds:$gap}' > _out/cozyreport/snapshots/worker-upgrade/window.json
  printf '%s\n' verified > _out/cozyreport/snapshots/worker-upgrade/verified
}
