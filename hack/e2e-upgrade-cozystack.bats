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

  # Latest stable (non-prerelease) tag from the previous minor line.
  # Per SemVer, any tag containing a hyphen is a pre-release.
  local prev_release
  prev_release=$(git tag -l "v${major}.${prev_minor}.*" --sort=-v:refname \
    | grep -v -- '-' | head -1)

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

@test "Install previous version of Cozystack from OCI" {
  PREV_RELEASE=$(cat "$STATE_DIR/prev-release")
  local prev_version="${PREV_RELEASE#v}"

  echo "Installing Cozystack ${PREV_RELEASE} from oci://ghcr.io/cozystack/cozystack/cozy-installer"

  # Install the same way end users do — pull the published OCI chart
  helm upgrade --install cozystack \
    oci://ghcr.io/cozystack/cozystack/cozy-installer \
    --version "${prev_version}" \
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

  # Wait until HelmReleases appear & reconcile. The 10-release floor is a
  # conservative signal that the operator has started creating HRs for the
  # isp-full variant (which produces many more); below 10 means the operator
  # isn't reconciling yet.
  timeout 180 sh -ec 'until [ $(kubectl get hr -A --no-headers 2>/dev/null | wc -l) -gt 10 ]; do sleep 1; done'
  sleep 5
  kubectl get hr -A \
    | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' \
    | sh -ex

  # Fail fast on a broken baseline when STRICT_BASELINE=1 (CI). On long-lived
  # dev clusters, leave as a warning so pre-existing noise doesn't block runs.
  if kubectl get hr -A | grep -v " True " | grep -v NAME; then
    kubectl get hr -A
    if [ "${STRICT_BASELINE:-0}" = "1" ]; then
      echo "ERROR: HelmReleases failed to reconcile (previous version)" >&2
      exit 1
    fi
    echo "WARNING: Some HelmReleases not reconciled (previous version); continuing with tolerant baseline" >&2
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
  [ -n "$pg_pod" ] || { echo "No running PostgreSQL pod found for postgres-${name}" >&2; exit 1; }

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

@test "Deploy MariaDB with test data" {
  local name='upgrade-mariadb'

  kubectl -n tenant-test delete mariadbs.apps.cozystack.io "$name" --ignore-not-found --timeout=2m || true

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: MariaDB
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 2
  storageClass: ""
  users:
    upgradeuser:
      maxUserConnections: 100
      password: upgrade-test-pw
  databases:
    upgradedb:
      roles:
        admin:
        - upgradeuser
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/mariadb-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3"
    s3AccessKey: placeholder
    s3SecretKey: placeholder
    resticPassword: placeholder
  resources: {}
  resourcesPreset: "nano"
EOF

  sleep 5
  kubectl -n tenant-test wait hr "mariadb-$name" --timeout=120s --for=condition=ready
  kubectl -n tenant-test wait statefulset.apps/mariadb-$name --timeout=5m --for=jsonpath='{.status.readyReplicas}'=2
  timeout 60 sh -ec "until kubectl -n tenant-test get endpoints mariadb-${name} -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 5; done"

  local my_pod
  my_pod=$(kubectl get pods -n tenant-test \
    -l "app.kubernetes.io/instance=mariadb-${name}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')
  [ -n "$my_pod" ] || { echo "No running MariaDB pod found for mariadb-${name}" >&2; exit 1; }

  # Seed 3 known rows via the primary service (handles replica topology)
  local root_pw
  root_pw=$(kubectl get secret mariadb-${name}-credentials -n tenant-test \
    -o jsonpath='{.data.root}' | base64 -d)
  kubectl exec -n tenant-test "$my_pod" -- \
    env MYSQL_PWD="$root_pw" mysql -h "mariadb-${name}-primary" -u root upgradedb -e "
      CREATE TABLE IF NOT EXISTS upgrade_canary (
        id INT AUTO_INCREMENT PRIMARY KEY,
        data VARCHAR(64) NOT NULL
      );
      INSERT INTO upgrade_canary (data) VALUES
        ('mariadb-pre-upgrade-row-1'),
        ('mariadb-pre-upgrade-row-2'),
        ('mariadb-pre-upgrade-row-3');
    "

  local count
  count=$(kubectl exec -n tenant-test "$my_pod" -- \
    env MYSQL_PWD="$root_pw" mysql -h "mariadb-${name}-primary" -u root -N -B upgradedb -e "SELECT count(*) FROM upgrade_canary;")
  echo "MariaDB pre-upgrade row count: $count"
  [ "$count" -eq 3 ]

  echo "upgrade-mariadb" > "$STATE_DIR/mariadb-instance-name"
}

@test "Deploy Kafka cluster" {
  local name='upgrade-kafka'

  kubectl -n tenant-test delete kafkas.apps.cozystack.io "$name" --ignore-not-found --timeout=2m || true
  kubectl -n tenant-test wait kafkas.apps.cozystack.io "$name" --for=delete --timeout=2m 2>/dev/null || true

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kafka
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  kafka:
    size: 5Gi
    replicas: 2
    storageClass: ""
    resources: {}
    resourcesPreset: "nano"
  zookeeper:
    size: 5Gi
    replicas: 2
    storageClass: ""
    resources: {}
    resourcesPreset: "nano"
  topics:
    - name: upgradeCanary
      partitions: 1
      replicas: 2
      config:
        min.insync.replicas: 1
EOF

  sleep 5
  kubectl -n tenant-test wait hr "kafka-$name" --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait kafka "$name" --timeout=5m --for=condition=ready

  # Record the Kafka CR generation so we can check it survives untouched
  kubectl get kafka -n tenant-test "$name" \
    -o jsonpath='{.metadata.uid}' > "$STATE_DIR/kafka-uid"
  echo "$name" > "$STATE_DIR/kafka-instance-name"
}

@test "Deploy VirtualMachine" {
  local name='upgrade-vm'

  kubectl -n tenant-test delete vminstances.apps.cozystack.io "$name" --ignore-not-found --timeout=2m || true
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io "$name" --ignore-not-found --timeout=2m || true

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $name
  namespace: tenant-test
spec:
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  optical: false
  storage: 5Gi
  storageClass: replicated
---
apiVersion: apps.cozystack.io/v1alpha1
kind: VMInstance
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  running: true
  instanceType: "u1.medium"
  instanceProfile: ubuntu
  disks:
    - name: $name
  gpus: []
  sshKeys: []
  cloudInit: |
    #cloud-config
    users:
      - name: cozy
        shell: /bin/bash
  cloudInitSeed: ""
EOF

  kubectl -n tenant-test wait hr "vm-disk-$name" --timeout=30s --for=condition=ready
  kubectl -n tenant-test wait dv "vm-disk-$name" --timeout=5m --for=condition=ready
  kubectl -n tenant-test wait pvc "vm-disk-$name" --timeout=3m --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait hr "vm-instance-$name" --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait vm "vm-instance-$name" --timeout=2m --for=condition=Ready

  # Record VMI UID so we can verify the instance itself survives (not just a recreate)
  kubectl get vmi -n tenant-test "vm-instance-$name" \
    -o jsonpath='{.metadata.uid}' > "$STATE_DIR/vm-uid"
  echo "$name" > "$STATE_DIR/vm-instance-name"
}

@test "Deploy tenant Kubernetes cluster" {
  local name='upgrade-k8s'
  local k8s_version
  k8s_version=$(yq 'keys | sort_by(.) | .[-1]' packages/apps/kubernetes/files/versions.yaml)

  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "$name" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kuberneteses.apps.cozystack.io "$name" --for=delete --timeout=2m 2>/dev/null || true

  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: $name
  namespace: tenant-test
spec:
  addons:
    certManager:
      enabled: false
      valuesOverride: {}
    cilium:
      valuesOverride: {}
    fluxcd:
      enabled: false
      valuesOverride: {}
    gatewayAPI:
      enabled: false
    gpuOperator:
      enabled: false
      valuesOverride: {}
    ingressNginx:
      enabled: false
      hosts: []
      valuesOverride: {}
    monitoringAgents:
      enabled: false
      valuesOverride: {}
    verticalPodAutoscaler:
      valuesOverride: {}
  controlPlane:
    apiServer:
      resources: {}
      resourcesPreset: small
    controllerManager:
      resources: {}
      resourcesPreset: micro
    konnectivity:
      server:
        resources: {}
        resourcesPreset: micro
    replicas: 2
    scheduler:
      resources: {}
      resourcesPreset: micro
  host: ""
  nodeGroups: {}
  storageClass: replicated
  version: "${k8s_version}"
EOF

  # Wait for the Kamaji control plane to spin up. We skip waiting for the
  # tenant-node-dependent add-ons (nodeGroups is empty) — just verifying the
  # control plane CR survives the upgrade is enough to catch CRD breakage.
  timeout 120 sh -ec 'until kubectl get kamajicontrolplane -n tenant-test "kubernetes-'"$name"'" >/dev/null 2>&1; do sleep 2; done'
  kubectl wait --for=condition=TenantControlPlaneCreated \
    kamajicontrolplane -n tenant-test "kubernetes-$name" --timeout=5m
  kubectl wait tcp -n tenant-test "kubernetes-$name" \
    --timeout=5m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready

  kubectl get tcp -n tenant-test "kubernetes-$name" \
    -o jsonpath='{.metadata.uid}' > "$STATE_DIR/k8s-tcp-uid"
  echo "$name" > "$STATE_DIR/k8s-instance-name"
}

@test "Record pre-upgrade state" {
  mkdir -p "$STATE_DIR"

  # Snapshot HelmRelease count
  kubectl get hr -A --no-headers | wc -l > "$STATE_DIR/pre-hr-count"
  echo "Pre-upgrade HelmRelease count: $(cat "$STATE_DIR/pre-hr-count")"

  # Baseline sets for post-upgrade delta comparison. In the CI sandbox these
  # are empty (clean start); on long-lived clusters they capture pre-existing
  # noise so we only fail on upgrade-caused regressions.
  kubectl get hr -A --no-headers 2>/dev/null \
    | awk '$4 != "True" {print $1"/"$2}' | sort > "$STATE_DIR/pre-hr-not-ready"
  kubectl get pods -A --no-headers 2>/dev/null \
    | awk '/CrashLoopBackOff/ {print $1"/"$2}' | sort > "$STATE_DIR/pre-crashloop-pods"
  kubectl get pv --no-headers 2>/dev/null \
    | awk '$5 != "Bound" {print $1}' | sort > "$STATE_DIR/pre-unbound-pvs"

  # Pre-upgrade cozystack migration version (for post-upgrade assertion).
  # Coalesce missing CM, kubectl failure, and empty stdout all to "0".
  local previous_version
  previous_version=$(kubectl get cm cozystack-version -n cozy-system \
    -o jsonpath='{.data.version}' 2>/dev/null || true)
  echo "${previous_version:-0}" > "$STATE_DIR/pre-cozystack-version"

  # Snapshot pod state (for debugging if upgrade breaks things)
  kubectl get pods -A --no-headers > "$STATE_DIR/pre-pods"
  echo "Pre-upgrade pod count: $(wc -l < "$STATE_DIR/pre-pods")"
}

# ===================================================================
# Phase 3: Upgrade to the current version
# ===================================================================

@test "Upgrade Cozystack to current version" {
  PREV_RELEASE=$(cat "$STATE_DIR/prev-release")
  local current_tag="${UPGRADE_TARGET_TAG:-}"
  if [ -z "$current_tag" ]; then
    current_tag=$(git describe --tags --exact-match --match 'v*' 2>/dev/null)
  fi
  local current_version="${current_tag#v}"
  echo "Upgrading from $PREV_RELEASE to $current_tag (oci://ghcr.io/cozystack/cozystack/cozy-installer:${current_version})"

  # Pull the published OCI chart — same mechanism users run during upgrades.
  # Timeout is generous: the installer renders cozystack-migration-hook as a
  # pre-upgrade Job (backoffLimit=3), and helm --wait blocks on it.
  helm upgrade cozystack \
    oci://ghcr.io/cozystack/cozystack/cozy-installer \
    --version "${current_version}" \
    --namespace cozy-system \
    --wait \
    --timeout 10m

  # Verify the new operator is available
  kubectl wait deployment/cozystack-operator -n cozy-system \
    --timeout=2m --for=condition=Available

  # CRDs may be updated — wait for them to be established
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
}

# ===================================================================
# Phase 4: Post-upgrade validation
# ===================================================================

@test "Wait for HelmReleases to reconcile after upgrade (no new failures)" {
  # Give Flux time to detect the new package source and start reconciling
  sleep 10

  local max_wait=900  # 15 minutes
  local interval=10
  local elapsed=0
  local pre_not_ready="$STATE_DIR/pre-hr-not-ready"

  # Wait until no HRs are newly not-ready (i.e., not present in the pre-upgrade baseline)
  while [ $elapsed -lt $max_wait ]; do
    local new_not_ready
    new_not_ready=$(comm -23 \
      <(kubectl get hr -A --no-headers 2>/dev/null | awk '$4 != "True" {print $1"/"$2}' | sort) \
      "$pre_not_ready")
    if [ -z "$new_not_ready" ]; then
      echo "No HelmReleases newly not-ready after ${elapsed}s"
      break
    fi
    local count
    count=$(printf '%s\n' "$new_not_ready" | grep -c . || true)
    echo "Waiting for $count newly not-ready HelmReleases (${elapsed}s / ${max_wait}s)..."
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  # Final check: fail only on HRs that weren't already failing pre-upgrade
  local new_not_ready
  new_not_ready=$(comm -23 \
    <(kubectl get hr -A --no-headers 2>/dev/null | awk '$4 != "True" {print $1"/"$2}' | sort) \
    "$pre_not_ready")
  if [ -n "$new_not_ready" ]; then
    echo "HelmReleases newly not-ready after upgrade:" >&2
    echo "$new_not_ready" >&2
    kubectl get hr -A >&2
    exit 1
  fi
}

@test "Verify migration state after upgrade" {
  local expected actual previous
  # Target version from the upgraded platform chart
  expected=$(yq '.migrations.targetVersion' packages/core/platform/values.yaml)
  actual=$(kubectl get cm cozystack-version -n cozy-system -o jsonpath='{.data.version}')
  previous=$(cat "$STATE_DIR/pre-cozystack-version")

  echo "cozystack-version: pre=$previous post=$actual chart_target=$expected"

  # Version must match what the new platform chart expects (catches silent skips / wrong image)
  [ "$actual" = "$expected" ]
  # Version must not regress
  [ "$actual" -ge "$previous" ]

  # If a migration was required, verify the Job completed successfully
  if [ "$previous" -lt "$expected" ]; then
    kubectl wait job/cozystack-migration-hook -n cozy-system \
      --for=condition=Complete --timeout=5m
  fi
}

@test "Verify no new CrashLoopBackOff pods after upgrade" {
  # Allow a brief settling period for pods to restart
  sleep 10

  local new_crashloop
  new_crashloop=$(comm -23 \
    <(kubectl get pods -A --no-headers 2>/dev/null | awk '/CrashLoopBackOff/ {print $1"/"$2}' | sort) \
    "$STATE_DIR/pre-crashloop-pods")

  if [ -n "$new_crashloop" ]; then
    echo "New CrashLoopBackOff pods detected after upgrade:" >&2
    echo "$new_crashloop" >&2
    exit 1
  fi

  echo "No new CrashLoopBackOff pods"
}

@test "Verify no new unbound PersistentVolumes after upgrade" {
  local new_unbound
  new_unbound=$(comm -23 \
    <(kubectl get pv --no-headers 2>/dev/null | awk '$5 != "Bound" {print $1}' | sort) \
    "$STATE_DIR/pre-unbound-pvs")

  if [ -n "$new_unbound" ]; then
    echo "New unbound PersistentVolumes detected after upgrade:" >&2
    echo "$new_unbound" >&2
    exit 1
  fi

  echo "No new unbound PersistentVolumes"
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
  [ -n "$pg_pod" ] || { echo "No running PostgreSQL pod found for postgres-${name} after upgrade" >&2; exit 1; }

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

@test "Verify MariaDB data survived upgrade" {
  local name
  name=$(cat "$STATE_DIR/mariadb-instance-name")

  # Wait for MariaDB to stabilize after upgrade
  timeout 180 sh -ec "until kubectl -n tenant-test get endpoints mariadb-${name} -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 5; done"
  kubectl -n tenant-test wait statefulset.apps/mariadb-$name --timeout=5m --for=jsonpath='{.status.readyReplicas}'=2

  local my_pod
  my_pod=$(kubectl get pods -n tenant-test \
    -l "app.kubernetes.io/instance=mariadb-${name}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')
  [ -n "$my_pod" ] || { echo "No running MariaDB pod found for mariadb-${name} after upgrade" >&2; exit 1; }

  local root_pw
  root_pw=$(kubectl get secret mariadb-${name}-credentials -n tenant-test \
    -o jsonpath='{.data.root}' | base64 -d)

  local count
  count=$(kubectl exec -n tenant-test "$my_pod" -- \
    env MYSQL_PWD="$root_pw" mysql -h "mariadb-${name}-primary" -u root -N -B upgradedb -e "SELECT count(*) FROM upgrade_canary;")
  echo "MariaDB post-upgrade row count: $count"
  [ "$count" -eq 3 ]

  local data
  data=$(kubectl exec -n tenant-test "$my_pod" -- \
    env MYSQL_PWD="$root_pw" mysql -h "mariadb-${name}-primary" -u root -N -B upgradedb -e "SELECT data FROM upgrade_canary ORDER BY id;")
  echo "MariaDB post-upgrade data:"
  echo "$data"
  echo "$data" | grep -q "mariadb-pre-upgrade-row-1"
  echo "$data" | grep -q "mariadb-pre-upgrade-row-2"
  echo "$data" | grep -q "mariadb-pre-upgrade-row-3"

  echo "MariaDB data integrity verified"
}

@test "Verify Kafka cluster survived upgrade" {
  local name
  name=$(cat "$STATE_DIR/kafka-instance-name")
  local pre_uid
  pre_uid=$(cat "$STATE_DIR/kafka-uid")

  kubectl -n tenant-test wait kafka "$name" --timeout=10m --for=condition=ready

  local post_uid
  post_uid=$(kubectl get kafka -n tenant-test "$name" -o jsonpath='{.metadata.uid}')
  [ "$pre_uid" = "$post_uid" ] || { echo "Kafka UID changed during upgrade: $pre_uid → $post_uid" >&2; exit 1; }

  # Topic should still be present on the broker — check via the Strimzi KafkaTopic CR
  kubectl get kafkatopic -n tenant-test \
    -l "strimzi.io/cluster=kafka-${name}" \
    -o jsonpath='{.items[*].spec.topicName}' | grep -qw 'upgradeCanary'

  echo "Kafka cluster and topic survived upgrade"
}

@test "Verify VirtualMachine survived upgrade" {
  local name
  name=$(cat "$STATE_DIR/vm-instance-name")
  local pre_uid
  pre_uid=$(cat "$STATE_DIR/vm-uid")

  kubectl -n tenant-test wait vm "vm-instance-$name" --timeout=5m --for=condition=Ready

  local post_uid
  post_uid=$(kubectl get vmi -n tenant-test "vm-instance-$name" \
    -o jsonpath='{.metadata.uid}')
  [ "$pre_uid" = "$post_uid" ] || { echo "VMI UID changed during upgrade: $pre_uid → $post_uid" >&2; exit 1; }

  local phase
  phase=$(kubectl get vmi -n tenant-test "vm-instance-$name" \
    -o jsonpath='{.status.phase}')
  [ "$phase" = "Running" ] || { echo "VMI phase is $phase, expected Running" >&2; exit 1; }

  echo "VirtualMachine survived upgrade (same UID, still Running)"
}

@test "Verify tenant Kubernetes cluster survived upgrade" {
  local name
  name=$(cat "$STATE_DIR/k8s-instance-name")
  local pre_uid
  pre_uid=$(cat "$STATE_DIR/k8s-tcp-uid")

  kubectl wait tcp -n tenant-test "kubernetes-$name" \
    --timeout=10m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready

  local post_uid
  post_uid=$(kubectl get tcp -n tenant-test "kubernetes-$name" \
    -o jsonpath='{.metadata.uid}')
  [ "$pre_uid" = "$post_uid" ] || { echo "TenantControlPlane UID changed during upgrade: $pre_uid → $post_uid" >&2; exit 1; }

  echo "Tenant Kubernetes control plane survived upgrade"
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
