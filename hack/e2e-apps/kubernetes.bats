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
  kubectl wait --timeout=20s namespace tenant-test --for=jsonpath='{.status.phase}'=Active
  kubectl -n tenant-test wait --timeout=10s kamajicontrolplane kubernetes-$name --for=jsonpath='{.status.conditions[0].status}'=True
  kubectl -n tenant-test wait --timeout=4m kamajicontrolplane kubernetes-$name --for=condition=TenantControlPlaneCreated
  kubectl -n tenant-test wait --timeout=210s tcp kubernetes-$name --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready
  kubectl -n tenant-test wait --timeout=4m deploy kubernetes-$name kubernetes-$name-cluster-autoscaler kubernetes-$name-kccm kubernetes-$name-kcsi-controller --for=condition=available
  kubectl -n tenant-test wait --timeout=1m machinedeployment kubernetes-$name-md0 --for=jsonpath='{.status.replicas}'=2
  kubectl -n tenant-test wait --timeout=10m machinedeployment kubernetes-$name-md0 --for=jsonpath='{.status.v1beta2.readyReplicas}'=2
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io $name
}
