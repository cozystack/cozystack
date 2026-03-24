#!/usr/bin/env bats

@test "Required installer chart exists" {
  if [ ! -f packages/core/installer/Chart.yaml ]; then
    echo "Missing: packages/core/installer/Chart.yaml" >&2
    exit 1
  fi
}

@test "Install Cozystack" {
  # Install cozy-installer chart (operator installs CRDs on startup via --install-crds)
  helm upgrade installer packages/core/installer \
    --install \
    --namespace cozy-system \
    --create-namespace \
    --wait \
    --timeout 2m

  # Verify the operator deployment is available
  kubectl wait deployment/cozystack-operator -n cozy-system --timeout=1m --for=condition=Available

  # Wait for operator to install CRDs (happens at startup before reconcile loop).
  # kubectl wait fails immediately if the CRD does not exist yet, so poll until it appears first.
  timeout 120 sh -ec 'until kubectl wait crd/packages.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl wait crd/packagesources.cozystack.io --for=condition=Established --timeout=10s 2>/dev/null; do sleep 2; done'

  # Wait for operator to create the platform PackageSource
  timeout 120 sh -ec 'until kubectl get packagesource cozystack.cozystack-platform >/dev/null 2>&1; do sleep 2; done'

  # Create platform Package with isp-full variant
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
        bundles:
          enabledPackages:
            - cozystack.external-dns-application
EOF

  # Wait until HelmReleases appear & reconcile them
  timeout 180 sh -ec 'until [ $(kubectl get hr -A --no-headers 2>/dev/null | wc -l) -gt 10 ]; do sleep 1; done'
  sleep 5
  kubectl get hr -A | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' | sh -ex

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
  kubectl wait --for=condition=Available apiservices/v1alpha1.apps.cozystack.io apiservices/v1alpha1.core.cozystack.io --timeout=2m
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
  kubectl patch package cozystack.cozystack-platform --type merge -p '{"spec":{"components":{"platform":{"values":{"publishing":{"exposedServices":["api","dashboard","cdi-uploadproxy","vm-exportproxy","keycloak"]}}}}}}'

  # NGINX ingress controller
  timeout 60 sh -ec 'until kubectl get deploy root-ingress-controller -n tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deploy/root-ingress-controller -n tenant-root --timeout=5m --for=condition=available

  # etcd statefulset
  kubectl wait sts/etcd -n tenant-root --for=jsonpath='{.status.readyReplicas}'=3 --timeout=5m

  # VictoriaMetrics components
  kubectl wait vmalert/vmalert-shortterm vmalertmanager/alertmanager -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=15m
  kubectl wait vlclusters/generic -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m
  kubectl wait vmcluster/shortterm vmcluster/longterm -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m

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
  kubectl patch package cozystack.cozystack-platform --type merge -p '{"spec":{"components":{"platform":{"values":{"authentication":{"oidc":{"enabled":true}}}}}}}'

  timeout 120 sh -ec 'until kubectl get hr -n cozy-keycloak keycloak keycloak-configure keycloak-operator >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/keycloak hr/keycloak-configure hr/keycloak-operator -n cozy-keycloak --timeout=10m --for=condition=ready
}

@test "Enable Gateway API and verify per-tenant Gateway" {
  # Enable Gateway API on platform
  kubectl patch package cozystack.cozystack-platform --type merge -p '{"spec":{"components":{"platform":{"values":{"gateway":{"gatewayAPI":true}}}}}}'

  # Enable gateway on root tenant
  kubectl patch tenants/root -n tenant-root --type merge -p '{"spec":{"gateway":true}}'
  kubectl wait hr/tenant-root -n tenant-root --timeout=2m --for=condition=ready

  # Wait for per-tenant gateway HelmRelease to appear and become ready
  timeout 120 sh -ec 'until kubectl get hr -n tenant-root gateway >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/gateway -n tenant-root --timeout=5m --for=condition=ready

  # Verify GatewayClass created and accepted
  timeout 60 sh -ec 'until [ "$(kubectl get gatewayclass tenant-root -o jsonpath='"'"'{.status.conditions[?(@.type=="Accepted")].status}'"'"' 2>/dev/null)" = "True" ]; do sleep 1; done'

  # Force reconcile system HelmReleases so they pick up gateway-api: true
  flux reconcile hr dashboard -n cozy-dashboard --force
  flux reconcile hr cozystack-api -n cozy-cozystack-api --force || true

  # Wait for a per-component Gateway to get an address (merged Service)
  timeout 300 sh -ec 'until [ -n "$(kubectl get gateway dashboard -n cozy-dashboard -o jsonpath='"'"'{.status.addresses[0].value}'"'"' 2>/dev/null)" ]; do sleep 1; done'

  gateway_ip=$(kubectl get gateway dashboard -n cozy-dashboard -o jsonpath='{.status.addresses[0].value}')
  if [ -z "$gateway_ip" ]; then
    echo "Gateway has no IP address assigned" >&2
    kubectl get gateway dashboard -n cozy-dashboard -o yaml >&2
    exit 1
  fi
  echo "Gateway IP: $gateway_ip"
}

@test "Verify system HTTPRoutes and TLSRoutes via Gateway" {
  # Dashboard HTTPRoute
  if ! timeout 60 sh -ec 'until [ "$(kubectl get httproute dashboard-web -n cozy-dashboard -o jsonpath='"'"'{.status.parents[0].conditions[?(@.type=="Accepted")].status}'"'"' 2>/dev/null)" = "True" ]; do sleep 1; done'; then
    echo "Dashboard HTTPRoute not accepted by Gateway" >&2
    kubectl get httproute dashboard-web -n cozy-dashboard -o yaml >&2
    exit 1
  fi

  # Keycloak HTTPRoute
  if ! timeout 60 sh -ec 'until [ "$(kubectl get httproute keycloak -n cozy-keycloak -o jsonpath='"'"'{.status.parents[0].conditions[?(@.type=="Accepted")].status}'"'"' 2>/dev/null)" = "True" ]; do sleep 1; done'; then
    echo "Keycloak HTTPRoute not accepted by Gateway" >&2
    kubectl get httproute keycloak -n cozy-keycloak -o yaml >&2
    exit 1
  fi

  # Kubernetes API TLSRoute
  if ! timeout 60 sh -ec 'until [ "$(kubectl get tlsroute kubernetes-api -n default -o jsonpath='"'"'{.status.parents[0].conditions[?(@.type=="Accepted")].status}'"'"' 2>/dev/null)" = "True" ]; do sleep 1; done'; then
    echo "API TLSRoute not accepted by Gateway" >&2
    kubectl get tlsroute kubernetes-api -n default -o yaml >&2
    exit 1
  fi
}

@test "Access services via Gateway API" {
  # With mergeGateways, all per-component Gateways share one Service
  # Get the merged Service IP from any Gateway's address
  gateway_ip=$(kubectl get gateway dashboard -n cozy-dashboard -o jsonpath='{.status.addresses[0].value}')

  # HTTP-to-HTTPS redirect (301)
  http_code=$(curl -sS --resolve "dashboard.example.org:80:${gateway_ip}" \
    "http://dashboard.example.org" --max-time 10 -o /dev/null -w '%{http_code}')
  if [ "$http_code" != "301" ]; then
    echo "Expected HTTP 301 redirect, got ${http_code}" >&2
    exit 1
  fi

  # Dashboard via HTTPS (302/303 redirect to Keycloak is expected when OIDC is enabled)
  http_code=$(curl -sS -k --resolve "dashboard.example.org:443:${gateway_ip}" \
    "https://dashboard.example.org" --max-time 30 -o /dev/null -w '%{http_code}')
  if [ "$http_code" != "200" ] && [ "$http_code" != "302" ] && [ "$http_code" != "303" ]; then
    echo "Failed to access Dashboard via Gateway, got HTTP ${http_code}" >&2
    exit 1
  fi

  # Kubernetes API via TLS passthrough (401/403 expected without credentials)
  http_code=$(curl -sS -k --resolve "api.example.org:443:${gateway_ip}" \
    "https://api.example.org" --max-time 30 -o /dev/null -w '%{http_code}')
  if [ "$http_code" != "401" ] && [ "$http_code" != "403" ]; then
    echo "Expected HTTP 401 or 403 from API server via Gateway, got ${http_code}" >&2
    exit 1
  fi
}

@test "Verify Grafana via tenant Gateway" {
  # gateway: true already set in previous test

  # Wait for monitoring to reconcile with gateway config
  if ! kubectl wait hr/monitoring -n tenant-root --timeout=3m --for=condition=ready; then
    flux reconcile hr monitoring -n tenant-root --force
    kubectl wait hr/monitoring -n tenant-root --timeout=2m --for=condition=ready
  fi

  # Wait for Grafana per-component Gateway to be Programmed
  timeout 120 sh -ec 'until kubectl get gateway grafana -n tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait gateway/grafana -n tenant-root --timeout=2m --for=condition=Programmed

  # Verify Grafana HTTPRoute is accepted
  if ! timeout 60 sh -ec 'until [ "$(kubectl get httproute grafana -n tenant-root -o jsonpath='"'"'{.status.parents[0].conditions[?(@.type=="Accepted")].status}'"'"' 2>/dev/null)" = "True" ]; do sleep 1; done'; then
    echo "Grafana HTTPRoute not accepted" >&2
    kubectl get httproute grafana -n tenant-root -o yaml >&2
    exit 1
  fi

  # Access Grafana via tenant Gateway (merged Service)
  timeout 60 sh -ec 'until [ -n "$(kubectl get gateway grafana -n tenant-root -o jsonpath='"'"'{.status.addresses[0].value}'"'"' 2>/dev/null)" ]; do sleep 1; done'
  grafana_gw_ip=$(kubectl get gateway grafana -n tenant-root -o jsonpath='{.status.addresses[0].value}')
  if ! curl -sS -k --resolve "grafana.example.org:443:${grafana_gw_ip}" \
    "https://grafana.example.org" --max-time 30 | grep -q Found; then
    echo "Failed to access Grafana via Gateway at ${grafana_gw_ip}" >&2
    exit 1
  fi
}

@test "Ingress still works alongside Gateway API" {
  ingress_ip=$(kubectl get svc root-ingress-controller -n tenant-root -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  if ! curl -sS -k --resolve "grafana.example.org:443:${ingress_ip}" \
    "https://grafana.example.org" --max-time 30 | grep -q Found; then
    echo "Ingress broken after enabling Gateway API" >&2
    exit 1
  fi
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
