#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack upgrade test (Bats)
#
# Installs the previous stable minor release, deploys representative
# workloads, upgrades to the current version, and validates that
# existing workloads survive.
#
# Expects:
#   - A provisioned Talos cluster (e2e-prepare-cluster.bats already ran)
#   - UPGRADE_TARGET_TAG env var set to the current release tag (e.g. v0.43.0)
#     Falls back to `git describe --tags --exact-match` if unset.
# -----------------------------------------------------------------------------

STATE_DIR="/tmp/upgrade-test"

# ---------------------------------------------------------------------------
# Helper: resolve the latest stable release from the previous minor line
# ---------------------------------------------------------------------------
find_previous_release() {
  local current_tag="${UPGRADE_TARGET_TAG:-}"
  if [ -z "$current_tag" ]; then
    current_tag=$(git describe --tags --exact-match --match 'v*' 2>/dev/null || true)
  fi
  if [ -z "$current_tag" ]; then
    echo "ERROR: Cannot determine current tag. Set UPGRADE_TARGET_TAG." >&2
    return 1
  fi

  # v0.43.0-rc.1 → major=0, minor=43
  local version="${current_tag#v}"
  local major minor
  major=$(echo "$version" | cut -d. -f1)
  minor=$(echo "$version" | cut -d. -f2)

  if [ "$minor" -eq 0 ]; then
    echo "ERROR: No previous minor version exists (current minor is 0)" >&2
    return 1
  fi

  local prev_minor=$((minor - 1))

  # Latest stable (non-prerelease) tag from the previous minor line
  local prev_release
  prev_release=$(git tag -l "v${major}.${prev_minor}.*" --sort=-v:refname \
    | grep -v -E '-(rc|alpha|beta)\.' | head -1)

  if [ -z "$prev_release" ]; then
    echo "ERROR: No stable release found for v${major}.${prev_minor}.*" >&2
    return 1
  fi

  echo "$prev_release"
}

# ===================================================================
# Phase 1: Install the previous stable release
# ===================================================================

@test "Determine previous stable release" {
  mkdir -p "$STATE_DIR"
  PREV_RELEASE=$(find_previous_release)
  echo "Previous release: $PREV_RELEASE"
  echo "$PREV_RELEASE" > "$STATE_DIR/prev-release"
}

@test "Extract and install previous version of Cozystack" {
  PREV_RELEASE=$(cat "$STATE_DIR/prev-release")

  # Extract installer chart from the previous release tag
  local prev_installer="$STATE_DIR/prev-installer"
  rm -rf "$prev_installer"
  mkdir -p "$prev_installer/templates"

  git show "${PREV_RELEASE}:packages/core/installer/Chart.yaml" \
    > "$prev_installer/Chart.yaml"
  git show "${PREV_RELEASE}:packages/core/installer/values.yaml" \
    > "$prev_installer/values.yaml"
  git show "${PREV_RELEASE}:packages/core/installer/templates/cozystack-operator.yaml" \
    > "$prev_installer/templates/cozystack-operator.yaml"

  echo "Operator image (previous): $(yq '.cozystackOperator.image' "$prev_installer/values.yaml")"

  # Install previous version via Helm (same mechanism users use)
  helm upgrade installer "$prev_installer" \
    --install \
    --namespace cozy-system \
    --create-namespace \
    --wait \
    --timeout 2m

  # Verify the operator deployment is available
  kubectl wait deployment/cozystack-operator -n cozy-system \
    --timeout=2m --for=condition=Available

  # Wait for operator to install CRDs
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl wait crd/packagesources.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'

  # Wait for operator to create the platform PackageSource
  timeout 120 sh -ec 'until kubectl get packagesource cozystack.cozystack-platform >/dev/null 2>&1; do sleep 2; done'
}

@test "Create platform Package and wait for previous version to stabilize" {
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
EOF

  # Wait until HelmReleases appear & reconcile
  timeout 180 sh -ec 'until [ $(kubectl get hr -A --no-headers 2>/dev/null | wc -l) -gt 10 ]; do sleep 1; done'
  sleep 5
  kubectl get hr -A \
    | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' \
    | sh -ex

  if kubectl get hr -A | grep -v " True " | grep -v NAME; then
    kubectl get hr -A
    echo "Some HelmReleases failed to reconcile (previous version)" >&2
  fi
}

@test "Wait for LINSTOR and configure storage (pre-upgrade)" {
  kubectl wait deployment/linstor-controller -n cozy-linstor \
    --timeout=5m --for=condition=available
  timeout 60 sh -ec 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) -eq 3 ]; do sleep 1; done'

  created_pools=$(kubectl exec -n cozy-linstor deploy/linstor-controller -- \
    linstor sp l -s data --pastable | awk '$2 == "data" {printf " " $4} END{printf " "}')
  for node in srv1 srv2 srv3; do
    case $created_pools in
      *" $node "*) echo "Storage pool 'data' already exists on node $node"; continue;;
    esac
    kubectl exec -n cozy-linstor deploy/linstor-controller -- \
      linstor ps cdp zfs ${node} /dev/vdc --pool-name data --storage-pool data
  done

  kubectl apply -f - <<'EOF'
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/layerList: "storage"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "false"
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: replicated
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/autoPlace: "3"
  linstor.csi.linbit.com/layerList: "drbd storage"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "true"
  property.linstor.csi.linbit.com/DrbdOptions/auto-quorum: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-no-data-accessible: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-suspended-primary-outdated: force-secondary
  property.linstor.csi.linbit.com/DrbdOptions/Net/rr-conflict: retry-connect
volumeBindingMode: Immediate
allowVolumeExpansion: true
EOF
}

@test "Wait for MetalLB and configure address pool (pre-upgrade)" {
  kubectl apply -f - <<'EOF'
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  ipAddressPools: [cozystack]
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  addresses: [192.168.123.200-192.168.123.250]
  autoAssign: true
  avoidBuggyIPs: false
EOF
}

@test "Check Cozystack API service (pre-upgrade)" {
  kubectl wait --for=condition=Available \
    apiservices/v1alpha1.apps.cozystack.io \
    apiservices/v1alpha1.core.cozystack.io \
    --timeout=2m
}

@test "Create test tenant" {
  # Patch root tenant — enable only what's needed for tenant creation
  kubectl patch tenants/root -n tenant-root --type merge \
    -p '{"spec":{"host":"example.org","ingress":false,"monitoring":false,"etcd":true,"isolated":true,"seaweedfs":false}}'

  timeout 60 sh -ec 'until kubectl get hr -n tenant-root etcd tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/etcd hr/tenant-root -n tenant-root --timeout=4m --for=condition=ready

  # Create an isolated test tenant with resource quotas
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
    storage: "100Gi"
  seaweedfs: false
EOF
  kubectl wait hr/tenant-test -n tenant-root --timeout=2m --for=condition=ready
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
}

# ===================================================================
# Phase 2: Deploy workloads and seed test data
# ===================================================================

@test "Deploy PostgreSQL with test data" {
  local name='upgrade-pg'

  kubectl -n tenant-test delete postgreses.apps.cozystack.io "$name" --ignore-not-found --timeout=2m || true

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 2
  storageClass: ""
  postgresql:
    parameters:
      max_connections: 100
  quorum:
    minSyncReplicas: 0
    maxSyncReplicas: 0
  users:
    upgradeuser:
      password: upgrade-test-pw
  databases:
    upgradedb:
      roles:
        admin:
        - upgradeuser
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/postgres-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3"
    s3AccessKey: placeholder
    s3SecretKey: placeholder
    resticPassword: placeholder
  resources: {}
  resourcesPreset: "nano"
EOF

  sleep 5
  kubectl -n tenant-test wait hr "postgres-$name" --timeout=120s --for=condition=ready
  kubectl -n tenant-test wait "job.batch/postgres-${name}-init-job" --timeout=60s --for=condition=Complete

  # Wait for RW endpoint to have an address
  timeout 60 sh -ec "until kubectl -n tenant-test get endpoints postgres-${name}-rw -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 5; done"

  # Seed test data: create a table and insert 3 known rows
  local pg_pod
  pg_pod=$(kubectl get pods -n tenant-test \
    -l "cnpg.io/cluster=postgres-${name}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')

  kubectl exec -n tenant-test "$pg_pod" -- \
    psql -U postgres -d upgradedb -c "
      CREATE TABLE IF NOT EXISTS upgrade_canary (
        id serial PRIMARY KEY,
        data text NOT NULL,
        created_at timestamptz DEFAULT now()
      );
      INSERT INTO upgrade_canary (data) VALUES
        ('pre-upgrade-row-1'),
        ('pre-upgrade-row-2'),
        ('pre-upgrade-row-3');
    "

  # Verify seed data
  local count
  count=$(kubectl exec -n tenant-test "$pg_pod" -- \
    psql -U postgres -d upgradedb -t -A -c "SELECT count(*) FROM upgrade_canary;")
  echo "Pre-upgrade row count: $count"
  [ "$count" -eq 3 ]

  echo "upgrade-pg" > "$STATE_DIR/pg-instance-name"
}

@test "Record pre-upgrade state" {
  mkdir -p "$STATE_DIR"

  # Snapshot HelmRelease count
  kubectl get hr -A --no-headers | wc -l > "$STATE_DIR/pre-hr-count"
  echo "Pre-upgrade HelmRelease count: $(cat "$STATE_DIR/pre-hr-count")"

  # Snapshot pod state (for debugging if upgrade breaks things)
  kubectl get pods -A --no-headers > "$STATE_DIR/pre-pods"
  echo "Pre-upgrade pod count: $(wc -l < "$STATE_DIR/pre-pods")"
}

# ===================================================================
# Phase 3: Upgrade to the current version
# ===================================================================

@test "Upgrade Cozystack to current version" {
  PREV_RELEASE=$(cat "$STATE_DIR/prev-release")
  echo "Upgrading from $PREV_RELEASE to current version"
  echo "Operator image (current): $(yq '.cozystackOperator.image' packages/core/installer/values.yaml)"

  helm upgrade installer packages/core/installer \
    --install \
    --namespace cozy-system \
    --wait \
    --timeout 2m

  # Verify the new operator is available
  kubectl wait deployment/cozystack-operator -n cozy-system \
    --timeout=2m --for=condition=Available

  # CRDs may be updated — wait for them to be established
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
}

# ===================================================================
# Phase 4: Post-upgrade validation
# ===================================================================

@test "Wait for all HelmReleases to reconcile after upgrade" {
  # Give Flux time to detect the new package source and start reconciling
  sleep 10

  # Wait for all HelmReleases to become Ready (generous timeout for full reconciliation)
  local max_wait=900  # 15 minutes
  local interval=10
  local elapsed=0

  while [ $elapsed -lt $max_wait ]; do
    local not_ready
    not_ready=$(kubectl get hr -A --no-headers 2>/dev/null | grep -v " True " | wc -l)
    if [ "$not_ready" -eq 0 ]; then
      echo "All HelmReleases are Ready after ${elapsed}s"
      break
    fi
    echo "Waiting for $not_ready HelmReleases to reconcile (${elapsed}s / ${max_wait}s)..."
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  # Final check: fail if any HR is still not ready
  if kubectl get hr -A --no-headers | grep -v " True "; then
    echo "HelmReleases still not ready after ${max_wait}s:" >&2
    kubectl get hr -A >&2
    exit 1
  fi
}

@test "Verify no CrashLoopBackOff pods after upgrade" {
  # Allow a brief settling period for pods to restart
  sleep 10

  local crashloop_pods
  crashloop_pods=$(kubectl get pods -A --no-headers 2>/dev/null \
    | grep -i "CrashLoopBackOff" || true)

  if [ -n "$crashloop_pods" ]; then
    echo "CrashLoopBackOff pods detected after upgrade:" >&2
    echo "$crashloop_pods" >&2
    exit 1
  fi

  echo "No CrashLoopBackOff pods found"
}

@test "Verify all PersistentVolumes are Bound after upgrade" {
  local unbound_pvs
  unbound_pvs=$(kubectl get pv --no-headers 2>/dev/null \
    | grep -v "Bound" || true)

  if [ -n "$unbound_pvs" ]; then
    echo "Unbound PersistentVolumes detected after upgrade:" >&2
    echo "$unbound_pvs" >&2
    exit 1
  fi

  echo "All PersistentVolumes are Bound"
}

@test "Verify PostgreSQL data survived upgrade" {
  local name
  name=$(cat "$STATE_DIR/pg-instance-name")

  # Wait for the PostgreSQL pods to stabilize after upgrade
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-${name}-rw -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 5; done"

  local pg_pod
  pg_pod=$(kubectl get pods -n tenant-test \
    -l "cnpg.io/cluster=postgres-${name}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')

  # Check that the 3 pre-upgrade rows are still there
  local count
  count=$(kubectl exec -n tenant-test "$pg_pod" -- \
    psql -U postgres -d upgradedb -t -A -c "SELECT count(*) FROM upgrade_canary;")
  echo "Post-upgrade row count: $count"
  [ "$count" -eq 3 ]

  # Verify actual data values
  local data
  data=$(kubectl exec -n tenant-test "$pg_pod" -- \
    psql -U postgres -d upgradedb -t -A -c "SELECT data FROM upgrade_canary ORDER BY id;")
  echo "Post-upgrade data:"
  echo "$data"
  echo "$data" | grep -q "pre-upgrade-row-1"
  echo "$data" | grep -q "pre-upgrade-row-2"
  echo "$data" | grep -q "pre-upgrade-row-3"

  echo "PostgreSQL data integrity verified"
}

@test "Verify Cozystack API available after upgrade" {
  kubectl wait --for=condition=Available \
    apiservices/v1alpha1.apps.cozystack.io \
    apiservices/v1alpha1.core.cozystack.io \
    --timeout=2m

  echo "Cozystack API is available after upgrade"
}

@test "Verify platform pods are healthy after upgrade" {
  # Check that key system namespaces have no unhealthy pods
  local failed=false
  for ns in cozy-system cozy-fluxcd cozy-linstor cozy-metallb; do
    local not_running
    not_running=$(kubectl get pods -n "$ns" --no-headers 2>/dev/null \
      | grep -v -E "(Running|Completed|Succeeded)" || true)
    if [ -n "$not_running" ]; then
      echo "Unhealthy pods in $ns:" >&2
      echo "$not_running" >&2
      failed=true
    fi
  done

  if [ "$failed" = true ]; then
    exit 1
  fi

  echo "All platform pods are healthy after upgrade"
}
