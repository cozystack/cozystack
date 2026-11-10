#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CORE="$REPO_ROOT/packages/system/capi-providers-core"

setup() {
  strict_setup
  RENDERED="$BATS_TEST_TMPDIR/core.yaml"
  helm template capi-providers-core "$CORE" --namespace test-capi --kube-version 1.33.0 > "$RENDERED"
}

@test "the intermediate upgrade runs before Helm applies final provider manifests" {
  [ "$(yq --no-doc 'select(.kind == "Job") | .metadata.annotations."helm.sh/hook"' "$RENDERED")" = pre-install,pre-upgrade ]
  [ "$(yq --no-doc 'select(.kind == "Job") | .spec.backoffLimit' "$RENDERED")" = 0 ]
  [ "$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.restartPolicy' "$RENDERED")" = Never ]
  [ "$(yq --no-doc 'select(.kind == "CoreProvider") | .metadata.annotations."helm.sh/hook"' "$RENDERED")" = null ]
  local provider target
  provider="$(yq --no-doc 'select(.kind == "CoreProvider") | .spec.version' "$RENDERED")"
  target="$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].env[] | select(.name == "TARGET_VERSION") | .value' "$RENDERED")"
  [ "$provider" = "$target-core" ]
}

@test "the operator retains intermediate bundles independently of migration hooks" {
  local name bridge policy rendered="$BATS_TEST_TMPDIR/operator.yaml"
  bridge="$(< "$CORE/files/bridge-version")"
  [ "$bridge" = "$(< "$REPO_ROOT/packages/system/capi-operator/files/bridge-version")" ]
  helm template capi-operator "$REPO_ROOT/packages/system/capi-operator" --namespace test-capi > "$rendered"
  for name in "$bridge-core" "$bridge-bootstrap"; do
    policy="$(NAME="$name" yq --no-doc 'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME)) | .metadata.annotations."helm.sh/resource-policy"' "$rendered")"
    [ "$policy" = keep ]
    [ "$(NAME="$name" yq --no-doc 'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME)) | .metadata.annotations."helm.sh/hook"' "$rendered")" = null ]
    [ "$(NAME="$name" yq --no-doc 'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME)) | .metadata.annotations."provider.cluster.x-k8s.io/compressed"' "$rendered")" = true ]
  done
}

@test "the hook has no cluster-wide or secret permissions" {
  local roles
  roles="$(yq --output-format=json --no-doc 'select(.kind == "Role") | .rules' "$RENDERED")"
  [ -n "$roles" ]
  jq --exit-status 'length == 3 and all(.[];
    (.verbs - ["get", "patch"] | length == 0) and
    (.resourceNames | length > 0) and
    (.resources - ["coreproviders", "bootstrapproviders", "deployments"] | length == 0)
  )' <<< "$roles"
  [ -z "$(yq --no-doc 'select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding") | .kind' "$RENDERED")" ]
}

@test "the hook mounts its script and uses an explicit in-cluster kubeconfig" {
  local script image
  script="$(yq --no-doc 'select(.kind == "ConfigMap" and .metadata.name == "capi-providers-core-upgrade") | .data."upgrade-providers.sh"' "$RENDERED")"
  [ -n "$script" ]
  [ "$script" = "$(< "$CORE/files/upgrade-providers.sh")" ]
  [ "$(yq --output-format=json --indent 0 --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].command' "$RENDERED")" = '["bash","/scripts/upgrade-providers.sh"]' ]
  [ "$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.volumes[] | select(.name == "scripts") | .configMap.name' "$RENDERED")" = capi-providers-core-upgrade ]
  [ "$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "scripts") | .mountPath' "$RENDERED")" = /scripts ]
  [ "$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].env[] | select(.name == "KUBE_CONTEXT") | .value' "$RENDERED")" = capi-upgrade ]
  image="$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].image' "$RENDERED")"
  [[ "$image" =~ @sha256:[0-9a-f]{64}$ ]]
}

@test "hook RoleBinding namespace remains a string for YAML keyword names" {
  local rendered="$BATS_TEST_TMPDIR/keyword-namespace.yaml"
  helm template capi-providers-core "$CORE" --namespace true --kube-version 1.33.0 > "$rendered"
  [ "$(yq --no-doc 'select(.kind == "RoleBinding") | .subjects[0].namespace | tag' "$rendered")" = '!!str' ]
  [ "$(yq --no-doc 'select(.kind == "RoleBinding") | .subjects[0].namespace' "$rendered")" = true ]
}

@test "the job deadline leaves room beyond the script deadline" {
  local outer inner
  outer="$(yq --no-doc 'select(.kind == "Job") | .spec.activeDeadlineSeconds' "$RENDERED")"
  inner="$(yq --no-doc 'select(.kind == "Job") | .spec.template.spec.containers[0].env[] | select(.name == "CAPI_UPGRADE_TIMEOUT_SECONDS") | .value' "$RENDERED")"
  [ "$outer" -gt "$((inner + 10))" ]
  [ "$outer" -lt 600 ]
}

@test "CAPI releases leave headroom for Helm Secret storage" {
  local package chart manifest size budget=$((900 * 1024))
  for package in capi-operator capi-providers-core; do
    chart="$REPO_ROOT/packages/system/$package"
    manifest="$BATS_TEST_TMPDIR/$package-manifest.yaml"
    helm template "$package" "$chart" --namespace test-capi --kube-version 1.33.0 > "$manifest"
    size="$(python3 -I "$REPO_ROOT/hack/helm-release-size.py" "$chart" < "$manifest")"
    if [ "$size" -ge "$budget" ]; then
      printf '%s: %s bytes exceeds storage budget %s\n' "$package" "$size" "$budget" >&2
      return 1
    fi
  done
}

@test "every bootstrap variant waits for the core package migration" {
  local variants
  variants="$(yq --output-format=json '.spec.variants' "$REPO_ROOT/packages/core/platform/sources/capi-provider-bootstrap-kubeadm.yaml")"
  [ -n "$variants" ]
  jq --exit-status 'length > 0 and all(.[]; .dependsOn | index("cozystack.capi-provider-core") != null)' <<< "$variants"
}

render_repositories() {
  local package
  for package in capi-operator capi-providers-core capi-providers-bootstrap; do
    helm template "$package" "$REPO_ROOT/packages/system/$package" --namespace test-capi --kube-version 1.33.0
    printf '\n---\n'
  done | yq --output-format=json --no-doc '.' | jq --slurp '.' > "$BATS_TEST_TMPDIR/repositories.json"
}

repository_matches_request() {
  jq --exit-status --argjson count "${1:-2}" '
    . as $objects | [.[] | select(.kind == "CoreProvider" or (.kind == "BootstrapProvider" and .metadata.name == "kubeadm"))] |
    length == $count and all(.[]; . as $provider |
      [$objects[] | select(.kind == "ConfigMap") | select(.metadata.labels as $labels |
        $provider.spec.fetchConfig.selector.matchLabels | to_entries | all(.[]; $labels[.key] == .value))] |
      length == 1 and ((.[0].metadata.labels["provider.cluster.x-k8s.io/version"] // .[0].metadata.name) == $provider.spec.version))
  ' "$BATS_TEST_TMPDIR/repositories.json"
}

@test "combined final repositories select one ConfigMap at the requested effective version" {
  render_repositories
  repository_matches_request
}

@test "combined bridge repositories select one ConfigMap at the requested effective version" {
  render_repositories
  local bridge
  bridge="$(< "$CORE/files/bridge-version")"
  jq --arg bridge "$bridge" 'map(if .kind == "CoreProvider" or (.kind == "BootstrapProvider" and .metadata.name == "kubeadm") then
    .spec.version = ($bridge + (if .kind == "CoreProvider" then "-core" else "-bootstrap" end)) |
    .spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] = .spec.version
    else . end)' "$BATS_TEST_TMPDIR/repositories.json" > "$BATS_TEST_TMPDIR/bridge.json"
  mv "$BATS_TEST_TMPDIR/bridge.json" "$BATS_TEST_TMPDIR/repositories.json"
  repository_matches_request
}

@test "a unique repository with an unsuffixed native version is rejected" {
  render_repositories
  jq 'map(select(.kind != "BootstrapProvider") | if (.kind == "CoreProvider") then
      .spec.fetchConfig.selector.matchLabels = {"core-components": "cozy", "provider.cluster.x-k8s.io/version": "v1.14.4"}
    elif (.kind == "ConfigMap" and .metadata.name == "v1.14.4-core") then
      .metadata.labels["provider.cluster.x-k8s.io/version"] = "v1.14.4"
    else . end)' "$BATS_TEST_TMPDIR/repositories.json" > "$BATS_TEST_TMPDIR/negative.json"
  mv "$BATS_TEST_TMPDIR/negative.json" "$BATS_TEST_TMPDIR/repositories.json"
  jq --exit-status '[.[] | select(.kind == "ConfigMap" and .metadata.labels["core-components"] == "cozy" and .metadata.labels["provider.cluster.x-k8s.io/version"] == "v1.14.4")] | length == 1' "$BATS_TEST_TMPDIR/repositories.json"
  run repository_matches_request 1
  [ "$status" -ne 0 ]
}

@test "Helm final restoration changes the bridge version and selector together" {
  render_repositories
  jq --exit-status '[.[] | select(.kind == "CoreProvider" or (.kind == "BootstrapProvider" and .metadata.name == "kubeadm"))] |
    length == 2 and all(.[]; .spec.version == .spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"])' "$BATS_TEST_TMPDIR/repositories.json"
}
