#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Upgrade lane — Phase 3: upgrade the platform to the CURRENT version.
#
# Runs between the seed (Phase 2) and verify (Phase 4) Chainsaw invocations.
# Upgrades the `cozystack` Helm release installed in Phase 1 to the in-tree
# (current) installer chart — the operator image + platformSourceRef baked into
# the checked-out tree — which is exactly how a release upgrade happens: the new
# operator re-renders the platform PackageSource and the existing
# Package/cozystack.cozystack-platform reconciles to the new HRs, running the
# version-gated migrations along the way.
#
# Thin by design: the all-HelmReleases-Ready gate is the shared
# hack/e2e-wait-hr-ready.sh (same teeth as install), so this file only carries
# the upgrade command and the migration-advanced assertion.
# -----------------------------------------------------------------------------

@test "Upgrade Cozystack to current version" {
  # NO --reuse-values: the whole point is to adopt the CURRENT chart's values
  # (new operator image + new platformSourceRef digest). --reuse-values would
  # pin the previous operator image and there would be no real upgrade. Re-set
  # the reconcile interval that Phase 1 passed, since we are not reusing values.
  helm upgrade cozystack packages/core/installer \
    --namespace cozy-system \
    --set cozystackOperator.helmReleaseInterval=30s \
    --wait \
    --timeout 5m

  # The operator rolls to the new image; wait for it before gating the platform.
  kubectl wait deployment/cozystack-operator -n cozy-system --timeout=3m --for=condition=Available
}

@test "Platform reconciles clean after upgrade" {
  # Every HelmRelease must return to Ready after the operator re-renders the
  # platform against the new source. Generous budget: the upgrade re-pulls image
  # deltas across three nodes and rolls stateful workloads, and a parent HR that
  # briefly flips InProgress re-reconciles every 1m until it converges.
  hack/e2e-wait-hr-ready.sh 20m
}

@test "Migration stamp advanced to the current target" {
  # cozystack-version's .data.version holds the migrations.targetVersion stamp
  # (created once with resource-policy:keep, advanced by the migration Jobs on
  # upgrade). Asserting it reached the CURRENT chart's targetVersion proves the
  # version-gated migrations actually ran — not just that HRs are Ready. The
  # baseline installs an older stamp (e.g. v1.5.3 = 45); the current tree is the
  # source of truth for the expected value.
  expected=$(yq '.migrations.targetVersion' packages/core/platform/values.yaml)
  if [ -z "$expected" ] || [ "$expected" = "null" ]; then
    echo "could not read migrations.targetVersion from packages/core/platform/values.yaml" >&2
    exit 1
  fi

  # The stamp is set by a Job, so poll briefly rather than reading once.
  timeout 120 sh -ec "until [ \"\$(kubectl get configmap cozystack-version -n cozy-system -o jsonpath='{.data.version}' 2>/dev/null)\" = \"${expected}\" ]; do sleep 3; done" || {
    actual=$(kubectl get configmap cozystack-version -n cozy-system -o jsonpath='{.data.version}' 2>/dev/null || echo "<missing>")
    echo "migration stamp did not reach ${expected}; cozystack-version .data.version = ${actual}" >&2
    exit 1
  }
  echo "migration stamp advanced to ${expected}"
}
