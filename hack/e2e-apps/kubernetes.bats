#!/usr/bin/env bats

@test "Create a tenant Kubernetes control plane" {
  kubectl apply -f - <<EOF
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
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io test
}
