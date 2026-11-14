#!/usr/bin/env bats
# The operator's resync period is what bounds the stale-Ready window after an
# operator restart (kubernetes-sigs/cluster-api-operator#1031): about two
# periods. It reaches the manager only through patches/sync-period.diff, which
# `make update` has to reapply on every re-vendor, and through the value in
# values.yaml. Losing either silently returns the window to about 20 minutes,
# so this checks the argument the rendered Deployment actually carries.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
OPERATOR="$REPO_ROOT/packages/system/capi-operator"

@test "the operator chart declares its vendored dependencies" {
  helm lint "$OPERATOR"
}

@test "the operator manager runs with a one-minute resync period" {
  local rendered args
  rendered="$BATS_TEST_TMPDIR/operator.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api > "$rendered"
  args="$(yq -N 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "manager") | .args[]' "$rendered")"
  [ -n "$args" ] || { echo "no manager container args rendered from $OPERATOR" >&2; return 1; }
  case "$(printf '%s\n' "$args" | grep -c '^--sync-period=')" in
    1) ;;
    *) echo "expected exactly one --sync-period in the manager args, got:" >&2; echo "$args" >&2; return 1 ;;
  esac
  printf '%s\n' "$args" | grep -qx -- '--sync-period=1m' || { echo "manager args: $args" >&2; return 1; }
}

check_feature_gate() {
  local expected="$1" mode="$2" group provider legacy kind rendered actual failed=0
  local -a flags
  for entry in 'core cluster-api core CoreProvider' \
    'controlPlane kubeadm kubeadm ControlPlaneProvider' \
    'infrastructure docker docker InfrastructureProvider' \
    'ipam in-cluster in-cluster IPAMProvider'; do
    read -r group provider legacy kind <<< "$entry"
    flags=(--set "cluster-api-operator.$group.$provider.version=v1.13.7")
    case "$mode" in
      global)
        flags+=(--set "cluster-api-operator.manager.featureGates.$legacy.ClusterTopology=true")
        ;;
      provider)
        flags+=(--set "cluster-api-operator.$group.$provider.manager.featureGates.ClusterTopology=true")
        ;;
      false)
        flags+=(--set "cluster-api-operator.manager.featureGates.$legacy.ClusterTopology=true"
          --set "cluster-api-operator.$group.$provider.manager.featureGates.ClusterTopology=false")
        ;;
      empty)
        flags+=(--set "cluster-api-operator.manager.featureGates.$legacy.ClusterTopology=true"
          --set-json "cluster-api-operator.$group.$provider.manager.featureGates={}")
        ;;
    esac
    rendered="$BATS_TEST_TMPDIR/$kind.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api "${flags[@]}" > "$rendered"
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.manager.featureGates.ClusterTopology" "$rendered")"
    [ "$actual" = "$expected" ] || {
      echo "$kind $mode: expected $expected, got $actual" >&2
      failed=1
    }
  done
  [ "$failed" -eq 0 ]
}

@test "legacy global feature gates remain effective for provider kinds" {
  check_feature_gate true global
}

@test "provider feature gates work without a legacy global value" {
  check_feature_gate true provider
}

@test "an explicit false provider gate overrides the global gate" {
  check_feature_gate false false
}

@test "an explicitly empty provider gate map replaces global gates" {
  check_feature_gate null empty
}

check_auto_kubeadm_gate() {
  local expected="$1" rendered actual
  shift
  rendered="$BATS_TEST_TMPDIR/automatic-control-plane.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.infrastructure.docker.version=v1.13.7 \
    --set cluster-api-operator.configSecret.name=provider-config \
    "$@" > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "ControlPlaneProvider") | .spec.manager.featureGates.ClusterTopology' "$rendered")"
  [ "$actual" = "$expected" ] || {
    echo "automatic kubeadm gate: expected $expected, got $actual" >&2
    return 1
  }
}

@test "automatic kubeadm control plane retains the legacy global gate" {
  check_auto_kubeadm_gate true \
    --set cluster-api-operator.manager.featureGates.kubeadm.ClusterTopology=true
}

@test "automatic kubeadm control plane does not manufacture a feature gate" {
  check_auto_kubeadm_gate null
}

@test "explicit kubeadm false gate supersedes automatic global defaults" {
  check_auto_kubeadm_gate false \
    --set cluster-api-operator.manager.featureGates.kubeadm.ClusterTopology=true \
    --set cluster-api-operator.controlPlane.kubeadm.version=v1.13.7 \
    --set cluster-api-operator.controlPlane.kubeadm.manager.featureGates.ClusterTopology=false
}

@test "explicit kubeadm empty gate map supersedes automatic global defaults" {
  check_auto_kubeadm_gate null \
    --set cluster-api-operator.manager.featureGates.kubeadm.ClusterTopology=true \
    --set cluster-api-operator.controlPlane.kubeadm.version=v1.13.7 \
    --set-json 'cluster-api-operator.controlPlane.kubeadm.manager.featureGates={}'
}

@test "automatic core and bootstrap keep their existing gate-free defaults" {
  local rendered actual
  rendered="$BATS_TEST_TMPDIR/automatic-defaults.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.infrastructure.docker.version=v1.13.7 \
    --set cluster-api-operator.configSecret.name=provider-config \
    --set cluster-api-operator.manager.featureGates.core.ClusterTopology=true \
    --set cluster-api-operator.manager.featureGates.kubeadm.ClusterTopology=true > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "CoreProvider") | .spec.manager.featureGates' "$rendered")"
  [ "$actual" = null ]
  actual="$(yq --no-doc 'select(.kind == "BootstrapProvider") | .spec.manager.featureGates' "$rendered")"
  [ "$actual" = null ]
}

@test "explicit bootstrap retains its own manager gate" {
  local rendered actual
  rendered="$BATS_TEST_TMPDIR/bootstrap.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.bootstrap.kubeadm.version=v1.13.7 \
    --set cluster-api-operator.bootstrap.kubeadm.manager.featureGates.ClusterTopology=true > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "BootstrapProvider") | .spec.manager.featureGates.ClusterTopology' "$rendered")"
  [ "$actual" = true ]
}

@test "addon retains its supported manager metrics without a legacy gate" {
  local rendered actual
  rendered="$BATS_TEST_TMPDIR/addon.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.addon.example.version=v1.13.7 \
    --set cluster-api-operator.addon.example.manager.metrics.diagnosticsAddress=:8443 \
    --set cluster-api-operator.manager.featureGates.example.ClusterTopology=true > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "AddonProvider") | .spec.manager.metrics.diagnosticsAddress' "$rendered")"
  [ "$actual" = ':8443' ]
  actual="$(yq --no-doc 'select(.kind == "AddonProvider") | .spec.manager.featureGates' "$rendered")"
  [ "$actual" = null ]
}

check_provider_spec_field() {
  local field="$1" group provider kind entry rendered actual expected path failed=0
  local -a flags
  for entry in 'core cluster-api CoreProvider' 'bootstrap kubeadm BootstrapProvider' \
    'controlPlane kubeadm ControlPlaneProvider' 'infrastructure docker InfrastructureProvider' \
    'ipam in-cluster IPAMProvider' 'addon example AddonProvider'; do
    read -r group provider kind <<< "$entry"
    flags=(--set "cluster-api-operator.$group.$provider.createNamespace=false")
    case "$field" in
      fetchConfig)
        flags+=(--set-string "cluster-api-operator.$group.$provider.fetchConfig.selector.matchLabels.example=manifests")
        path='.spec.fetchConfig.selector.matchLabels.example'
        expected=manifests
        ;;
      manifestPatches)
        flags+=(--set-json "cluster-api-operator.$group.$provider.manifestPatches=[\"kind: Deployment\"]")
        path='.spec.manifestPatches[0]'
        expected='kind: Deployment'
        ;;
      additionalManifests)
        flags+=(--set-json "cluster-api-operator.$group.$provider.additionalManifests={\"name\":\"extra-manifests\",\"manifests\":\"kind: ConfigMap\"}")
        path='.spec.additionalManifests.name'
        expected=extra-manifests
        ;;
      absent)
        path='.spec'
        expected=null
        ;;
    esac
    rendered="$BATS_TEST_TMPDIR/$kind-$field.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api "${flags[@]}" > "$rendered"
    actual="$(yq --no-doc "select(.kind == \"$kind\") | $path" "$rendered")"
    [ "$actual" = "$expected" ] || {
      echo "$kind $field: expected $expected in spec, got $actual" >&2
      failed=1
    }
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .metadata.$field" "$rendered")"
    [ "$actual" = null ] || {
      echo "$kind leaked $field into metadata" >&2
      failed=1
    }
  done
  [ "$failed" -eq 0 ]
}

@test "fetch configuration alone belongs to every provider spec" {
  check_provider_spec_field fetchConfig
}

@test "manifest patches alone belong to every provider spec" {
  check_provider_spec_field manifestPatches
}

@test "additional manifests alone belong to every provider spec" {
  check_provider_spec_field additionalManifests
}

@test "provider namespace options alone do not manufacture spec fields" {
  check_provider_spec_field absent
}

@test "legacy global fetch configuration alone stays inside matching provider specs" {
  local group provider kind entry rendered actual
  for entry in 'infrastructure docker InfrastructureProvider' 'ipam in-cluster IPAMProvider'; do
    read -r group provider kind <<< "$entry"
    rendered="$BATS_TEST_TMPDIR/$kind-global-fetch.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
      --set "cluster-api-operator.$group.$provider.createNamespace=false" \
      --set-string "cluster-api-operator.fetchConfig.$provider.url=https://example.test/components.yaml" > "$rendered"
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.url" "$rendered")"
    [ "$actual" = 'https://example.test/components.yaml' ]
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .metadata.fetchConfig" "$rendered")"
    [ "$actual" = null ]
  done
}

@test "another provider's global fetch configuration does not create a spec" {
  local rendered actual
  rendered="$BATS_TEST_TMPDIR/unmatched-fetch.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.infrastructure.docker.createNamespace=false \
    --set-string cluster-api-operator.fetchConfig.other.url=https://example.test/components.yaml > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "InfrastructureProvider") | .spec' "$rendered")"
  [ "$actual" = null ]
}

@test "infrastructure patches alone belong to the provider spec" {
  local rendered actual
  rendered="$BATS_TEST_TMPDIR/infrastructure-patches.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set-string 'cluster-api-operator.infrastructure.docker.patches[0].patch=kind: Deployment' > "$rendered"
  actual="$(yq --no-doc 'select(.kind == "InfrastructureProvider") | .spec.patches[0].patch' "$rendered")"
  [ "$actual" = 'kind: Deployment' ]
  actual="$(yq --no-doc 'select(.kind == "InfrastructureProvider") | .metadata.patches' "$rendered")"
  [ "$actual" = null ]
}

check_fetch_overlap() {
  local mode="$1" group provider kind template entry rendered actual expected failed=0
  local -a flags
  for entry in 'infrastructure docker InfrastructureProvider infra.yaml' \
    'ipam in-cluster IPAMProvider ipam.yaml'; do
    read -r group provider kind template <<< "$entry"
    flags=(--set "cluster-api-operator.$group.$provider.createNamespace=false"
      --set-string "cluster-api-operator.fetchConfig.$provider.url=https://legacy.example/components.yaml")
    case "$mode" in
      override)
        flags+=(--set-string "cluster-api-operator.$group.$provider.fetchConfig.url=https://provider.example/components.yaml")
        expected=https://provider.example/components.yaml
        ;;
      empty)
        flags+=(--set-json "cluster-api-operator.$group.$provider.fetchConfig={}")
        expected=https://legacy.example/components.yaml
        ;;
    esac
    rendered="$BATS_TEST_TMPDIR/$kind-overlap-$mode.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api "${flags[@]}" \
      --show-only "charts/cluster-api-operator/templates/$template" > "$rendered"
    [ "$(grep -c '^  fetchConfig:' "$rendered")" -eq 1 ] || {
      echo "$kind $mode must emit exactly one fetchConfig key" >&2
      failed=1
    }
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.url" "$rendered")"
    [ "$actual" = "$expected" ] || {
      echo "$kind $mode expected $expected, got $actual" >&2
      failed=1
    }
  done
  [ "$failed" -eq 0 ]
}

@test "nonempty provider fetch configuration overrides global input with one key" {
  check_fetch_overlap override
}

@test "empty provider fetch configuration retains the legacy global fallback" {
  check_fetch_overlap empty
}

check_profiling_flags() {
  local rendered
  rendered="$BATS_TEST_TMPDIR/profiling.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.profilerAddress=:6061 \
    --set cluster-api-operator.contentionProfiling=true "$@" > "$rendered"
  grep -q -- '--profiler-address=localhost:6061' "$rendered"
  grep -q -- '--contention-profiling=true' "$rendered"
  grep -q '^[[:space:]]*- containerPort: 6061$' "$rendered"
}

@test "profiling flags remain effective when leader election is disabled" {
  check_profiling_flags --set cluster-api-operator.leaderElection.enabled=false
}

@test "profiling flags remain effective when the optional leader election block is absent" {
  check_profiling_flags --set-json cluster-api-operator.leaderElection=null
}

check_global_fetch_type() {
  local mode="$1" group provider kind template entry rendered actual failed=0
  local -a flags
  for entry in 'infrastructure docker InfrastructureProvider infra.yaml' \
    'ipam in-cluster IPAMProvider ipam.yaml'; do
    read -r group provider kind template <<< "$entry"
    flags=(--set "cluster-api-operator.$group.$provider.createNamespace=false")
    case "$mode" in
      selector)
        flags+=(--set-json "cluster-api-operator.fetchConfig.$provider.selector={\"matchLabels\":{\"example\":\"manifests\"},\"matchExpressions\":[{\"key\":\"release\",\"operator\":\"In\",\"values\":[\"v1\"]}]}")
        ;;
      empty)
        flags+=(--set-json "cluster-api-operator.fetchConfig.$provider.selector={}")
        ;;
      oci)
        flags+=(--set-string "cluster-api-operator.fetchConfig.$provider.oci=oci://registry.example/provider:v1")
        ;;
    esac
    rendered="$BATS_TEST_TMPDIR/$kind-fetch-type-$mode.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api "${flags[@]}" \
      --show-only "charts/cluster-api-operator/templates/$template" > "$rendered"
    [ "$(grep -c '^  fetchConfig:' "$rendered")" -eq 1 ]
    if [ "$mode" = oci ]; then
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.oci" "$rendered")"
      [ "$actual" = 'oci://registry.example/provider:v1' ]
      continue
    fi
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.selector | tag" "$rendered")"
    if [ "$actual" != '!!map' ]; then
      echo "$kind $mode selector must be an object, got $actual" >&2
      failed=1
      continue
    fi
    if [ "$mode" = selector ]; then
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.selector.matchLabels.example" "$rendered")"
      [ "$actual" = manifests ]
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.selector.matchExpressions[0].key" "$rendered")"
      [ "$actual" = release ]
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.selector.matchExpressions[0].operator" "$rendered")"
      [ "$actual" = In ]
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig.selector.matchExpressions[0].values[0]" "$rendered")"
      [ "$actual" = v1 ]
    fi
  done
  [ "$failed" -eq 0 ]
}

@test "global fetch selectors preserve nested label and expression objects" {
  check_global_fetch_type selector
}

@test "empty global fetch selectors remain objects rather than strings" {
  check_global_fetch_type empty
}

@test "global OCI fetch configuration remains a string source" {
  check_global_fetch_type oci
}

check_global_fetch_entry() {
  local value="$1" expected="$2" group provider kind template entry rendered actual failed=0
  for entry in 'infrastructure docker InfrastructureProvider infra.yaml' \
    'ipam in-cluster IPAMProvider ipam.yaml'; do
    read -r group provider kind template <<< "$entry"
    rendered="$BATS_TEST_TMPDIR/$kind-entry.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
      --set "cluster-api-operator.$group.$provider.version=v1.13.7" \
      --set-json "cluster-api-operator.fetchConfig.$provider=$value" \
      --show-only "charts/cluster-api-operator/templates/$template" > "$rendered"
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.fetchConfig | tag" "$rendered")"
    [ "$actual" = "$expected" ] || {
      echo "$kind global entry $value: expected $expected, got $actual" >&2
      failed=1
    }
  done
  [ "$failed" -eq 0 ]
}

@test "empty global fetch entries preserve default source selection" {
  check_global_fetch_entry '{}' '!!null'
}

@test "null global fetch entries preserve default source selection" {
  check_global_fetch_entry null '!!null'
}

@test "malformed global fetch primitives are not silently replaced by a default source" {
  check_global_fetch_entry false '!!bool'
  check_global_fetch_entry 0 '!!int'
  check_global_fetch_entry '"invalid"' '!!str'
  check_global_fetch_entry '[]' '!!seq'
}

check_provider_scalar_types() {
  local mode="$1" metrics="$2" name="$3" namespace="$4" expected="$5"
  local group provider kind entry rendered path actual failed=0
  local -a flags
  for entry in 'core cluster-api CoreProvider' 'bootstrap kubeadm BootstrapProvider' \
    'controlPlane kubeadm ControlPlaneProvider' 'infrastructure docker InfrastructureProvider' \
    'ipam in-cluster IPAMProvider' 'addon example AddonProvider'; do
    read -r group provider kind <<< "$entry"
    flags=("--set-$mode" "cluster-api-operator.$group.$provider.manager.metrics.diagnosticsAddress=$metrics"
      "--set-$mode" "cluster-api-operator.$group.$provider.configSecret.name=$name"
      "--set-$mode" "cluster-api-operator.$group.$provider.configSecret.namespace=$namespace")
    rendered="$BATS_TEST_TMPDIR/$kind-scalars-$mode.yaml"
    helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api "${flags[@]}" > "$rendered"
    for path in manager.metrics.diagnosticsAddress configSecret.name configSecret.namespace; do
      actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.$path | tag" "$rendered")"
      [ "$actual" = "$expected" ] || {
        echo "$kind $path: expected YAML type $expected, got $actual" >&2
        failed=1
      }
    done
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.manager.metrics.diagnosticsAddress" "$rendered")"
    [ "$actual" = "$metrics" ]
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.configSecret.name" "$rendered")"
    [ "$actual" = "$name" ]
    actual="$(yq --no-doc "select(.kind == \"$kind\") | .spec.configSecret.namespace" "$rendered")"
    [ "$actual" = "$namespace" ]
  done
  [ "$failed" -eq 0 ]
}

@test "provider metric disable values and numeric secret identifiers stay strings" {
  check_provider_scalar_types string 0 123 456 '!!str'
}

@test "provider secret identifiers that resemble YAML keywords stay strings" {
  check_provider_scalar_types string 0 true null '!!str'
}

@test "invalid numeric provider string inputs are not coerced into valid strings" {
  check_provider_scalar_types json 123 123 456 '!!int'
}

@test "invalid boolean provider string inputs are not coerced into valid strings" {
  check_provider_scalar_types json true true true '!!bool'
}

@test "provider config Secrets override global defaults" {
  local group provider kind rendered entry
  for entry in 'core cluster-api CoreProvider' 'bootstrap kubeadm BootstrapProvider' \
    'controlPlane kubeadm ControlPlaneProvider' 'infrastructure docker InfrastructureProvider' \
    'ipam in-cluster IPAMProvider' 'addon example AddonProvider'; do
    read -r group provider kind <<< "$entry"
    rendered="$BATS_TEST_TMPDIR/$kind-secret-precedence.yaml"
    helm template capi-operator "$OPERATOR" \
      --set cluster-api-operator.configSecret.name=global-config \
      --set cluster-api-operator.configSecret.namespace=global-namespace \
      --set "cluster-api-operator.$group.$provider.configSecret.name=provider-config" \
      --set "cluster-api-operator.$group.$provider.configSecret.namespace=provider-namespace" > "$rendered"
    [ "$(yq --no-doc "select(.kind == \"$kind\") | .spec.configSecret.name" "$rendered")" = provider-config ]
    [ "$(yq --no-doc "select(.kind == \"$kind\") | .spec.configSecret.namespace" "$rendered")" = provider-namespace ]
  done
}

@test "operator profiling preserves an explicit host address" {
  local rendered="$BATS_TEST_TMPDIR/profiler-host.yaml"
  helm template capi-operator "$OPERATOR" \
    --set cluster-api-operator.profilerAddress=127.0.0.1:6061 > "$rendered"
  grep --quiet --fixed-strings -- '--profiler-address=127.0.0.1:6061' "$rendered"
}

@test "operator ports retain port-only addresses" {
  local rendered
  rendered="$BATS_TEST_TMPDIR/operator.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.healthAddr=:9440 \
    --set cluster-api-operator.diagnosticsAddress=:8443 > "$rendered"
  [ "$(grep -c '^[[:space:]]*port: 9440$' "$rendered")" -eq 2 ]
  grep -q '^[[:space:]]*- containerPort: 8443$' "$rendered"
}

@test "operator ports preserve explicit IPv4 host addresses" {
  local rendered
  rendered="$BATS_TEST_TMPDIR/operator.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api \
    --set cluster-api-operator.healthAddr=0.0.0.0:9440 \
    --set cluster-api-operator.diagnosticsAddress=0.0.0.0:8443 > "$rendered"
  [ "$(grep -c '^[[:space:]]*port: 9440$' "$rendered")" -eq 2 ]
  grep -q '^[[:space:]]*- containerPort: 8443$' "$rendered"
  grep -q -- '--health-addr=0.0.0.0:9440' "$rendered"
  grep -q -- '--diagnostics-address=0.0.0.0:8443' "$rendered"
}
