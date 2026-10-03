#!/usr/bin/env bats

# The isp-hosted lane (.github/workflows/e2e-isp-hosted.yaml) is the only CI
# coverage the isp-hosted platform variant and the hosted installer variant
# have. It runs once a day after merge, so a wiring mistake here goes unseen
# until a scheduled run: a Package naming another variant, or an install
# without the hosted installer variant, would leave the lane testing something
# other than its name says. These checks pin the wiring that makes the run mean
# "isp-hosted converged on a cluster that provides its own CNI and storage".

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/e2e-isp-hosted.yaml"
PACKAGE="$REPO_ROOT/hack/e2e-isp-hosted-package.yaml"
KIND_CONFIG="$REPO_ROOT/hack/e2e-isp-hosted-kind.yaml"
INSTALL="$REPO_ROOT/hack/e2e-install-cozystack-hosted.bats"
TESTING_MAKEFILE="$REPO_ROOT/packages/core/testing/Makefile"

@test "the lane's platform Package selects the isp-hosted variant" {
  name="$(yq -r '.metadata.name' "$PACKAGE")"
  variant="$(yq -r '.spec.variant' "$PACKAGE")"
  if [ "$name" != "cozystack.cozystack-platform" ] || [ "$variant" != "isp-hosted" ]; then
    echo "expected cozystack.cozystack-platform with variant isp-hosted, got $name / $variant" >&2
    exit 1
  fi
}

@test "the operator registers isp-hosted with its own values file" {
  grep -Fq '{"isp-hosted", []string{"values.yaml", "values-isp-hosted.yaml"}}' \
    "$REPO_ROOT/cmd/cozystack-operator/main.go"
  [ -f "$REPO_ROOT/packages/core/platform/values-isp-hosted.yaml" ]
}

@test "the install suite uses the hosted installer variant and the lane's Package" {
  grep -Eq -- '--set cozystackOperator\.variant=hosted([[:space:]]|$)' "$INSTALL"
  grep -Fq 'hack/e2e-isp-hosted-package.yaml' "$INSTALL"
}

@test "the install suite is reachable through the testing Makefile" {
  grep -Eq "^install-cozystack-hosted:" "$TESTING_MAKEFILE"
  grep -Fq 'hack/cozytest.sh hack/e2e-install-cozystack-hosted.bats' "$TESTING_MAKEFILE"
}

# The platform renders service addresses from networking.clusterDomain, and
# several system packages still carry the domain as a literal, so a kind
# cluster on its own default (cluster.local) would fail for a reason that has
# nothing to do with the variant.
@test "the kind cluster DNS domain matches the platform cluster domain" {
  want="$(yq -r '.spec.components.platform.values.networking.clusterDomain // ""' "$PACKAGE")"
  if [ -z "$want" ]; then
    want="$(yq -r '.networking.clusterDomain' "$REPO_ROOT/packages/core/platform/values.yaml")"
  fi
  dns="$(yq -r '.kubeadmConfigPatches[] | from_yaml | select(.kind == "ClusterConfiguration") | .networking.dnsDomain' "$KIND_CONFIG")"
  kubelet="$(yq -r '.kubeadmConfigPatches[] | from_yaml | select(.kind == "KubeletConfiguration") | .clusterDomain' "$KIND_CONFIG")"
  if [ "$dns" != "$want" ] || [ "$kubelet" != "$want" ]; then
    echo "platform clusterDomain=$want, kind dnsDomain=$dns, kubelet clusterDomain=$kubelet" >&2
    exit 1
  fi
}

@test "the workflow creates the cluster from the lane's kind config and runs the hosted install" {
  grep -Fq 'hack/e2e-isp-hosted-kind.yaml' "$WORKFLOW"
  grep -Fq 'install-cozystack-hosted' "$WORKFLOW"
}

@test "the workflow pins the kind binary by checksum and the node image by digest" {
  grep -Eq '^[[:space:]]+KIND_SHA256: "?[0-9a-f]{64}"?$' "$WORKFLOW"
  grep -Eq '^[[:space:]]+KIND_NODE_IMAGE: "?kindest/node:v[0-9.]+@sha256:[0-9a-f]{64}"?$' "$WORKFLOW"
  grep -Fq 'sha256sum --check' "$WORKFLOW"
}

# Runs the install suite's no-Cilium check against a stubbed kubectl. The copy
# drops the e2e- prefix so hack/cozytest.sh arms no cluster captures, which
# would otherwise run the stub as if it were a cluster.
@test "the suite's no-Cilium check fails on any cilium.io CRD, not only CiliumNetworkPolicy" {
  tmp="$(mktemp -d)"
  mkdir -p "$tmp/bin"
  cp "$INSTALL" "$tmp/hosted.bats"
  printf '%s\n' \
    '#!/bin/sh' \
    '[ "$1 $2" = "get storageclass" ] && exit 1' \
    '[ "$1 $2" = "get crd" ] || exit 2' \
    'shift 2' \
    '[ -n "$FAKE_LIST_FAILS" ] && exit 1' \
    'case "$*" in' \
    '  -o\ jsonpath=*) for n in $FAKE_CRDS; do echo "${n#*.}"; done ;;' \
    '  -*) exit 2 ;;' \
    '  *) for n in $FAKE_CRDS; do [ "$n" = "$1" ] && exit 0; done; exit 1 ;;' \
    'esac' > "$tmp/bin/kubectl"
  chmod +x "$tmp/bin/kubectl"

  rc=0
  PATH="$tmp/bin:$PATH" FAKE_CRDS="packages.cozystack.io ciliumendpoints.cilium.io" COZY_REPORT_DIR="$tmp/report" COZYTEST_TRACE=0 \
    "$REPO_ROOT/hack/cozytest.sh" "$tmp/hosted.bats" "The cluster carries none" > "$tmp/partial.log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "the check passed with ciliumendpoints.cilium.io installed" >&2
    exit 1
  fi

  rc=0
  PATH="$tmp/bin:$PATH" FAKE_CRDS="" FAKE_LIST_FAILS=1 COZY_REPORT_DIR="$tmp/report" COZYTEST_TRACE=0 \
    "$REPO_ROOT/hack/cozytest.sh" "$tmp/hosted.bats" "The cluster carries none" > "$tmp/unreadable.log" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "the check passed although the CRD list could not be read" >&2
    exit 1
  fi

  rc=0
  PATH="$tmp/bin:$PATH" FAKE_CRDS="packages.cozystack.io" COZY_REPORT_DIR="$tmp/report" COZYTEST_TRACE=0 \
    "$REPO_ROOT/hack/cozytest.sh" "$tmp/hosted.bats" "The cluster carries none" > "$tmp/clean.log" 2>&1 || rc=$?
  if [ "$rc" -ne 0 ] || ! grep -q "Test OK" "$tmp/clean.log"; then
    echo "the check did not pass on a cluster with no cilium.io CRD:" >&2
    cat "$tmp/clean.log" >&2
    exit 1
  fi
  rm -rf "$tmp"
}
