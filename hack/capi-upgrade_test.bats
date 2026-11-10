#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
UPGRADE_SCRIPT="$REPO_ROOT/packages/system/capi-providers-core/files/upgrade-providers.sh"

setup() {
  strict_setup
  mkdir "$BATS_TEST_TMPDIR/bin"
  cp "$REPO_ROOT/hack/testdata/capi-upgrade/kubectl" "$BATS_TEST_TMPDIR/bin/kubectl"
  chmod +x "$BATS_TEST_TMPDIR/bin/kubectl"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
  export KUBECONFIG=/dev/null KUBE_CONTEXT=fixture NAMESPACE=test-capi
  export BRIDGE_VERSION=v1.13.8 TARGET_VERSION=v1.14.4
  export CAPI_UPGRADE_TIMEOUT_SECONDS=60 CAPI_UPGRADE_POLL_SECONDS=0
  export CAPI_TEST_CLOCK_JUMP="${CAPI_TEST_CLOCK_JUMP:-5}"
  export BASH_ENV="$REPO_ROOT/hack/testdata/capi-upgrade/clock.bash"
  export CAPI_TEST_STATE="$BATS_TEST_TMPDIR/state.json" CAPI_TEST_LOG="$BATS_TEST_TMPDIR/calls.jsonl"
  : > "$CAPI_TEST_LOG"
  jq --null-input '
    def provider($name; $suffix): {
      metadata: {name: $name, uid: $name, generation: 1},
      spec: {version: ("v1.10.1-" + $suffix), fetchConfig: {selector: {matchLabels: {($suffix + "-components"): "cozy"}}}},
      status: {installedVersion: ("v1.10.1-" + $suffix), observedGeneration: 1, conditions: [{type: "Ready", status: "True"}]}
    };
    def deployment: {
      metadata: {generation: 1}, spec: {replicas: 1},
      status: {observedGeneration: 1, replicas: 1, updatedReplicas: 1, readyReplicas: 1, availableReplicas: 1}
    };
    {
      providers: {"cluster-api": provider("cluster-api"; "core"), kubeadm: provider("kubeadm"; "bootstrap")},
      deployments: {"capi-controller-manager": deployment, "capi-kubeadm-bootstrap-controller-manager": deployment}
    }' > "$CAPI_TEST_STATE"
}

fixture() {
  jq "$1" "$CAPI_TEST_STATE" > "$BATS_TEST_TMPDIR/next.json"
  mv "$BATS_TEST_TMPDIR/next.json" "$CAPI_TEST_STATE"
}

accepted_patches() {
  jq --slurp --compact-output '[.[] | select(.accepted == true) | {name, version: (.patch[] | select(.path == "/spec/version" and .op == "replace") | .value)}]' "$CAPI_TEST_LOG"
}

@test "fresh installations do not create or patch providers" {
  fixture '.providers = {}'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[]' ]
}

@test "providers within the supported upgrade window are unchanged" {
  fixture '.providers["cluster-api"].spec.version = "v1.13.7-core" | .providers["cluster-api"].status.installedVersion = "v1.13.7-core" | .providers.kubeadm.spec.version = "v1.13.7-bootstrap" | .providers.kubeadm.status.installedVersion = "v1.13.7-bootstrap"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[]' ]
}

@test "legacy core and kubeadm converge on the intermediate release in order" {
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[{"name":"cluster-api","version":"v1.13.8-core"},{"name":"kubeadm","version":"v1.13.8-bootstrap"}]' ]
  [ "$(jq --raw-output '.providers.kubeadm.status.installedVersion' "$CAPI_TEST_STATE")" = v1.13.8-bootstrap ]
}

@test "bootstrap waits for the intermediate core deployment rollout" {
  fixture '.lagRollout = true'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(jq --raw-output '.deployments["capi-controller-manager"].status.readyReplicas' "$CAPI_TEST_STATE")" = 1 ]
  [ "$(jq --raw-output '.deployments["capi-kubeadm-bootstrap-controller-manager"].status.readyReplicas' "$CAPI_TEST_STATE")" = 1 ]
}

@test "an interrupted transition resumes without downgrading core" {
  fixture '.providers["cluster-api"].spec.version = "v1.13.8-core" | .providers["cluster-api"].spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] = "v1.13.8-core" | .providers["cluster-api"].status.installedVersion = "v1.13.8-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[{"name":"kubeadm","version":"v1.13.8-bootstrap"}]' ]
}

@test "a final core upgrade already in progress is not reset to the bridge" {
  fixture '.providers["cluster-api"].spec.version = "v1.14.4-core" | .providers["cluster-api"].status.installedVersion = "v1.13.8-core" | .pending = {"cluster-api": 2} | .providers["cluster-api"].spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] = "v1.14.4-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[{"name":"kubeadm","version":"v1.13.8-bootstrap"}]' ]
  [ "$(jq --raw-output '.providers["cluster-api"].status.installedVersion' "$CAPI_TEST_STATE")" = v1.14.4-core ]
}

@test "an API process cannot outlive the upgrade deadline" {
  fixture '.readDelay = 3'
  export CAPI_UPGRADE_TIMEOUT_SECONDS=1
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"timed out"* ]]
  [ "$(accepted_patches)" = '[]' ]
  [ "$(jq --raw-output '.delayedReadCompleted // false' "$CAPI_TEST_STATE")" = false ]
}

@test "a rollback of the requested core version resumes the installed bridge" {
  fixture '.providers["cluster-api"].status.installedVersion = "v1.13.8-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[{"name":"cluster-api","version":"v1.13.8-core"},{"name":"kubeadm","version":"v1.13.8-bootstrap"}]' ]
}

@test "a missing kubeadm provider is not installed by the migration" {
  fixture 'del(.providers.kubeadm)'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[{"name":"cluster-api","version":"v1.13.8-core"}]' ]
}

@test "an API read failure aborts before any mutation" {
  fixture '.failGet = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"API unavailable"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a rejected patch aborts rather than reporting an upgraded provider" {
  fixture '.failPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"patch refused"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "replacing a provider between read and patch fails the UID precondition" {
  fixture '.replaceUIDOnPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"JSON patch test failed"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a competing requested version is not overwritten by the migration" {
  fixture '.replaceVersionOnPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"JSON patch test failed"* ]]
  [ "$(accepted_patches)" = '[]' ]
  [ "$(jq --raw-output '.providers["cluster-api"].spec.version' "$CAPI_TEST_STATE")" = v1.12.4-core ]
}

@test "a newer installed minor cannot be downgraded by the chart" {
  fixture '.providers["cluster-api"].spec.version = "v1.15.0-core" | .providers["cluster-api"].status.installedVersion = "v1.15.0-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"downgrades are not supported"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a newer installed patch cannot be downgraded by the chart" {
  fixture '.providers["cluster-api"].spec.version = "v1.14.5-core" | .providers["cluster-api"].status.installedVersion = "v1.14.5-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"downgrades are not supported"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "malformed installed versions fail before either provider is changed" {
  fixture '.providers.kubeadm.status.installedVersion = "invalid"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"invalid installed version"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "an uninitialised provider is not mistaken for a fresh installation" {
  fixture 'del(.providers["cluster-api"].status.installedVersion)'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"installed version is not recorded"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "versions outside the intermediate upgrade window are rejected" {
  fixture '.providers["cluster-api"].status.installedVersion = "v1.9.0-core" | .providers["cluster-api"].spec.version = "v1.9.0-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"outside the intermediate upgrade window"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a different requested upgrade is not overwritten" {
  fixture '.providers["cluster-api"].spec.version = "v1.12.4-core"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"another upgrade is in progress"* ]]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a stalled intermediate rollout reaches a hard deadline" {
  fixture '.stalled = true'
  export CAPI_TEST_CLOCK_JUMP=61
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"timed out"* ]]
  [ "$(accepted_patches)" = '[{"name":"cluster-api","version":"v1.13.8-core"}]' ]
}

@test "bridge selection changes version and repository selector in one guarded patch" {
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  jq --slurp --exit-status 'all(.[] | select(.accepted); . as $call |
    any(.patch[]; .op == "test" and .path == "/metadata/uid") and
    any(.patch[]; .op == "test" and .path == "/spec/version") and
    any(.patch[]; .op == "test" and .path == "/spec/fetchConfig") and
    any(.patch[]; .path == "/spec/fetchConfig" and .op == "replace" and
      .value.selector.matchLabels["provider.cluster.x-k8s.io/version"] ==
        (if $call.name == "cluster-api" then "v1.13.8-core" else "v1.13.8-bootstrap" end)))' "$CAPI_TEST_LOG"
}

@test "already selected bridges repair stale selectors inside the supported window" {
  fixture '.providers |= with_entries(.value.spec.version = ("v1.13.8-" + (if .key == "cluster-api" then "core" else "bootstrap" end)) | .value.status.installedVersion = .value.spec.version)'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  jq --exit-status 'all(.providers[]; .spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] == .spec.version)' "$CAPI_TEST_STATE"
}

@test "already selected bridges repair missing fetchConfig without changing other spec fields" {
  fixture '.providers |= with_entries(.value.spec.version = ("v1.13.8-" + (if .key == "cluster-api" then "core" else "bootstrap" end)) | .value.status.installedVersion = .value.spec.version | .value.spec.deployment = {replicas: 2} | del(.value.spec.fetchConfig))'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  jq --exit-status 'all(.providers[]; .spec.deployment.replicas == 2 and .spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] == .spec.version)' "$CAPI_TEST_STATE"
  jq --slurp --exit-status 'all(.[] | select(.accepted); any(.patch[]; .op == "test" and .path == "/spec") and any(.patch[]; .op == "add" and .path == "/spec/fetchConfig"))' "$CAPI_TEST_LOG"
}

@test "a matching bridge selector is an idempotent no-op" {
  fixture '.providers |= with_entries(.value.spec.version = ("v1.13.8-" + (if .key == "cluster-api" then "core" else "bootstrap" end)) | .value.status.installedVersion = .value.spec.version | .value.spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] = .value.spec.version)'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(accepted_patches)" = '[]' ]
}

@test "a concurrent fetchConfig replacement rejects the whole version transition" {
  fixture '.replaceFetchConfigOnPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"JSON patch test failed"* ]]
  [ "$(accepted_patches)" = '[]' ]
  jq --exit-status '.providers["cluster-api"].spec | .version == "v1.10.1-core" and .fetchConfig.selector.matchLabels == {competing: "repository"}' "$CAPI_TEST_STATE"
}

@test "a concurrent fetchConfig creation rejects a stale add" {
  fixture 'del(.providers["cluster-api"].spec.fetchConfig) | .replaceFetchConfigOnPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"JSON patch test failed"* ]]
  [ "$(accepted_patches)" = '[]' ]
  jq --exit-status '.providers["cluster-api"].spec | .version == "v1.10.1-core" and .fetchConfig.selector.matchLabels == {competing: "repository"}' "$CAPI_TEST_STATE"
}

@test "a status-only concurrent update does not conflict with bridge selection" {
  fixture '.statusOnlyOnPatch = "cluster-api"'
  run bash "$UPGRADE_SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(jq --raw-output '.providers["cluster-api"].metadata.resourceVersion' "$CAPI_TEST_STATE")" = 2 ]
}

@test "the fake applies JSON Patch atomically when a later precondition fails" {
  run kubectl --context fixture --namespace test-capi --request-timeout=10s patch coreproviders.operator.cluster.x-k8s.io cluster-api --type=json --patch '[{"op":"replace","path":"/spec/version","value":"v1.13.8-core"},{"op":"test","path":"/metadata/uid","value":"wrong"}]'
  [ "$status" -ne 0 ]
  [ "$(jq --raw-output '.providers["cluster-api"].spec.version' "$CAPI_TEST_STATE")" = v1.10.1-core ]
}

@test "the fake does not repair a stale selector or synthesize readiness" {
  fixture '.pending = {"cluster-api": 0} | .providers["cluster-api"].spec.version = "v1.13.8-core" | .providers["cluster-api"].status.conditions = [{type: "Ready", status: "False"}]'
  run kubectl --context fixture --namespace test-capi --request-timeout=10s get coreproviders.operator.cluster.x-k8s.io cluster-api --output json
  [ "$status" -eq 0 ]
  jq --exit-status '.providers["cluster-api"] | .status.installedVersion == "v1.10.1-core" and .status.conditions[0].status == "False" and (.spec.fetchConfig.selector.matchLabels | has("provider.cluster.x-k8s.io/version") | not)' "$CAPI_TEST_STATE"
}
