#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The TalosConfigTemplate is named after a content hash of the worker machine
# config, and three places in kubernetes-nodes have to arrive at that name:
#
#   * talos-reconcile-job.yaml creates the object, passing the name to the Job
#     as TCT_NAME;
#   * the same file's Role pins the Job's get/patch/update and delete rights on
#     TalosConfigTemplates to it by resourceNames;
#   * nodegroup.yaml points MachineDeployment.spec.template.spec.bootstrap
#     .configRef.name at it.
#
# If the Job and the MachineDeployment disagree, the MachineDeployment
# references a TalosConfigTemplate that is never created. If the Role disagrees,
# the Job's server-side apply -- a PATCH on the name, even for a create -- is
# Forbidden and the template is never created either. CAPI does not fail in
# either case: it blocks on "templates do not exist" and no worker is ever
# built, so the divergence surfaces as a cluster that quietly never scales
# rather than as a render error.
#
# The chart's helm-unittest suites pin both names, but each pins its own
# literal in its own file. That catches a divergence, yet the output only says
# one literal moved -- which reads as a stale fixture, and re-pasting the new
# value is then the obvious and wrong fix. This guard renders the chart ONCE and
# compares the two values against each other, so the failure states the
# invariant instead of a number.
#
# Rendered with explicit resources rather than an instanceType: an
# instanceType-sized pool resolves its kubelet reservations through a live
# `lookup` of the VirtualMachineClusterInstancetype, which is empty outside a
# cluster. The invariant under test does not depend on which sizing source is
# used -- both consumers read the same assembled group either way.
#
# Compatible with both `bats` directly and the in-repo cozytest.sh runner, which
# runs each @test in a fresh subshell under `set -u` and honors no setup() or
# teardown(), so TMP is provisioned inline per test and cleaned without an EXIT
# trap (docs/agents/e2e-testing.md bans those here).

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/apps/kubernetes-nodes"

render_pool() {
  # $1: output file, remaining args: extra --set flags for the pool shape
  out="$1"; shift
  helm template kubernetes-nodes-myk8s-md0 "$CHART" \
    --namespace tenant-test \
    --set cluster=myk8s \
    --set _cluster.cluster-domain=cozy.local \
    --set version=v1.35 \
    --set minReplicas=0 --set maxReplicas=3 \
    --set instanceType="" \
    --set diskSize=20Gi --set storageClass=replicated \
    --set 'roles[0]=ingress-nginx' \
    --set resources.cpu=2 --set resources.memory=4Gi \
    "$@" > "$out" 2>"$out.err"
  [ -s "$out" ] || { cat "$out.err" >&2; return 1; }
}

job_tct_name() {
  awk '/name: TCT_NAME/ { getline; sub(/^[[:space:]]*value:[[:space:]]*/, ""); gsub(/"/, ""); print; exit }' "$1"
}

role_tct_names() {
  # One line per Role rule on talosconfigtemplates that carries resourceNames.
  awk '/resources: \["talosconfigtemplates"\]/ { getline; if ($0 ~ /resourceNames:/) { sub(/.*resourceNames:[[:space:]]*\[/, ""); sub(/\].*/, ""); gsub(/"/, ""); print } }' "$1"
}

md_config_ref_name() {
  awk '/configRef:/ { f=1; next } f && /name:/ { sub(/^[[:space:]]*name:[[:space:]]*/, ""); print; exit }' "$1"
}

assert_agreement() {
  tmp="$1"; shape="$2"
  job=$(job_tct_name "$tmp/r.yaml")
  md=$(md_config_ref_name "$tmp/r.yaml")

  # An empty capture means the render stopped emitting one of the two fields,
  # which would make a naive string comparison pass on "" == "".
  if [ -z "$job" ] || [ -z "$md" ]; then
    echo "FAIL: could not read both names out of the $shape render."
    echo "  Job TCT_NAME:            '${job:-<missing>}'"
    echo "  MachineDeployment ref:   '${md:-<missing>}'"
    echo "Either the field moved or the render is incomplete; this guard cannot"
    echo "compare what it cannot find, so it fails rather than pass vacuously."
    return 1
  fi

  if [ "$job" != "$md" ]; then
    echo "FAIL: the two TalosConfigTemplate names must be equal, and are not ($shape pool)."
    echo "  talos-reconcile-job.yaml creates: $job"
    echo "  nodegroup.yaml references:        $md"
    echo "The MachineDeployment now points at a TalosConfigTemplate nothing creates."
    echo "CAPI blocks on \"templates do not exist\" and builds no worker, without"
    echo "failing the release. Both names come from a hash of the group assembled"
    echo "by kubernetes-nodes.group in templates/_helpers.tpl: they diverge when a"
    echo "call site stops feeding that helper, or when a key it carries is dropped."
    echo "Fix the input they share; do not re-pin either literal."
    return 1
  fi

  # Two named rules: get/patch/update, and delete. Fewer means one went unnamed
  # or disappeared, and the per-rule comparison below would pass on nothing.
  roles=$(role_tct_names "$tmp/r.yaml")
  if [ "$(printf '%s\n' "$roles" | grep -c .)" -ne 2 ]; then
    echo "FAIL: expected two named TalosConfigTemplate rules in the talos-reconcile Role ($shape pool), found:"
    printf '  %s\n' "${roles:-<none>}"
    return 1
  fi
  for r in $roles; do
    if [ "$r" != "$md" ]; then
      echo "FAIL: the talos-reconcile Role names a TalosConfigTemplate the Job does not apply ($shape pool)."
      echo "  Role resourceNames:        $r"
      echo "  nodegroup.yaml references: $md"
      echo "The Job's server-side apply is a PATCH on the template's name even when"
      echo "it creates it, so it is Forbidden, and the pool never gets a template"
      echo "to scale onto. Name the rule after the same content-hash name."
      return 1
    fi
  done
}

@test "Job and MachineDeployment agree on the TalosConfigTemplate name" {
  TMP=$(mktemp -d)
  render_pool "$TMP/r.yaml"
  assert_agreement "$TMP" "default"
  rm -rf "$TMP"
}

@test "they still agree once the hashed spec changes (GPU pool)" {
  # A GPU pool renders extra nodeLabels into the machine config, so the hash --
  # and therefore both names -- must move together. Pinning the agreement on a
  # second shape is what catches a call site that reads a subset of the group:
  # the default shape renders gpus [], kubelet {} and instanceType "" as falsy,
  # so a consumer missing those keys is indistinguishable from one carrying them.
  TMP=$(mktemp -d)
  render_pool "$TMP/r.yaml" --set 'gpus[0].name=nvidia.com/GH200'
  assert_agreement "$TMP" "GPU"
  rm -rf "$TMP"
}

@test "they still agree with an explicit kernelModules list" {
  # kernelModules has no values.yaml default, so the default and GPU shapes both
  # leave it unset and cannot tell a call site that carries it from one that
  # does not. Only a set value moves the hash on one side alone.
  TMP=$(mktemp -d)
  render_pool "$TMP/r.yaml" --set 'kernelModules[0].name=br_netfilter'
  assert_agreement "$TMP" "kernelModules"
  rm -rf "$TMP"
}

@test "the two shapes hash differently" {
  # Guards the guard: if both renders collapsed to the same name, the agreement
  # assertions above would hold trivially and stop proving anything.
  TMP=$(mktemp -d)
  render_pool "$TMP/plain.yaml"
  render_pool "$TMP/gpu.yaml" --set 'gpus[0].name=nvidia.com/GH200'
  plain=$(job_tct_name "$TMP/plain.yaml")
  gpu=$(job_tct_name "$TMP/gpu.yaml")
  if [ "$plain" = "$gpu" ]; then
    echo "FAIL: the plain and GPU pools hashed to the same name ($plain)."
    echo "The content hash is supposed to track the rendered spec, and a GPU pool"
    echo "renders different nodeLabels. Equal names mean the hash input no longer"
    echo "covers the field that changed."
    false
  fi
  rm -rf "$TMP"
}
