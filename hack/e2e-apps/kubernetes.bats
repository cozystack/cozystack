#!/usr/bin/env bats

@test "Create a tenant Kubernetes control plane" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: $name
  namespace: tenant-test
spec:
  addons:
    certManager:
      enabled: true
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
      hosts:
        - example.org
      exposeMethod: Proxied
      valuesOverride: {}
    monitoringAgents:
      enabled: true
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
  sleep 10
  kubectl wait --timeout=20s namespace tenant-test --for=jsonpath='{.status.phase}'=Active
  kubectl -n tenant-test wait --timeout=10s kamajicontrolplane kubernetes-$name --for=jsonpath='{.status.conditions[0].status}'=True
  kubectl -n tenant-test wait --timeout=4m kamajicontrolplane kubernetes-$name --for=condition=TenantControlPlaneCreated
  kubectl -n tenant-test wait --timeout=210s tcp kubernetes-$name --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready
  kubectl -n tenant-test wait --timeout=4m deploy kubernetes-$name kubernetes-$name-cluster-autoscaler kubernetes-$name-kccm kubernetes-$name-kcsi-controller --for=condition=available
  kubectl -n tenant-test wait --timeout=1m machinedeployment kubernetes-$name-md0 --for=jsonpath='{.status.replicas}'=2
  kubectl -n tenant-test wait --timeout=10m machinedeployment kubernetes-$name-md0 --for=jsonpath='{.status.v1beta2.readyReplicas}'=2
  # ingress / load balancer
  kubectl -n tenant-test wait --timeout=5m hr kubernetes-$name-monitoring-agents --for=condition=ready
  kubectl -n tenant-test wait --timeout=5m hr kubernetes-$name-ingress-nginx --for=condition=ready
  kubectl -n tenant-test get secret kubernetes-$name-admin-kubeconfig -o go-template='{{ printf "%s\n" (index .data "admin.conf" | base64decode) }}' > admin.conf
  KUBECONFIG=admin.conf kubectl -n cozy-ingress-nginx wait --timeout=3m deploy ingress-nginx-defaultbackend --for=jsonpath='{.status.conditions[0].status}'=True
  KUBECONFIG=admin.conf kubectl -n cozy-monitoring wait --timeout=3m deploy cozy-monitoring-agents-metrics-server --for=jsonpath='{.status.conditions[0].status}'=True
}

@test "Create a PVC in tenant Kubernetes" {
  name='test'
  KUBECONFIG=admin.conf kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-$name
  namespace: cozy-monitoring
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
EOF
  sleep 10
  KUBECONFIG=admin.conf kubectl -n cozy-monitoring wait --timeout=20s pvc pvc-$name --for=jsonpath='{.status.phase}'=Bound
  KUBECONFIG=admin.conf kubectl -n cozy-monitoring delete pvc pvc-$name
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io $name
}