#!/bin/bash

# Debug functions
debug_log() {
    echo "[DEBUG] $(date '+%Y-%m-%d %H:%M:%S') - $1"
}

check_provider_status() {
    debug_log "Checking Cluster API provider status..."
    kubectl get pods -n capi-system
    kubectl get pods -n capi-kubeadm-bootstrap-system
    kubectl get pods -n capi-kubeadm-control-plane-system
    kubectl get pods -n capi-proxmox-system
}

check_cluster_status() {
    debug_log "Checking cluster status..."
    kubectl get clusters -A
    kubectl get machines -A
    kubectl get proxmoxclusters -A
    kubectl get proxmoxmachines -A
}

check_provider_logs() {
    debug_log "Checking provider logs..."
    for namespace in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capi-proxmox-system; do
        echo "=== Logs from $namespace ==="
        kubectl logs -n $namespace -l control-plane=controller-manager --tail=100
    done
}

check_machine_logs() {
    debug_log "Checking machine logs..."
    kubectl get machines -A -o wide
    for machine in $(kubectl get machines -A -o jsonpath='{.items[*].metadata.name}'); do
        echo "=== Logs for machine $machine ==="
        kubectl logs -n default -l cluster.x-k8s.io/machine-name=$machine --tail=100
    done
}

# Check if required environment variables are set
required_vars=(
  "PROXMOX_USERNAME"
  "PROXMOX_PASSWORD"
  "PROXMOX_URL"
  "PROXMOX_SERVER"
  "PROXMOX_NODE"
  "VM_TEMPLATE"
  "KUBERNETES_VERSION"
  "LOAD_BALANCER_HOST"
)

for var in "${required_vars[@]}"; do
  if [ -z "${!var}" ]; then
    echo "Error: Required environment variable $var is not set"
    exit 1
  fi
done

# Create a temporary directory for processed manifests
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

# Process the manifests with environment variables
debug_log "Processing manifests..."
envsubst < templates/proxmox-examples.yaml > "$TEMP_DIR/processed-manifests.yaml"

# Apply the manifests
debug_log "Applying Cluster API manifests..."
kubectl apply -f "$TEMP_DIR/processed-manifests.yaml"

# Initial status check
debug_log "Performing initial status check..."
check_provider_status
check_cluster_status

echo "Waiting for cluster to be ready..."
kubectl wait --for=condition=ready cluster/proxmox-cluster --timeout=300s || {
    debug_log "Cluster failed to become ready. Checking logs..."
    check_provider_logs
    check_machine_logs
    check_cluster_status
    exit 1
}

debug_log "Cluster is ready. Final status check..."
check_provider_status
check_cluster_status

echo "Cluster creation completed. You can monitor the progress with:"
echo "kubectl get clusters"
echo "kubectl get machines"
echo "kubectl get proxmoxclusters"
echo "kubectl get proxmoxmachines"

# Add debug commands
echo -e "\nDebug commands:"
echo "1. Check provider logs:"
echo "   kubectl logs -n capi-proxmox-system -l control-plane=controller-manager"
echo "2. Check machine status:"
echo "   kubectl get machines -A -o wide"
echo "3. Check cluster status:"
echo "   kubectl get clusters -A"
echo "4. Check Proxmox provider status:"
echo "   kubectl get proxmoxclusters -A"
echo "5. Check Proxmox machines:"
echo "   kubectl get proxmoxmachines -A" 