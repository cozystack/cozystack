run_kubernetes_test() {
    local version_expr="$1"
    local test_name="$2"
    local port="$3"
    local k8s_version=$(yq "$version_expr" packages/apps/kubernetes/files/versions.yaml)

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
      minReplicas: 2
      roles:
      - ingress-nginx
  version: "${k8s_version}"
EOF
  # Wait for the tenant-test namespace to be active
  kubectl wait namespace tenant-test --timeout=20s --for=jsonpath='{.status.phase}'=Active

  # Wait for the Kamaji control plane to be created (retry for up to 10 seconds)
  timeout 10 sh -ec 'until kubectl get kamajicontrolplane -n tenant-test kubernetes-'"${test_name}"'; do sleep 1; done'

  # Wait for the tenant control plane to be fully created (timeout after 4 minutes)
  kubectl wait --for=condition=TenantControlPlaneCreated kamajicontrolplane -n tenant-test kubernetes-${test_name} --timeout=4m

  # Wait for Kubernetes resources to be ready (timeout after 2 minutes)
  kubectl wait tcp -n tenant-test kubernetes-${test_name} --timeout=5m --for=jsonpath='{.status.kubernetesResources.version.status}'=Ready

  # Wait for all required deployments to be available (timeout after 4 minutes)
  kubectl wait deploy --timeout=4m --for=condition=available -n tenant-test kubernetes-${test_name} kubernetes-${test_name}-cluster-autoscaler kubernetes-${test_name}-kccm kubernetes-${test_name}-kcsi-controller

  # Wait for the machine deployment to scale to 2 replicas (timeout after 1 minute)
  kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=1m --for=jsonpath='{.status.replicas}'=2
  # Get the admin kubeconfig and save it to a file
  kubectl get secret kubernetes-${test_name}-admin-kubeconfig -ojsonpath='{.data.super-admin\.conf}' -n tenant-test | base64 -d > "tenantkubeconfig-${test_name}"

  # Update the kubeconfig to use localhost for the API server
  yq -i ".clusters[0].cluster.server = \"https://localhost:${port}\"" "tenantkubeconfig-${test_name}"


  # Kill any stale port-forward on this port from a previous retry
  pkill -f "port-forward.*${port}:" 2>/dev/null || true
  sleep 1

  # Set up port forwarding to the Kubernetes API server
  bash -c 'timeout 500s kubectl port-forward service/kubernetes-'"${test_name}"' -n tenant-test '"${port}"':6443 > /dev/null 2>&1 &'
  # Verify the Kubernetes version matches what we expect (retry for up to 20 seconds)
  timeout 20 sh -ec 'until kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' version 2>/dev/null | grep -Fq "Server Version: ${k8s_version}"; do sleep 5; done'

  # Wait for at least 2 nodes to join (timeout after 8 minutes)
  timeout 8m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get nodes -o jsonpath="{.items[*].metadata.name}" | wc -w)" -ge 2 ]; do
      sleep 2
    done
  '
  # Verify the nodes are ready
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait node --all --timeout=2m --for=condition=Ready
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" get nodes -o wide

  # Verify the kubelet version matches what we expect
  versions=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" \
    get nodes -o jsonpath='{.items[*].status.nodeInfo.kubeletVersion}')
  
  node_ok=true
  
  for v in $versions; do
    case "$v" in
      "${k8s_version}" | "${k8s_version}".* | "${k8s_version}"-*)
        # acceptable
        ;;
      *)
        node_ok=false
        break
        ;;
    esac
  done

  if [ "$node_ok" != true ]; then
    echo "Kubelet versions did not match expected ${k8s_version}" >&2
    exit 1
  fi

  # Verify StorageClass propagation from infra to tenant cluster
  echo "Verifying StorageClass propagation..."

  # Wait for at least one StorageClass to appear in the tenant cluster
  timeout 2m bash -c '
    until [ "$(kubectl --kubeconfig tenantkubeconfig-'"${test_name}"' get sc -o jsonpath="{.items[*].metadata.name}" 2>/dev/null | wc -w)" -ge 1 ]; do
      sleep 5
    done
  '

  sc_names=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc -o jsonpath='{.items[*].metadata.name}')
  if [ -z "$sc_names" ]; then
    echo "No StorageClasses found in tenant cluster" >&2
    exit 1
  fi
  echo "StorageClasses in tenant: ${sc_names}"

  # Verify each propagated StorageClass uses the kubevirt CSI provisioner
  for sc in $sc_names; do
    provisioner=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc "$sc" -o jsonpath='{.provisioner}')
    if [ "$provisioner" != "csi.kubevirt.io" ]; then
      echo "StorageClass $sc has unexpected provisioner: $provisioner (expected csi.kubevirt.io)" >&2
      exit 1
    fi
  done
  echo "All StorageClasses use csi.kubevirt.io provisioner"

  # Verify exactly one default StorageClass is set
  default_count=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" get sc \
    -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' | grep -c .)
  if [ "$default_count" -ne 1 ]; then
    echo "Expected exactly 1 default StorageClass, found $default_count" >&2
    exit 1
  fi
  echo "Default StorageClass is correctly set"

  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: tenant-test
EOF

  # Clean up backend resources from any previous failed attempt
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Backend 1
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
      backend: "${test_name}-backend"
  template:
    metadata:
      labels:
        app: backend
        backend: "${test_name}-backend"
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
        readinessProbe:
          httpGet:
            path: /
            port: 80
          initialDelaySeconds: 2
          periodSeconds: 2
EOF

  # LoadBalancer Service
  kubectl apply --kubeconfig "tenantkubeconfig-${test_name}" -f- <<EOF
apiVersion: v1
kind: Service
metadata:
  name: "${test_name}-backend"
  namespace: tenant-test
spec:
  type: LoadBalancer
  selector:
    app: backend
    backend: "${test_name}-backend"
  ports:
  - port: 80
    targetPort: 80
EOF

  # Wait for pods readiness
  kubectl wait deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test --for=condition=Available --timeout=300s
  
  # Wait for LoadBalancer to be provisioned (IP or hostname)
  timeout 90 sh -ec "
    until kubectl get svc ${test_name}-backend --kubeconfig tenantkubeconfig-${test_name} -n tenant-test \
      -o jsonpath='{.status.loadBalancer.ingress[0]}' | grep -q .; do
      sleep 5
    done
  "

LB_ADDR=$(
  kubectl get svc --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" \
    -n tenant-test \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}'
)

if [ -z "$LB_ADDR" ]; then
  echo "LoadBalancer address is empty" >&2
  exit 1
fi

  for i in $(seq 1 20); do
    echo "Attempt $i"
    curl --silent --fail "http://${LB_ADDR}" && break
    sleep 3
  done

  if [ "$i" -eq 20 ]; then
    echo "LoadBalancer not reachable" >&2
    exit 1
  fi

  # Cleanup
  kubectl delete deployment --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test
  kubectl delete service --kubeconfig "tenantkubeconfig-${test_name}" "${test_name}-backend" -n tenant-test

  # Clean up NFS test resources from any previous failed attempt
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod \
    -n tenant-test --ignore-not-found --timeout=60s || true
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc \
    -n tenant-test --ignore-not-found --timeout=60s || true

  # Test RWX NFS mount in tenant cluster (uses kubevirt CSI driver with RWX support)
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-test-pvc
  namespace: tenant-test
spec:
  accessModes:
  - ReadWriteMany
  storageClassName: replicated
  resources:
    requests:
      storage: 1Gi
EOF

  # Wait for PVC to be bound
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pvc nfs-test-pvc -n tenant-test --timeout=2m --for=jsonpath='{.status.phase}'=Bound

  # Create Pod that writes and reads data from NFS volume
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: nfs-test-pod
  namespace: tenant-test
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'nfs-mount-ok' > /data/test.txt && cat /data/test.txt"]
    volumeMounts:
    - name: nfs-vol
      mountPath: /data
  volumes:
  - name: nfs-vol
    persistentVolumeClaim:
      claimName: nfs-test-pvc
  restartPolicy: Never
EOF

  # Wait for Pod to complete successfully
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" wait pod nfs-test-pod -n tenant-test --timeout=5m --for=jsonpath='{.status.phase}'=Succeeded

  # Verify NFS data integrity
  nfs_result=$(kubectl --kubeconfig "tenantkubeconfig-${test_name}" logs nfs-test-pod -n tenant-test)
  if [ "$nfs_result" != "nfs-mount-ok" ]; then
    echo "NFS mount test failed: expected 'nfs-mount-ok', got '$nfs_result'" >&2
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait=false 2>/dev/null || true
    kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test --wait=false 2>/dev/null || true
    exit 1
  fi

  # Cleanup NFS test resources in tenant cluster
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pod nfs-test-pod -n tenant-test --wait
  kubectl --kubeconfig "tenantkubeconfig-${test_name}" delete pvc nfs-test-pvc -n tenant-test

  # Wait for all machine deployment replicas to be ready (timeout after 10 minutes)
  kubectl wait machinedeployment kubernetes-${test_name}-md0 -n tenant-test --timeout=10m --for=jsonpath='{.status.v1beta2.readyReplicas}'=2

  for component in cilium coredns csi vsnap-crd; do
      kubectl wait hr kubernetes-${test_name}-${component} -n tenant-test --timeout=1m --for=condition=ready
    done
    kubectl wait hr kubernetes-${test_name}-ingress-nginx -n tenant-test --timeout=5m --for=condition=ready

  # Clean up by deleting the Kubernetes resource
  kubectl -n tenant-test delete kuberneteses.apps.cozystack.io $test_name

}
