#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The tenant cluster's pod and service CIDRs are written by two charts:
#
#   * packages/apps/kubernetes renders them into the CAPI Cluster's
#     spec.clusterNetwork, which the control plane is built from;
#   * packages/apps/kubernetes-nodes renders them into every worker's Talos
#     machine config (cluster.network.podSubnets / serviceSubnets), inside the
#     TalosConfigTemplate the talos-reconcile Job applies.
#
# Each chart carries its own literal, and neither render fails when the two
# disagree: the workers boot, join, and route pod and Service traffic over
# ranges the control plane does not use. This guard renders both charts and
# compares the lists, so a range moved on one side only fails here.
#
# The worker side is read the way the Job produces it: the heredoc is cut out
# of the Job's command, run through sh, and the TalosConfigTemplate it prints is
# parsed, then the machine config inside it. A text grep of the Job would also
# match a comment.
#
# Requires: helm, yq (mikefarah v4+). Runs under both `bats` and the in-repo
# cozytest.sh runner, so TMP is provisioned inline per test and cleaned without
# an EXIT trap.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# $1: output directory. Writes pods and services, one CIDR per line.
cluster_cidrs() {
  helm template kubernetes-myk8s "$REPO_ROOT/packages/apps/kubernetes" \
    --namespace tenant-test \
    -f "$REPO_ROOT/packages/apps/kubernetes/tests/values-ci.yaml" \
    > "$1/cluster.yaml" 2> "$1/cluster.err" \
    || { cat "$1/cluster.err" >&2; return 1; }
  yq 'select(.kind == "Cluster" and .apiVersion == "cluster.x-k8s.io/v1beta1") | .spec.clusterNetwork.pods.cidrBlocks[]' \
    "$1/cluster.yaml" > "$1/cluster.pods"
  yq 'select(.kind == "Cluster" and .apiVersion == "cluster.x-k8s.io/v1beta1") | .spec.clusterNetwork.services.cidrBlocks[]' \
    "$1/cluster.yaml" > "$1/cluster.services"
}

# $1: output directory. Writes pool.pods and pool.services, one CIDR per line.
pool_cidrs() {
  helm template kubernetes-nodes-myk8s-md0 "$REPO_ROOT/packages/apps/kubernetes-nodes" \
    --namespace tenant-test \
    --set cluster=myk8s \
    --set _cluster.cluster-domain=cozy.local \
    --set instanceType="" \
    --set resources.cpu=2 --set resources.memory=4Gi \
    --show-only templates/talos-reconcile-job.yaml \
    > "$1/pool.yaml" 2> "$1/pool.err" \
    || { cat "$1/pool.err" >&2; return 1; }
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$1/pool.yaml" > "$1/cmd.sh"
  awk '
    /(^|="?\$\()cat <<EOF/ { print "cat <<EOF"; inblock=1; next }
    inblock && /^EOF$/      { print "EOF"; inblock=0; next }
    inblock                 { print }
  ' "$1/cmd.sh" > "$1/heredoc.sh"
  sh "$1/heredoc.sh" > "$1/tct.yaml" 2> "$1/heredoc.err" \
    || { cat "$1/heredoc.err" >&2; return 1; }
  yq 'select(.kind == "TalosConfigTemplate") | .spec.template.spec.data' "$1/tct.yaml" > "$1/machineconfig.yaml"
  yq '.cluster.network.podSubnets[]' "$1/machineconfig.yaml" > "$1/pool.pods"
  yq '.cluster.network.serviceSubnets[]' "$1/machineconfig.yaml" > "$1/pool.services"
}

# $1: directory, $2: pods or services, $3: what to call it in a message.
assert_agree() {
  cluster=$(cat "$1/cluster.$2")
  pool=$(cat "$1/pool.$2")
  # An empty read means a field moved or a render came out short; comparing
  # "" with "" would pass on nothing.
  if [ -z "$cluster" ] || [ -z "$pool" ]; then
    echo "FAIL: could not read the $3 CIDRs from both renders."
    echo "  kubernetes Cluster.spec.clusterNetwork:          '${cluster:-<missing>}'"
    echo "  kubernetes-nodes machine config cluster.network: '${pool:-<missing>}'"
    return 1
  fi
  if [ "$cluster" != "$pool" ]; then
    echo "FAIL: the tenant $3 CIDRs differ between the two charts."
    echo "  packages/apps/kubernetes (CAPI Cluster):                $cluster"
    echo "  packages/apps/kubernetes-nodes (worker machine config): $pool"
    echo "Workers would route $3 traffic over a range the control plane does not"
    echo "use. Change both charts together. Moving the worker side also renames"
    echo "the content-hashed TalosConfigTemplate and rolls every worker."
    return 1
  fi
}

@test "kubernetes and kubernetes-nodes render the same pod CIDRs" {
  TMP=$(mktemp -d)
  cluster_cidrs "$TMP"
  pool_cidrs "$TMP"
  assert_agree "$TMP" pods pod
  rm -rf "$TMP"
}

@test "kubernetes and kubernetes-nodes render the same service CIDRs" {
  TMP=$(mktemp -d)
  cluster_cidrs "$TMP"
  pool_cidrs "$TMP"
  assert_agree "$TMP" services service
  rm -rf "$TMP"
}
