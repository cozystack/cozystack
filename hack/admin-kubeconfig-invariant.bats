#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Chart-wide invariant for packages/apps/kubernetes:
#
# Every Deployment in this chart that mounts <release>-admin-kubeconfig as a
# Secret volume MUST:
#   - declare that volume optional: true (so kubelet does not FailedMount
#     while Kamaji is still provisioning the Secret), AND
#   - include the wait-for-kubeconfig init container (so the pod becomes
#     Ready only after Kamaji publishes the Secret).
#
# The per-template unittests in packages/apps/kubernetes/tests/ lock in
# today's three Deployments (cluster-autoscaler, kccm, csi controller) by
# name. This invariant is stricter: any future Deployment added to this
# chart that mounts the same Secret but forgets the guard will fail here.
#
# Requires: helm, yq (mikefarah v4+), jq. All three are available on the
# project's CI runners and on the maintainer workstation.
# -----------------------------------------------------------------------------

@test "every Deployment mounting admin-kubeconfig has optional:true and wait-for-kubeconfig init" {
  values_file="packages/apps/kubernetes/tests/values-ci.yaml"
  [ -f "$values_file" ]

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  helm template invariant packages/apps/kubernetes \
    --namespace tenant-root \
    --values "$values_file" \
    2>/dev/null > "$tmp/rendered.yaml"

  # yq streams one JSON object per input document. jq -s slurps the stream
  # into an array so we can treat all Deployments as a single collection.
  yq --output-format=json eval-all '.' "$tmp/rendered.yaml" \
    | jq -s --raw-output '
        map(select(.kind == "Deployment")) |
        map({
          name: .metadata.name,
          volumes: (.spec.template.spec.volumes // []),
          initNames: ((.spec.template.spec.initContainers // []) | map(.name)),
        }) |
        map(
          .name as $n |
          .initNames as $ins |
          (.volumes[] | select(.secret.secretName | test("-admin-kubeconfig$")?))
          | {
              name: $n,
              optional: (.secret.optional == true),
              hasInit: ($ins | index("wait-for-kubeconfig") != null),
            }
        )
      ' > "$tmp/summary.json"

  # At least one Deployment must match; if a refactor removes every
  # admin-kubeconfig volume from this chart, the test must be updated
  # deliberately rather than silently passing.
  matched=$(jq 'length' "$tmp/summary.json")
  [ "$matched" -ge 1 ]

  offenders=$(jq --raw-output '.[] | select(.optional != true or .hasInit != true) | .name' "$tmp/summary.json")

  if [ -n "$offenders" ]; then
    echo "Deployments mounting *-admin-kubeconfig without optional:true + wait-for-kubeconfig init:" >&2
    echo "$offenders" >&2
    echo "Full summary:" >&2
    cat "$tmp/summary.json" >&2
    exit 1
  fi

  echo "Invariant holds for $matched Deployment(s)"
}

@test "chart emits zero admin-kubeconfig Deployments when tenant has no etcd DataStore" {
  # Without a DataStore (parent Tenant has not populated _namespace.etcd yet)
  # the control-plane-side Deployments must NOT render at all. If they did,
  # the wait-for-kubeconfig init would CrashLoopBackOff indefinitely - there
  # would be no KamajiControlPlane to provision the Secret - consuming the
  # HelmRelease wait budget and triggering exactly the remediation cycle the
  # rest of this chart tries to avoid. This test renders the whole chart
  # with etcd empty and asserts no Deployment references the admin-kubeconfig
  # Secret.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  helm template invariant packages/apps/kubernetes \
    --namespace tenant-root \
    --set _namespace.etcd="" \
    --set _namespace.monitoring="" \
    --set _namespace.ingress="" \
    --set _namespace.seaweedfs="" \
    --set _namespace.host="" \
    --set _cluster.cluster-domain=cozy.local \
    --set 'nodeGroups=null' \
    2>/dev/null > "$tmp/rendered.yaml"

  matched=$(
    yq --output-format=json eval-all '.' "$tmp/rendered.yaml" \
      | jq -s '
          map(select(.kind == "Deployment")) |
          map(select(
            (.spec.template.spec.volumes // [])
              | any(.secret.secretName | test("-admin-kubeconfig$")?)
          )) |
          length
        '
  )

  if [ "$matched" -ne 0 ]; then
    echo "Expected zero Deployments mounting *-admin-kubeconfig when etcd is empty, got $matched:" >&2
    yq --output-format=json eval-all '.' "$tmp/rendered.yaml" \
      | jq -s 'map(select(.kind == "Deployment") | .metadata.name)' >&2
    exit 1
  fi

  echo "No admin-kubeconfig Deployments rendered for empty etcd (as expected)"
}
