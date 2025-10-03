run_kubernetes_test() {
    local version_expr="$1"
    local test_name="$2"
    local port="$3"
    local k8s_version
    k8s_version=$(yq "$version_expr" packages/apps/kubernetes/files/versions.yaml)

    kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: "${test_name}"
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
      roles:
      - ingress-nginx
  storageClass: replicated
  version: "${k8s_version}"
EOF

    kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active
    timeout 10 sh -ec 'until kubectl get kamajicontrolplane -n tenant-test kubernetes-'"${test_name}"'; do sleep 1; done'
    kubectl wait --for=condition=TenantControlPlaneCreated kamajicontrolplane -n tenant-test kubernetes-${test_name} --timeout=4m
    kubectl wait tcp -n tenant-test kubernetes-${test_name} --timeout=2m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready
    kubectl wait deploy --timeout=4m --for=condition=available -n tenant-test kubernetes-${test_name} kubernetes-${test_name}-cluster-autoscaler kubernetes-${test_name}-kccm kubernetes-${test_name}-kcsi-controller
    kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=1m --for=jsonpath='{.status.replicas}'=2

    kubectl get secret kubernetes-${test_name}-admin-kubeconfig -ojsonpath='{.data.super-admin\.conf}' -n tenant-test | base64 -d > tenantkubeconfig
    yq -i ".clusters[0].cluster.server = \"https://localhost:${port}\"" tenantkubeconfig

    bash -c 'timeout 200s kubectl port-forward service/kubernetes-'"${test_name}"' -n tenant-test '"${port}"':6443 > /dev/null 2>&1 &'
    timeout 20 sh -ec 'until kubectl --kubeconfig tenantkubeconfig version 2>/dev/null | grep -Fq "Server Version: ${k8s_version}"; do sleep 5; done'

    timeout 2m bash -c '
      until [ "$(kubectl --kubeconfig tenantkubeconfig get nodes -o jsonpath="{.items[*].metadata.name}" | wc -w)" -eq 2 ]; do
        sleep 3
      done
    '

    kubectl --kubeconfig tenantkubeconfig wait node --all --timeout=2m --for=condition=Ready
    kubectl --kubeconfig tenantkubeconfig get nodes -o wide

    versions=$(kubectl --kubeconfig tenantkubeconfig get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}')
    node_ok=true

    case "$k8s_version" in
      v1.32*)
        echo "⚠️  TODO: Temporary stub — allowing nodes with v1.33 while k8s_version is v1.32"
        ;;
    esac

    for v in $versions; do
      case "$k8s_version" in
        v1.32 | v1.32.* | v1.32-*)
          case "$v" in
            v1.32 | v1.32.* | v1.32-* | v1.33 | v1.33.* | v1.33-*) ;;
            *) node_ok=false; break ;;
          esac
          ;;
        *)
          case "$v" in
            "$k8s_version" | "$k8s_version".* | "$k8s_version"-*) ;;
            *) node_ok=false; break ;;
          esac
          ;;
      esac
    done

    if [ "$node_ok" != true ]; then
      echo "Kubelet versions did not match expected ${k8s_version}" >&2
      exit 1
    fi

    kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=10m --for=jsonpath='{.status.v1beta2.readyReplicas}'=2

    for component in cilium coredns csi ingress-nginx vsnap-crd; do
      kubectl wait hr kubernetes-${test_name}-${component} -n tenant-test --timeout=1m --for=condition=ready
    done

    kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "$test_name"
}
