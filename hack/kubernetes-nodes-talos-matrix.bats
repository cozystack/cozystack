#!/usr/bin/env bats
# Unit test: the refusals of the Talos to Kubernetes support-matrix guard in
# packages/apps/kubernetes-nodes (kubernetes-nodes.assertTalosSupportsKubernetes,
# called from templates/nodegroup.yaml for talos.version and again for the
# release osImage resolves to).
#
# On the shipped chart a refusal is unreachable: the values schema offers only
# Kubernetes versions the v1.13 row lists, and files/versions.yaml, which another
# template fails on first, holds the same set. The guard is there for the day a
# Talos row stops listing a version the chart still offers. So this renders a
# copy of the chart with --skip-schema-validation and one extra versions.yaml row
# for a version the matrix does not list. The pairings the guard lets through
# are in the chart's own tests/talos_k8s_support_matrix_test.yaml.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/apps/kubernetes-nodes"

# render_pool <args...> renders the pool chart copy with the given --set flags and
# prints helm's output, errors included. The copy lives in the test's own tmpdir.
render_pool() {
  copy="$BATS_TEST_TMPDIR/chart"
  if [ ! -d "$copy" ]; then
    cp -RL "$CHART" "$copy"
    printf '"v1.30": "v1.30.14"\n' >> "$copy/files/versions.yaml"
  fi
  helm template kubernetes-nodes-myk8s-md0 "$copy" --namespace tenant-test \
    --skip-schema-validation \
    --set cluster=myk8s \
    --set _cluster.cluster-domain=cozy.local \
    --set minReplicas=1 --set maxReplicas=3 \
    --set instanceType= --set resources.cpu=2 --set resources.memory=4Gi \
    --set diskSize=20Gi --set storageClass=replicated \
    "$@" 2>&1
}

@test "an in-window pairing renders on the same copy, so the refusals below are the guard's" {
  out=$(render_pool --set version=v1.31) || { echo "$out" >&2; return 1; }
  printf '%s\n' "$out" | grep -q '^kind: KubevirtMachineTemplate$'
}

@test "a Kubernetes version outside the row of talos.version is refused" {
  if out=$(render_pool --set version=v1.30); then
    echo "render succeeded with Kubernetes v1.30 on the default Talos" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF "Kubernetes v1.30 is not supported by Talos v1.13.6. Supported versions: [v1.31 v1.32 v1.33 v1.34 v1.35 v1.36]. Bump 'version' or set talos.version explicitly." \
    || { echo "$out" >&2; return 1; }
}

@test "an older patch release of a listed Talos minor is checked against the same row" {
  if out=$(render_pool --set version=v1.30 --set talos.version=v1.13.0); then
    echo "render succeeded with Kubernetes v1.30 on Talos v1.13.0: the guard is not keyed on the minor" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF 'Kubernetes v1.30 is not supported by Talos v1.13.0.' \
    || { echo "$out" >&2; return 1; }
}

# talos.version names a minor the matrix does not list, so the first call lets the
# pool through and only the call fed the osImage release can refuse it.
@test "a Kubernetes version outside the row of osImage.factory.version is refused" {
  if out=$(render_pool --set version=v1.30 --set talos.version=v1.12.0 --set osImage.factory.version=v1.13.0); then
    echo "render succeeded with Kubernetes v1.30 on Talos v1.13.0 reached through osImage.factory" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF 'Kubernetes v1.30 is not supported by Talos v1.13.0.' \
    && printf '%s\n' "$out" | grep -qF 'nodeGroup md0 reaches this Talos release through its osImage override' \
    || { echo "$out" >&2; return 1; }
}

@test "a Kubernetes version outside the row of osImage.builtin.version is refused" {
  if out=$(render_pool --set version=v1.30 --set talos.version=v1.12.0 --set osImage.builtin.version=v1.13.0); then
    echo "render succeeded with Kubernetes v1.30 on Talos v1.13.0 reached through osImage.builtin" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -qF 'Kubernetes v1.30 is not supported by Talos v1.13.0.' \
    && printf '%s\n' "$out" | grep -qF 'nodeGroup md0 reaches this Talos release through its osImage override' \
    || { echo "$out" >&2; return 1; }
}
