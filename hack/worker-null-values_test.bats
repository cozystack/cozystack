#!/usr/bin/env bats
# Every path the typed Go client for KubernetesNodes writes as null (listed in
# api/apps/v1alpha1/kubernetesnodes/testdata/null-paths.json, pinned by that
# module's test), a talos.imageFactoryCA: null a pool stored before WorkerClass
# may carry, talos: null, and an osImage: null or proxmox.templateTags: null
# such a pool may carry mean "not set": Helm drops them against the chart
# defaults, so the render must equal the one without them.

load test_helper

render() {
  helm template kubernetes-nodes-k-md0 "$1/chart" --namespace tenant-test \
    --values "$1/base.yaml" ${2:+--values "$2"}
}

null_setup() {
  dir="$(mktemp -d)"
  cp -RL packages/apps/kubernetes-nodes "$dir/chart"
  printf '%s\n' 'cluster: k' 'version: v1.35' 'instanceType: ""' \
    'resources: {cpu: "2", memory: 4Gi}' '_cluster: {cluster-domain: cozy.local}' > "$dir/base.yaml"
}

@test "a null the chart defaults absorb renders as if it were not set, on every worker path" {
  null_setup
  paths="$(jq --raw-output '.[]' api/apps/v1alpha1/kubernetesnodes/testdata/null-paths.json)"
  [ -n "$paths" ]
  for class in '' talos kairos; do
    : > "$dir/class.yaml"
    [ -z "$class" ] || printf 'workerClassName: %s\n' "$class" > "$dir/class.yaml"
    cat "$dir/class.yaml" > "$dir/none.yaml"
    { cat "$dir/class.yaml"; printf '%s\n' 'osImage: null'; } > "$dir/image.yaml"
    { cat "$dir/class.yaml"; printf '%s\n' 'proxmox: {templateTags: null}'; } > "$dir/tags.yaml"
    variants="ca talos image tags"
    i=0
    for path in $paths; do
      i=$((i + 1))
      { cat "$dir/class.yaml"; yq --null-input ".${path} = null"; } > "$dir/typed$i.yaml"
      variants="$variants typed$i"
    done
    { cat "$dir/class.yaml"; printf '%s\n' 'talos: {imageFactoryCA: null}'; } > "$dir/ca.yaml"
    { cat "$dir/class.yaml"; printf '%s\n' 'talos: null'; } > "$dir/talos.yaml"
    want="$(render "$dir" "$dir/none.yaml")"
    [ -n "$want" ]
    for variant in $variants; do
      got="$(render "$dir" "$dir/$variant.yaml")" || { echo "class '${class}': $variant does not render" >&2; false; }
      [ "$got" = "$want" ] || { echo "class '${class}': $variant renders differently" >&2; false; }
    done
  done
  rm -rf "$dir"
}
