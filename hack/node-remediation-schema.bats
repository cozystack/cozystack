#!/usr/bin/env bats
# The platform forwards one nodeRemediation block to two charts,
# node-remediation (the operators) and node-remediation-config, as the values
# of two HelmReleases. Each chart carries a values.schema.json, and the part
# that describes the block has to be the same in both: a key typed in one
# chart and left free in the other would let a wrong kind install one release
# and fail the other. The platform's own render does not run those schemas
# (it emits a Package, not the charts), and the platform chart does not render
# offline as a whole (templates/repository.yaml looks the source repository up
# and fails without it), so the path a hoster's value takes is pinned in two
# halves: packages/core/platform/tests/bundles_node_remediation_test.yaml pins
# that the block reaches both components as written, a quoted "false" still a
# string, and tests/values_schema_test.yaml of each chart pins that the chart
# refuses that string under its key.
#
# Harness compatibility note: CI runs this file under Bats through
# `make bats-unit-tests`. It avoids `run`, `$status`, `$output`, `skip` and
# setup()/teardown() so the narrower legacy `hack/cozytest.sh` translator can
# run it too. Paths stay repo-root-relative for both runners.
#
# Run with: bats hack/node-remediation-schema.bats

load test_helper

CONFIG=packages/system/node-remediation-config
OPERATORS=packages/system/node-remediation

write_block() {
  cat > "$1" <<'VALUES'
agent: fence_redfish
unhealthyAfter: 300s
remediateNotReady: false
workerMinHealthy: 51%
stormCooldown: 5m
paused: false
bmc:
  caBundle: |
    -----BEGIN CERTIFICATE-----
    MIIB
    -----END CERTIFICATE-----
  credentialsSecretName: bmc-credentials
  systemsUri: /redfish/v1/Systems/1
  port: 8443
  nodes:
    node1:
      address: 192.0.2.11
      port: 443
      systemsUri: /redfish/v1/Systems/2
VALUES
}

@test "the two charts type the shared nodeRemediation block identically" {
  tmp=$(mktemp -d)
  jq -S '.properties | del(.fenceAgentsRemediation, .nodeHealthcheck)' "$OPERATORS/values.schema.json" > "$tmp/operators"
  jq -S '.properties' "$CONFIG/values.schema.json" > "$tmp/config"
  if ! diff -u "$tmp/operators" "$tmp/config" > "$tmp/diff"; then
    echo "the shared part of $OPERATORS/values.schema.json and $CONFIG/values.schema.json differs; a key typed in one chart and not the other lets a wrong kind install one release and fail the other:" >&2
    cat "$tmp/diff" >&2
    rm -rf "$tmp"
    return 1
  fi
  if [ "$(jq 'length' "$tmp/config")" -lt 7 ]; then
    echo "the configuration chart's schema types fewer than the seven keys of the block" >&2
    rm -rf "$tmp"
    return 1
  fi
  # required differs by design: a chart may require only the keys it reads and
  # has defaults for, so that a null drops to "missing" instead of rendering
  # "<nil>". The operators chart has no defaults for the scalars of the block;
  # requiring them there would refuse a block that relies on the configuration
  # chart's defaults, the common case.
  config_required=$(jq -c '.required | sort' "$CONFIG/values.schema.json")
  operators_required=$(jq -c '.required | sort' "$OPERATORS/values.schema.json")
  if [ "$config_required" != '["stormCooldown","unhealthyAfter","workerMinHealthy"]' ]; then
    echo "the configuration chart's schema must require exactly the three scalars it reads and defaults; got $config_required" >&2
    rm -rf "$tmp"
    return 1
  fi
  if [ "$operators_required" != '["fenceAgentsRemediation","nodeHealthcheck"]' ]; then
    echo "the operators chart's schema must require exactly its two image blocks and no key of the shared block; got $operators_required" >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "both charts render the full block beside the keys the platform values Secret adds" {
  tmp=$(mktemp -d)
  write_block "$tmp/values.yaml"
  # The platform's cozystack-values Secret reaches every release through
  # valuesFrom with top-level keys of its own; a top-level additionalProperties
  # would refuse them in the cluster while every chart test stayed green.
  printf '_cluster:\n  name: example\nkeda:\n  clusterDomain: cozy.local\n' >> "$tmp/values.yaml"
  for c in "$CONFIG" "$OPERATORS"; do
    if ! helm template x "$c" --values "$tmp/values.yaml" > /dev/null 2> "$tmp/err"; then
      echo "$c refuses the full block with the platform's top-level keys beside it:" >&2
      cat "$tmp/err" >&2
      rm -rf "$tmp"
      return 1
    fi
  done
  rm -rf "$tmp"
}

@test "a nodeRemediation block that is not a map is refused by the platform render under its key" {
  tmp=$(mktemp -d)
  printf 'bundles:\n  system:\n    enabled: true\n    variant: isp-full\n  enabledPackages:\n    - cozystack.node-remediation\nnodeRemediation: 300s\n' > "$tmp/platform.yaml"
  if helm template platform packages/core/platform --values "$tmp/platform.yaml" > /dev/null 2> "$tmp/err"; then
    echo "the platform rendered nodeRemediation: 300s; two HelmReleases would carry a string where an object is required" >&2
    rm -rf "$tmp"
    return 1
  fi
  if ! grep -q "nodeRemediation must be a map" "$tmp/err"; then
    echo "the platform refused the string, but not under the key:" >&2
    cat "$tmp/err" >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}
