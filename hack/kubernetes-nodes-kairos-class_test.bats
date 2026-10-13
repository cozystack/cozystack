#!/usr/bin/env bats
# A pool on an operator Kairos class takes its Kubernetes release and image
# from the class, so a platform upgrade that moves the patch files/versions.yaml
# pins must leave every object of that pool unchanged, the name-hashed
# KairosConfigTemplate included; anything else rolls the pool. The class is
# read with lookup, which only helm-unittest can fake, so this renders the same
# suite against two copies of the chart that differ only in that patch and
# compares the snapshots. A pool on the built-in kairos class follows the
# platform and must change: that is the control showing the bump reaches the
# render at all.

load test_helper

CHART="$(pwd)/packages/apps/kubernetes-nodes"

suite() {
  local class="$1" objects="$2"
  cat <<SUITE
suite: patch pin
templates:
  - templates/nodegroup.yaml
  - templates/kairos-reconcile-job.yaml
release:
  name: kubernetes-nodes-myk8s-md0
  namespace: tenant-test
set:
  cluster: myk8s
  _cluster: {cluster-domain: cozy.local}
  version: "v1.35"
  instanceType: ""
  resources: {cpu: "2", memory: 4Gi}
  nodeGroupClassName: $class
tests:
  - it: renders
    kubernetesProvider:
      scheme:
        "cozystack.io/v1alpha1/NodeGroupClass":
          gvr: {group: "cozystack.io", version: "v1alpha1", resource: "nodegroupclasses"}
          namespaced: false
        "cluster.x-k8s.io/v1beta1/MachineSet":
          gvr: {group: "cluster.x-k8s.io", version: "v1beta1", resource: "machinesets"}
          namespaced: true
        "infrastructure.cluster.x-k8s.io/v1alpha1/KubevirtMachineTemplate":
          gvr: {group: "infrastructure.cluster.x-k8s.io", version: "v1alpha1", resource: "kubevirtmachinetemplates"}
          namespaced: true
      objects:
        - {kind: MachineSet, apiVersion: cluster.x-k8s.io/v1beta1, metadata: {name: s, namespace: s}, spec: {}}
        - {kind: KubevirtMachineTemplate, apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1, metadata: {name: s, namespace: s}, spec: {}}
$objects
    asserts:
      - matchSnapshot: {}
SUITE
}

# Renders the suite for <class> against a copy of the chart whose v1.35 patch
# is <patch>, and prints the snapshot file.
render() {
  local patch="$1" class="$2" objects="$3" dir
  dir="$(mktemp -d)"
  cp -RL "$CHART" "$dir/chart"
  rm -rf "$dir/chart/tests"
  mkdir -p "$dir/chart/tests"
  sed -i.bak "s/^\"v1.35\": .*/\"v1.35\": \"$patch\"/" "$dir/chart/files/versions.yaml"
  grep -qxF "\"v1.35\": \"$patch\"" "$dir/chart/files/versions.yaml"
  suite "$class" "$objects" > "$dir/chart/tests/pin_test.yaml"
  helm unittest "$dir/chart" >&2
  cat "$dir/chart/tests/__snapshot__/pin_test.yaml.snap"
  rm -rf "$dir"
}

OPERATOR_CLASS='        - kind: NodeGroupClass
          apiVersion: cozystack.io/v1alpha1
          metadata: {name: acme-kairos}
          spec:
            os: kairos
            image:
              kubernetesVersion: v1.35.2
              reference: registry.example.com/kairos/worker@sha256:1111111111111111111111111111111111111111111111111111111111111111'

@test "a platform patch bump leaves a pool on an operator Kairos class unchanged" {
  before="$(render v1.35.6 acme-kairos "$OPERATOR_CLASS")"
  after="$(render v1.35.99 acme-kairos "$OPERATOR_CLASS")"
  printf '%s\n' "$before" | grep -q 'kind: KairosConfigTemplate'
  if [ "$before" != "$after" ]; then
    cmp_dir="$(mktemp -d)"
    printf '%s\n' "$before" > "$cmp_dir/before"
    printf '%s\n' "$after" > "$cmp_dir/after"
    diff "$cmp_dir/before" "$cmp_dir/after" >&2
    exit 1
  fi
}

@test "a platform patch bump rolls a pool on the built-in kairos class" {
  before="$(render v1.35.6 kairos '')"
  after="$(render v1.35.99 kairos '')"
  printf '%s\n' "$before" | grep -q 'v1.35.6'
  printf '%s\n' "$after" | grep -q 'v1.35.99'
  [ "$before" != "$after" ]
}

# A Kairos class without its own image boots the platform disk of the pool's
# minor. A minor the platform has no disk for must fail the render by name
# rather than render a worker with no system disk.
@test "a pool on the kairos class fails naming the minor the platform ships no disk for" {
  dir="$(mktemp -d)"
  cp -RL "$CHART" "$dir/chart"
  rm "$dir/chart/images/kairos-worker-v1.35.tag"
  if out="$(helm template kubernetes-nodes-myk8s-md0 "$dir/chart" --namespace tenant-test \
      --set cluster=myk8s --set nodeGroupClassName=kairos --set version=v1.35 2>&1)"; then
    echo "rendered without a platform disk" >&2
    exit 1
  fi
  rm -rf "$dir"
  printf '%s\n' "$out" | grep -qF 'NodeGroupClass "kairos": this platform ships no Kairos worker disk for Kubernetes v1.35'
}
