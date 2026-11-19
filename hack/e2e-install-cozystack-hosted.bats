#!/usr/bin/env bats
# Install suite for the isp-hosted lane (.github/workflows/e2e-isp-hosted.yaml).
#
# The cluster is kind: its CNI, kube-proxy and default StorageClass belong to
# the host, which is what the hosted installer variant and the isp-hosted
# platform variant assume. The pass condition is the one the variant has never
# had checked on a live cluster: every HelmRelease the platform creates goes
# Ready, cozystack-platform and tenant-root included, with no Cilium CRDs, no
# LINSTOR and no `replicated` StorageClass anywhere.
#
# What this does not cover, by design: tenant applications, ingress (kind has
# no LoadBalancer implementation, so an ingress Service never gets an address),
# and anything in the iaas bundle, which this variant refuses to render.

@test "Install Cozystack with the hosted installer variant" {
  helm upgrade installer packages/core/installer \
    --install \
    --namespace cozy-system \
    --create-namespace \
    --set cozystackOperator.variant=hosted \
    --set cozystackOperator.helmReleaseInterval=30s \
    --timeout 2m

  # On hosted the labeler hook runs as an ordinary pod on the host CNI rather
  # than on hostNetwork, so it is the path this lane exercises and no other does.
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx privileged
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.cozystack\.io/system}' | grep -qx true

  # Rollout rather than condition=Available, for the reason given at the same
  # step in hack/e2e-install-cozystack.bats.
  kubectl rollout status deployment/cozystack-operator -n cozy-system --timeout=5m || {
    kubectl describe pod -n cozy-system -l app=cozystack-operator 2>&1 || true
    kubectl logs -n cozy-system -l app=cozystack-operator --all-containers --tail=50 2>&1 || true
    kubectl logs -n cozy-system -l app=cozystack-operator --all-containers --previous --tail=50 2>&1 || true
    false
  }

  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl wait crd/packagesources.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl get packagesource cozystack.cozystack-platform >/dev/null 2>&1; do sleep 2; done'

  kubectl apply -f hack/e2e-isp-hosted-package.yaml

  # 1800s against the 900s the isp-full lane allows: nothing is pre-pulled here,
  # so one node pulls every platform image cold before its release can go Ready.
  . hack/e2e-wait-helmreleases.sh
  cozy_wait_all_helmreleases_ready 1800 11 5 2
}

@test "The platform and the root tenant are Ready" {
  kubectl wait hr/cozystack-platform -n cozy-system --for=condition=ready --timeout=1m
  kubectl wait hr/tenant-root -n tenant-root --for=condition=ready --timeout=1m
}

@test "The isp-hosted variant took effect" {
  # The install gate above is equally green for any variant that converges, so
  # this reads what the platform actually emitted, not what the lane asked for.
  kubectl get package cozystack.networking -o jsonpath='{.spec.variant}' | grep -qx noop
  for pkg in cozystack.linstor cozystack.metallb cozystack.multus cozystack.kubevirt; do
    if kubectl get package "$pkg" >/dev/null 2>&1; then
      echo "Package $pkg exists, but isp-hosted must not emit it" >&2
      exit 1
    fi
  done
}

@test "The cluster carries none of what isp-hosted leaves to the host" {
  # The variant's promise is that nothing in it needs Cilium or a
  # Cozystack-provided StorageClass. A green install proves that only if those
  # were really absent while it converged.
  # Any CRD in the group counts, not only the policy kinds. The premise is a
  # cluster with no Cilium at all, and a partial set breaks it even where the
  # tenant guard, which needs both policy kinds, would still skip its policies.
  # Read before counting, so a failed list fails the test instead of counting
  # as zero.
  crd_groups="$(kubectl get crd -o jsonpath='{range .items[*]}{.spec.group}{"\n"}{end}')"
  cilium_crds="$(printf '%s\n' "$crd_groups" | grep -cx 'cilium\.io' || true)"
  if [ "$cilium_crds" -ne 0 ]; then
    echo "$cilium_crds cilium.io CRDs are installed; the lane no longer tests a cluster without them" >&2
    exit 1
  fi
  for sc in replicated local; do
    if kubectl get storageclass "$sc" >/dev/null 2>&1; then
      echo "StorageClass $sc exists; the lane no longer tests a host-provided class alone" >&2
      exit 1
    fi
  done
}
