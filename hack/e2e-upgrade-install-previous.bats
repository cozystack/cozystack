#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Upgrade lane — Phase 1: install the PREVIOUS stable release ("upgrade FROM").
#
# This is the thin, previous-version-specific orchestration for the release
# upgrade test (see docs/agents/e2e-testing.md "Upgrade testing (release lane)"
# and cozystack/cozystack#2401). Every heavy primitive is a SHARED script/helper
# so this file stays small and legible:
#   - the all-HelmReleases-Ready gate  -> hack/e2e-wait-hr-ready.sh
#   - LINSTOR storage pool + StorageClasses + MetalLB pool -> hack/e2e-post-install-prep.sh
#   - the cilium orphaned-endpoint watchdog -> hack/e2e-cilium-leak-healer.yaml
#
# The previous release installs with the SAME Package/PackageSource API main
# uses (verified: v1.5.x carries cozystack.io/v1alpha1 Package +
# cozystack.cozystack-platform PackageSource), so the platform bring-up below is
# identical to hack/e2e-install-cozystack.bats apart from the chart source.
#
# Release name is `cozystack` (how end users install) so Phase 3
# (hack/e2e-upgrade-apply.bats) can `helm upgrade cozystack` this same release.
#
# Requires:
#   - a provisioned Talos cluster (hack/e2e-prepare-cluster.bats already ran)
#   - $UPGRADE_FROM_VERSION set to the baseline tag (e.g. v1.5.3), resolved on
#     the runner by hack/upgrade-prev-version.sh and threaded through the Makefile
# -----------------------------------------------------------------------------

@test "Deploy cilium-leak-healer watchdog (best-effort)" {
  # Same interim mitigation as the install suite (cilium/cilium#38313 class):
  # an in-cluster Job that evicts an orphaned Cilium endpoint / restarts the
  # node's cilium-agent on the "IP already in use" leak. Deployed first so it
  # covers the WHOLE upgrade lane's churn — two platform installs plus the app
  # + tenant-Kubernetes seeding. Best-effort: never fail the suite on a
  # band-aid. Single source of truth is hack/e2e-cilium-endpoint-leak-healer.sh,
  # shipped to the pod via this ConfigMap. Remove once a fixed Cilium ships.
  kubectl create configmap cilium-leak-healer -n kube-system \
    --from-file=heal.sh=hack/e2e-cilium-endpoint-leak-healer.sh \
    --dry-run=client -o yaml | kubectl apply -f - || true
  kubectl apply -f hack/e2e-cilium-leak-healer.yaml || true
  if kubectl -n kube-system get job cilium-leak-healer >/dev/null 2>&1; then
    echo "cilium-leak-healer Job created"
  else
    echo "WARNING: cilium-leak-healer Job NOT created — watchdog inactive this run"
  fi
}

@test "Install previous Cozystack from OCI" {
  : "${UPGRADE_FROM_VERSION:?UPGRADE_FROM_VERSION must be set (e.g. v1.5.3)}"
  # Helm chart versions carry no leading v; the tag does.
  prev_version="${UPGRADE_FROM_VERSION#v}"
  echo "Installing Cozystack ${UPGRADE_FROM_VERSION} from oci://ghcr.io/cozystack/cozystack/cozy-installer (release: cozystack)"

  # Install the exact bits end users get — the published OCI chart pulls
  # anonymously and carries the previous version's operator image + platform
  # packages digest baked in, so this is a faithful "from" state.
  helm upgrade --install cozystack \
    oci://ghcr.io/cozystack/cozystack/cozy-installer \
    --version "${prev_version}" \
    --namespace cozy-system \
    --create-namespace \
    --set cozystackOperator.helmReleaseInterval=30s \
    --wait \
    --timeout 5m

  # The pre-install hook must stamp PSA + identity labels on cozy-system (same
  # invariant the install suite checks) — operator pods need enforce=privileged.
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx privileged
  kubectl get ns cozy-system -o jsonpath='{.metadata.labels.cozystack\.io/system}' | grep -qx true

  kubectl wait deployment/cozystack-operator -n cozy-system --timeout=2m --for=condition=Available

  # Operator installs the CRDs at startup, then creates the platform PackageSource.
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl wait crd/packagesources.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl get packagesource cozystack.cozystack-platform >/dev/null 2>&1; do sleep 2; done'
}

@test "Create platform Package and reconcile previous version" {
  # Version-stable platform config: networking + publishing (valid since the 1.4
  # line, so the same manifest installs on the baseline AND is re-rendered by the
  # current chart on upgrade), plus migrations.etcdAdoptSkipBackup.
  #
  # etcdAdoptSkipBackup makes the etcd v1alpha2 adoption migration (run during the
  # upgrade) adopt the live etcd WITHOUT its pre-adoption S3 safety snapshot. That
  # snapshot is structurally impossible in an e2e sandbox: the Etcd backup
  # strategy has no caCert field, so it needs a trusted-cert (ACME) external S3
  # endpoint, which a sandbox on example.org with no DNS/ACME cannot provide
  # (proven on dev10 — the snapshot only completes against a real ACME S3 host).
  # The adoption itself (pod/PVC re-ownership, operator swap, etcd 3.5->3.6 roll)
  # still runs — that is what this lane exercises. Set here (no strict values
  # schema on either version) so it is in place when the current chart upgrades.
  kubectl apply -f - <<EOF
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  variant: isp-full
  components:
    platform:
      values:
        networking:
          podCIDR: "10.244.0.0/16"
          podGateway: "10.244.0.1"
          serviceCIDR: "10.96.0.0/16"
          joinCIDR: "100.64.0.0/16"
        publishing:
          host: "example.org"
          apiServerEndpoint: "https://192.168.123.10:6443"
        migrations:
          etcdAdoptSkipBackup: true
EOF

  # Configure storage (LINSTOR pool + StorageClasses) and the MetalLB pool in the
  # background; it waits for its own prerequisites and overlaps the HR reconcile.
  hack/e2e-post-install-prep.sh > /tmp/upgrade-post-install-prep.log 2>&1 &
  POST_PREP_PID=$!

  # Wait until the operator has emitted the platform HRs before gating on them.
  timeout 180 sh -ec 'until [ $(kubectl get hr -A --no-headers 2>/dev/null | wc -l) -gt 10 ]; do sleep 1; done'

  echo "Waiting for post-install-prep to complete"
  if ! wait $POST_PREP_PID; then
    cat /tmp/upgrade-post-install-prep.log >&2
    echo "post-install-prep failed" >&2
    exit 1
  fi
  cat /tmp/upgrade-post-install-prep.log

  # The baseline install must be pristine (CI sandbox is always fresh): every
  # platform HR must reconcile. Shared gate — same teeth as the install suite.
  hack/e2e-wait-hr-ready.sh 15m
}

@test "Check Cozystack API service (baseline)" {
  timeout 60 sh -ec 'until kubectl get apiservices/v1alpha1.apps.cozystack.io apiservices/v1alpha1.core.cozystack.io >/dev/null 2>&1; do sleep 2; done'
  kubectl wait --for=condition=Available apiservices/v1alpha1.apps.cozystack.io apiservices/v1alpha1.core.cozystack.io --timeout=2m
}

@test "Configure root tenant (baseline)" {
  # Mirror a realistic root tenant: etcd (a legacy etcd.aenix.io cluster for the
  # v1alpha2 adoption migration to exercise on upgrade), ingress, and seaweedfs.
  # seaweedfs is the historical default (install-cozystack.bats enables it too),
  # so the baseline resembles a real cluster being upgraded. The etcd-adoption
  # migration does NOT depend on the seaweedfs backup chain here — that snapshot
  # is skipped via migrations.etcdAdoptSkipBackup (see the platform Package
  # above), so we only need the seaweedfs HR itself Ready, not the deeper
  # S3/bucket/creds projection.
  kubectl patch tenants/root -n tenant-root --type merge \
    -p '{"spec":{"host":"example.org","ingress":true,"etcd":true,"isolated":true,"seaweedfs":true}}'

  timeout 60 sh -ec 'until kubectl get hr -n tenant-root etcd ingress seaweedfs tenant-root >/dev/null 2>&1; do sleep 1; done'
  # seaweedfs installs as a serial chain (seaweedfs-db CNPG -> seaweedfs-system
  # raft -> seaweedfs wrapper), ~5-6min idle and longer under load; the
  # tenant-root parent flips Ready only after every child, so gate at 20m.
  kubectl wait hr/etcd hr/ingress hr/seaweedfs hr/tenant-root -n tenant-root --timeout=20m --for=condition=ready
}

@test "Create isolated test tenant (baseline)" {
  # Produces the tenant-test namespace the Chainsaw seed/verify suites deploy
  # into (matches the install suite's tenant so quotas/StorageClass are present).
  kubectl -n tenant-root get tenants.apps.cozystack.io test 2>/dev/null ||
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: test
  namespace: tenant-root
spec:
  etcd: false
  host: ""
  ingress: false
  isolated: true
  monitoring: false
  resourceQuotas:
    cpu: "60"
    memory: "128Gi"
    storage: "200Gi"
  seaweedfs: false
EOF
  timeout 60 sh -ec 'until kubectl get hr/tenant-test -n tenant-root >/dev/null 2>&1; do sleep 2; done'
  kubectl wait hr/tenant-test -n tenant-root --timeout=5m --for=condition=ready
  timeout 60 sh -ec 'until kubectl get namespace tenant-test >/dev/null 2>&1; do sleep 2; done'
  kubectl wait namespace tenant-test --timeout=30s --for=jsonpath='{.status.phase}'=Active
}
