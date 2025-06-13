#!/usr/bin/env bats

# -----------------------------------------------------------------------------
# Cozystack end‑to‑end provisioning test (Bats)
# -----------------------------------------------------------------------------

@test "Create tenant with isolated mode enabled" {
  kubectl create -f - <<EOF
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
  resourceQuotas: {}
  seaweedfs: false
EOF
  kubectl wait hr/tenant-test -n tenant-root --timeout=1m --for=condition=ready
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
}

@test "Create a tenant Kubernetes control plane" {
  kubectl create -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: test
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
      enabled: true
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
  nodeGroups:
    md0:
      ephemeralStorage: 20Gi
      gpus: []
      instanceType: u1.medium
      maxReplicas: 10
      minReplicas: 0
      resources:
        cpu: ""
        memory: ""
      roles:
      - ingress-nginx
  storageClass: replicated
EOF
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
  timeout 10 sh -ec 'until kubectl get kamajicontrolplane -n tenant-test kubernetes-test; do sleep 1; done'
  kubectl wait --for=condition=TenantControlPlaneCreated kamajicontrolplane -n tenant-test kubernetes-test --timeout=4m
  kubectl wait tcp -n tenant-test kubernetes-test --timeout=2m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready
  kubectl wait deploy --timeout=4m --for=condition=available -n tenant-test kubernetes-test kubernetes-test-cluster-autoscaler kubernetes-test-kccm kubernetes-test-kcsi-controller
  kubectl wait machinedeployment kubernetes-test-md0 -n tenant-test --timeout=1m --for=jsonpath='{.status.replicas}'=2
  kubectl wait machinedeployment kubernetes-test-md0 -n tenant-test --timeout=10m --for=jsonpath='{.status.v1beta2.readyReplicas}'=2
}

@test "Create a VM Disk" {
  name='test'
  kubectl create -f - <<EOF
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
EOF
  sleep 5
  kubectl -n tenant-test wait hr vm-disk-$name --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$name --timeout=100s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$name --timeout=100s --for=jsonpath='{.status.phase}'=Bound
}

@test "Create a VM Instance" {
  diskName='test'
  name='test'
  kubectl create -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMInstance
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  externalMethod: PortList
  externalPorts:
  - 22
  running: true
  instanceType: "u1.medium"
  instanceProfile: ubuntu
  disks:
    - name: $diskName
  gpus: []
  resources:
    cpu: ""
    memory: ""
  sshKeys:
  - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF
    test@test
  cloudInit: |
    #cloud-config
    users:
      - name: test
        shell: /bin/bash
        sudo: ['ALL=(ALL) NOPASSWD: ALL']
        groups: sudo
        ssh_authorized_keys:
          - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF test@test
  cloudInitSeed: ""
EOF
  sleep 5
  timeout 20 sh -ec "until kubectl -n tenant-test get vmi vm-instance-$name -o jsonpath='{.status.interfaces[0].ipAddress}' | grep -q '[0-9]'; do sleep 5; done"
  kubectl -n tenant-test wait hr vm-instance-$name --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait vm vm-instance-$name --timeout=20s --for=condition=ready
}

@test "Create a Virtual Machine" {
  name='test'
  kubectl create -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  externalMethod: PortList
  externalPorts:
  - 22
  instanceType: "u1.medium"
  instanceProfile: ubuntu
  systemDisk:
    image: ubuntu
    storage: 5Gi
    storageClass: replicated
  gpus: []
  resources:
    cpu: ""
    memory: ""
  sshKeys:
  - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF
    test@test
  cloudInit: |
    #cloud-config
    users:
      - name: test
        shell: /bin/bash
        sudo: ['ALL=(ALL) NOPASSWD: ALL']
        groups: sudo
        ssh_authorized_keys:
          - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF test@test
  cloudInitSeed: ""
EOF
  sleep 5
  kubectl -n tenant-test wait hr virtual-machine-$name --timeout=10s --for=condition=ready
  kubectl -n tenant-test wait dv virtual-machine-$name --timeout=100s --for=condition=ready
  kubectl -n tenant-test wait pvc virtual-machine-$name --timeout=100s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait vm virtual-machine-$name --timeout=100s --for=condition=ready
  timeout 120 sh -ec "until kubectl -n tenant-test get vmi virtual-machine-$name -o jsonpath='{.status.interfaces[0].ipAddress}' | grep -q '[0-9]'; do sleep 10; done"
}

@test "Create DB PostgreSQL" {
  name='test'
  kubectl create -f - <<EOF
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
    testuser:
      password: xai7Wepo
  databases:
    testdb:
      roles:
        admin:
        - testuser
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/postgres-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3 --keep-daily=3 --keep-within-weekly=1m"
    s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
    s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
    resticPassword: ChaXoveekoh6eigh4siesheeda2quai0
  resources: {}
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr postgres-$name --timeout=50s --for=condition=ready
  kubectl -n tenant-test wait job.batch postgres-$name-init-job --timeout=50s --for=condition=Complete
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-r -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-ro -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc postgres-$name-rw -o jsonpath='{.spec.ports[0].port}' | grep -q '5432'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-r -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-ro -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 120 sh -ec "until kubectl -n tenant-test get endpoints postgres-$name-rw -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
}

@test "Create DB MySQL" {
  name='test'
  kubectl create -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: MySQL
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 10Gi
  replicas: 2
  storageClass: ""
  users:
    testuser:
      maxUserConnections: 1000
      password: xai7Wepo
  databases:
    testdb:
      roles:
        admin:
        - testuser
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/postgres-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3 --keep-daily=3 --keep-within-weekly=1m"
    s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
    s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
    resticPassword: ChaXoveekoh6eigh4siesheeda2quai0
  resources: {}
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr mysql-$name --timeout=30s --for=condition=ready
  timeout 80 sh -ec "until kubectl -n tenant-test get svc mysql-$name -o jsonpath='{.spec.ports[0].port}' | grep -q '3306'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get endpoints mysql-$name -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/mysql-$name --timeout=110s --for=jsonpath='{.status.replicas}'=2
  timeout 80 sh -ec "until kubectl -n tenant-test get svc mysql-$name-metrics -o jsonpath='{.spec.ports[0].port}' | grep -q '9104'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get endpoints mysql-$name-metrics -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test wait deployment.apps/mysql-$name-metrics --timeout=90s --for=jsonpath='{.status.replicas}'=1
}

@test "Create DB ClickHouse" {
  name='test'
  kubectl create -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ClickHouse
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 10Gi
  logStorageSize: 2Gi
  shards: 1
  replicas: 2
  storageClass: ""
  logTTL: 15
  users:
    testuser:
      password: xai7Wepo
  backup:
    enabled: false
    s3Region: us-east-1
    s3Bucket: s3.example.org/clickhouse-backups
    schedule: "0 2 * * *"
    cleanupStrategy: "--keep-last=3 --keep-daily=3 --keep-within-weekly=1m"
    s3AccessKey: oobaiRus9pah8PhohL1ThaeTa4UVa7gu
    s3SecretKey: ju3eum4dekeich9ahM1te8waeGai0oog
    resticPassword: ChaXoveekoh6eigh4siesheeda2quai0
  resources: {}
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr clickhouse-$name --timeout=20s --for=condition=ready
  timeout 100 sh -ec "until kubectl -n tenant-test get svc chendpoint-clickhouse-$name -o jsonpath='{.spec.ports[*].port}' | grep -q '8123 9000'; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/chi-clickhouse-$name-clickhouse-0-0 --timeout=120s --for=jsonpath='{.status.replicas}'=2
  timeout 80 sh -ec "until kubectl -n tenant-test get endpoints chi-clickhouse-$name-clickhouse-0-0 -o jsonpath='{.subsets[*].addresses[*].ip}' | grep -q '[0-9]'; do sleep 10; done"
  timeout 100 sh -ec "until kubectl -n tenant-test get svc chi-clickhouse-$name-clickhouse-0-0 -o jsonpath='{.spec.ports[0].port}' | grep -q '9000 8123 9009'; do sleep 10; done"
  kubectl -n tenant-test wait statefulset.apps/chi-clickhouse-$name-clickhouse-0-1 --timeout=140s --for=jsonpath='{.status.replicas}'=2
}
