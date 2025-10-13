#!/usr/bin/env bats

@test "Required installer assets exist" {
  if [ ! -f _out/assets/cozystack-installer.yaml ]; then
    echo "Missing: _out/assets/cozystack-installer.yaml" >&2
    exit 1
  fi
}

@test "Install Cozystack" {
  # Create namespace & configmap required by installer
  kubectl create namespace cozy-system --dry-run=client -o yaml | kubectl apply -f -
  kubectl create configmap cozystack -n cozy-system \
          --from-literal=bundle-name=paas-full \
          --from-literal=ipv4-pod-cidr=10.244.0.0/16 \
          --from-literal=ipv4-pod-gateway=10.244.0.1 \
          --from-literal=ipv4-svc-cidr=10.96.0.0/16 \
          --from-literal=ipv4-join-cidr=100.64.0.0/16 \
          --from-literal=root-host=example.org \
          --from-literal=api-server-endpoint=https://192.168.123.10:6443 \
          --dry-run=client -o yaml | kubectl apply -f -

  # Apply installer manifests from file
  kubectl apply -f _out/assets/cozystack-installer.yaml

  # Wait for the installer deployment to become available
  kubectl wait deployment/cozystack -n cozy-system --timeout=1m --for=condition=Available

  # Wait until HelmReleases appear & reconcile them
  timeout 60 sh -ec 'until kubectl get hr -A -l cozystack.io/system-app=true | grep -q cozys; do sleep 1; done'
  sleep 5
  kubectl get hr -A -l cozystack.io/system-app=true | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' | sh -ex

  # Fail the test if any HelmRelease is not Ready
  if kubectl get hr -A | grep -v " True " | grep -v NAME; then
    kubectl get hr -A
    echo "Some HelmReleases failed to reconcile" >&2
  fi
}

@test "Wait for Cluster‑API provider deployments" {
  # Wait for Cluster‑API provider deployments
  timeout 60 sh -ec 'until kubectl get deploy -n cozy-cluster-api capi-controller-manager capi-kamaji-controller-manager capi-kubeadm-bootstrap-controller-manager capi-operator-cluster-api-operator capk-controller-manager >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deployment/capi-controller-manager deployment/capi-kamaji-controller-manager deployment/capi-kubeadm-bootstrap-controller-manager deployment/capi-operator-cluster-api-operator deployment/capk-controller-manager -n cozy-cluster-api --timeout=1m --for=condition=available
}

@test "Wait for LINSTOR and configure storage" {
  # Linstor controller and nodes
  kubectl wait deployment/linstor-controller -n cozy-linstor --timeout=5m --for=condition=available
  timeout 60 sh -ec 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) -eq 3 ]; do sleep 1; done'

  created_pools=$(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor sp l -s data --pastable | awk '$2 == "data" {printf " " $4} END{printf " "}')
  for node in srv1 srv2 srv3; do
    case $created_pools in
      *" $node "*) echo "Storage pool 'data' already exists on node $node"; continue;;
    esac
    kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs ${node} /dev/vdc --pool-name data --storage-pool data
  done

  # Storage classes
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

@test "Wait for MetalLB and configure address pool" {
  # MetalLB address pool
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

@test "Check Cozystack API service" {
  kubectl wait --for=condition=Available apiservices/v1alpha1.apps.cozystack.io --timeout=2m
}

@test "Configure Tenant and wait for applications" {
  # Patch root tenant and wait for its releases

  kubectl patch tenants/root -n tenant-root --type merge -p '{"spec":{"host":"example.org","ingress":true,"monitoring":true,"etcd":true,"isolated":true, "seaweedfs": true}}'

  timeout 60 sh -ec 'until kubectl get hr -n tenant-root etcd ingress monitoring seaweedfs tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/etcd hr/ingress hr/tenant-root hr/seaweedfs -n tenant-root --timeout=4m --for=condition=ready

  # TODO: Workaround ingress unvailability issue
  if ! kubectl wait hr/monitoring -n tenant-root --timeout=2m --for=condition=ready; then
    flux reconcile hr monitoring -n tenant-root --force
    kubectl wait hr/monitoring -n tenant-root --timeout=2m --for=condition=ready
  fi

  if ! kubectl wait hr/seaweedfs-system -n tenant-root --timeout=2m --for=condition=ready; then
    flux reconcile hr seaweedfs-system -n tenant-root --force
    kubectl wait hr/seaweedfs-system -n tenant-root --timeout=2m --for=condition=ready
  fi


  # Expose Cozystack services through ingress
  kubectl patch configmap/cozystack -n cozy-system --type merge -p '{"data":{"expose-services":"api,dashboard,cdi-uploadproxy,vm-exportproxy,keycloak"}}'

  # NGINX ingress controller
  timeout 60 sh -ec 'until kubectl get deploy root-ingress-controller -n tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deploy/root-ingress-controller -n tenant-root --timeout=5m --for=condition=available

  # etcd statefulset
  kubectl wait sts/etcd -n tenant-root --for=jsonpath='{.status.readyReplicas}'=3 --timeout=5m

  # VictoriaMetrics components
  kubectl wait vmalert/vmalert-shortterm vmalertmanager/alertmanager -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=15m
  kubectl wait vlogs/generic -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m
  kubectl wait vmcluster/shortterm vmcluster/longterm -n tenant-root --for=jsonpath='{.status.clusterStatus}'=operational --timeout=5m

  # Grafana
  kubectl wait clusters.postgresql.cnpg.io/grafana-db -n tenant-root --for=condition=ready --timeout=5m
  kubectl wait deploy/grafana-deployment -n tenant-root --for=condition=available --timeout=5m

  # Verify Grafana via ingress
  ingress_ip=$(kubectl get svc root-ingress-controller -n tenant-root -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  if ! curl -sS -k "https://${ingress_ip}" -H 'Host: grafana.example.org' --max-time 30 | grep -q Found; then
    echo "Failed to access Grafana via ingress at ${ingress_ip}" >&2
    exit 1
  fi
}

@test "Keycloak OIDC stack is healthy" {
  kubectl patch configmap/cozystack -n cozy-system --type merge -p '{"data":{"oidc-enabled":"true"}}'

  timeout 120 sh -ec 'until kubectl get hr -n cozy-keycloak keycloak keycloak-configure keycloak-operator >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/keycloak hr/keycloak-configure hr/keycloak-operator -n cozy-keycloak --timeout=10m --for=condition=ready
}

@test "Create tenant with isolated mode enabled" {
  kubectl -n tenant-root get tenants.apps.cozystack.io test || 
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
  kubectl wait hr/tenant-test -n tenant-root --timeout=1m --for=condition=ready
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
  # Wait for ResourceQuota to appear and assert values
  timeout 60 sh -ec 'until [ "$(kubectl get quota -n tenant-test --no-headers 2>/dev/null | wc -l)" -ge 1 ]; do sleep 1; done'
  kubectl get quota -n tenant-test \
    -o jsonpath='{range .items[*]}{.spec.hard.requests\.memory}{" "}{.spec.hard.requests\.storage}{"\n"}{end}' \
    | grep -qx '137438953472 100Gi'

  # Assert LimitRange defaults for containers
  kubectl get limitrange -n tenant-test \
  -o jsonpath='{range .items[*].spec.limits[*]}{.default.cpu}{" "}{.default.memory}{" "}{.defaultRequest.cpu}{" "}{.defaultRequest.memory}{"\n"}{end}' \
  | grep -qx '250m 128Mi 25m 128Mi'
}
