#!/usr/bin/env bash
set -euo pipefail

: "${KUBE_CONTEXT:?}" "${NAMESPACE:?}" "${BRIDGE_VERSION:?}" "${TARGET_VERSION:?}"

fail() {
  printf 'CAPI upgrade: %s\n' "$*" >&2
  exit 1
}

kube() {
  local remaining=$((deadline - SECONDS)) status=0
  ((remaining > 0)) || fail "timed out before an API request"
  timeout "$remaining" kubectl --context "$KUBE_CONTEXT" --namespace "$NAMESPACE" --request-timeout=10s "$@" || status=$?
  ((SECONDS < deadline)) || fail "timed out during an API request"
  return "$status"
}

minor_version() {
  local version="$1"
  if [[ ! "$version" =~ ^v?1\.([0-9]+)\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
    fail "invalid installed version: $version"
  fi
  local minor="${BASH_REMATCH[1]}"
  [[ "$minor" =~ ^(0|[1-9][0-9]?)$ ]] || fail "invalid installed version: $version"
  printf '%s\n' "$minor"
}

command -v timeout >/dev/null || fail "timeout command is required"

timeout_seconds="${CAPI_UPGRADE_TIMEOUT_SECONDS:-480}"
[[ "$timeout_seconds" =~ ^[1-9][0-9]{0,3}$ ]] || fail "invalid upgrade timeout"
deadline=$((SECONDS + timeout_seconds))
poll_seconds="${CAPI_UPGRADE_POLL_SECONDS:-2}"
[[ "$poll_seconds" =~ ^[0-9]+$ ]] || fail "invalid poll interval"
bridge_minor="$(minor_version "$BRIDGE_VERSION")"
target_minor="$(minor_version "$TARGET_VERSION")"
((target_minor >= bridge_minor && target_minor - bridge_minor <= 3)) || fail "invalid intermediate release"

core="$(kube get coreproviders.operator.cluster.x-k8s.io cluster-api --ignore-not-found --output json)"
bootstrap="$(kube get bootstrapproviders.operator.cluster.x-k8s.io kubeadm --ignore-not-found --output json)"
if [[ -z "$core" ]]; then
  [[ -z "$bootstrap" ]] || fail "kubeadm exists without a core provider"
  printf 'No installed providers need an intermediate upgrade.\n'
  exit 0
fi

validate_provider() {
  local object="$1" suffix="$2" installed requested uid minor requested_minor newer
  installed="$(jq --raw-output '.status.installedVersion // empty' <<< "$object")"
  [[ -n "$installed" ]] || fail "$suffix installed version is not recorded"
  minor="$(minor_version "$installed")"
  newer="$(jq --null-input --arg installed "$installed" --arg target "$TARGET_VERSION" '
    def base: ltrimstr("v") | split("-")[0] | split("+")[0] | split(".") | map(tonumber);
    ($installed | base) > ($target | base)')"
  [[ "$newer" == false ]] || fail "$suffix provider downgrades are not supported"
  ((bridge_minor - minor <= 3)) || fail "$suffix is outside the intermediate upgrade window"
  requested="$(jq --raw-output '.spec.version // empty' <<< "$object")"
  requested_minor="$(minor_version "$requested")"
  uid="$(jq --raw-output '.metadata.uid // empty' <<< "$object")"
  [[ -n "$uid" ]] || fail "$suffix provider UID is missing"
  if [[ "$requested" != "$installed" && "$requested" != "$BRIDGE_VERSION-$suffix" && "$requested" != "$TARGET_VERSION-$suffix" ]]; then
    # A failed Helm action can restore the old spec after the bridge installed.
    [[ "$installed" == "$BRIDGE_VERSION-$suffix" && "$requested_minor" -lt "$bridge_minor" ]] \
      || fail "$suffix: another upgrade is in progress"
  fi
}

validate_provider "$core" core
core_minor="$(minor_version "$(jq --raw-output '.status.installedVersion' <<< "$core")")"
bootstrap_minor="$target_minor"
if [[ -n "$bootstrap" ]]; then
  validate_provider "$bootstrap" bootstrap
  bootstrap_minor="$(minor_version "$(jq --raw-output '.status.installedVersion' <<< "$bootstrap")")"
fi
bridge_selector_needs_repair() {
  jq --exit-status --arg version "$BRIDGE_VERSION-$2" '
    .spec.version == $version and
    .spec.fetchConfig.selector.matchLabels["provider.cluster.x-k8s.io/version"] != $version' <<< "$1" >/dev/null
}

if ((target_minor - core_minor <= 3 && target_minor - bootstrap_minor <= 3)) &&
  ! bridge_selector_needs_repair "$core" core &&
  { [[ -z "$bootstrap" ]] || ! bridge_selector_needs_repair "$bootstrap" bootstrap; }; then
  printf 'Installed providers are within the supported upgrade window.\n'
  exit 0
fi

wait_provider() {
  local resource="$1" name="$2" deployment="$3" expected="$4" uid="$5"
  local object ready rollout
  while ((SECONDS < deadline)); do
    object="$(kube get "$resource" "$name" --output json)"
    [[ "$(jq --raw-output '.metadata.uid' <<< "$object")" == "$uid" ]] || fail "$name was replaced during the upgrade"
    [[ "$(jq --raw-output '.spec.version' <<< "$object")" == "$expected" ]] || fail "$name requested version changed during the upgrade"
    ready="$(jq --raw-output --arg expected "$expected" '
      .status.installedVersion == $expected and
      .status.observedGeneration == .metadata.generation and
      any(.status.conditions[]?; .type == "Ready" and .status == "True")' <<< "$object")"
    if [[ "$ready" == true ]]; then
      rollout="$(kube get deployments.apps "$deployment" --ignore-not-found --output json)"
      if [[ -n "$rollout" ]]; then
        ready="$(jq --raw-output '
          (.spec.replicas // 1) as $replicas |
          $replicas > 0 and
          .status.observedGeneration == .metadata.generation and
          .status.replicas == $replicas and
          .status.updatedReplicas == $replicas and
          .status.readyReplicas == $replicas and
          .status.availableReplicas == $replicas' <<< "$rollout")"
        if [[ "$ready" == true ]]; then
          printf '%s is ready at %s.\n' "$name" "$expected"
          return
        fi
      fi
    fi
    sleep "$poll_seconds"
  done
  fail "$name timed out waiting for $expected"
}

stage_provider() {
  local resource="$1" name="$2" deployment="$3" suffix="$4" object="$5"
  local installed requested uid minor requested_minor expected patch
  installed="$(jq --raw-output '.status.installedVersion' <<< "$object")"
  requested="$(jq --raw-output '.spec.version' <<< "$object")"
  uid="$(jq --raw-output '.metadata.uid' <<< "$object")"
  minor="$(minor_version "$installed")"
  requested_minor="$(minor_version "$requested")"
  expected="$requested"
  if ((minor < bridge_minor)); then
    if [[ "$requested" == "$installed" || "$requested" == "$BRIDGE_VERSION-$suffix" ]] || ((target_minor - minor > 3)); then
      expected="$BRIDGE_VERSION-$suffix"
    fi
  elif [[ "$installed" == "$BRIDGE_VERSION-$suffix" ]] && ((requested_minor < bridge_minor)); then
    expected="$BRIDGE_VERSION-$suffix"
  fi
  if [[ "$requested" != "$expected" ]] || bridge_selector_needs_repair "$object" "$suffix"; then
    patch="$(jq --compact-output --arg uid "$uid" --arg old "$requested" --arg next "$expected" --arg suffix "$suffix" '
      .spec as $spec |
      (if $spec.fetchConfig == null then
        {selector: {matchLabels: {($suffix + "-components"): "cozy"}}} |
        if $suffix == "bootstrap" then .selector.matchLabels["bootstrap-provider"] = "kubeadm" else . end
      else $spec.fetchConfig end |
        .selector.matchLabels["provider.cluster.x-k8s.io/version"] = $next) as $fetch |
      [
        {op: "test", path: "/metadata/uid", value: $uid},
        {op: "test", path: "/spec/version", value: $old},
        (if $spec | has("fetchConfig") then
          {op: "test", path: "/spec/fetchConfig", value: $spec.fetchConfig}
        else
          {op: "test", path: "/spec", value: $spec}
        end),
        {op: "replace", path: "/spec/version", value: $next},
        {op: (if $spec | has("fetchConfig") then "replace" else "add" end), path: "/spec/fetchConfig", value: $fetch}
      ]' <<< "$object")"
    kube patch "$resource" "$name" --type=json --patch "$patch" >/dev/null
  fi
  wait_provider "$resource" "$name" "$deployment" "$expected" "$uid"
}

stage_provider coreproviders.operator.cluster.x-k8s.io cluster-api capi-controller-manager core "$core"
if [[ -n "$bootstrap" ]]; then
  stage_provider bootstrapproviders.operator.cluster.x-k8s.io kubeadm capi-kubeadm-bootstrap-controller-manager bootstrap "$bootstrap"
fi
printf 'Intermediate provider upgrade completed.\n'
