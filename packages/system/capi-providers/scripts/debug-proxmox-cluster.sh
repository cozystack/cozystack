#!/bin/bash

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Debug functions
debug_log() {
    echo -e "${YELLOW}[DEBUG] $(date '+%Y-%m-%d %H:%M:%S') - $1${NC}"
}

error_log() {
    echo -e "${RED}[ERROR] $(date '+%Y-%m-%d %H:%M:%S') - $1${NC}"
}

success_log() {
    echo -e "${GREEN}[SUCCESS] $(date '+%Y-%m-%d %H:%M:%S') - $1${NC}"
}

# Check Cluster API providers
check_providers() {
    debug_log "Checking Cluster API providers..."
    echo "=== Core Provider ==="
    kubectl get pods -n capi-system -l control-plane=controller-manager
    
    echo -e "\n=== Bootstrap Provider ==="
    kubectl get pods -n capi-kubeadm-bootstrap-system -l control-plane=controller-manager
    
    echo -e "\n=== Control Plane Provider ==="
    kubectl get pods -n capi-kubeadm-control-plane-system -l control-plane=controller-manager
    
    echo -e "\n=== Proxmox Provider ==="
    kubectl get pods -n capi-proxmox-system -l control-plane=controller-manager
}

# Check cluster resources
check_resources() {
    debug_log "Checking cluster resources..."
    echo "=== Clusters ==="
    kubectl get clusters -A -o wide
    
    echo -e "\n=== Machines ==="
    kubectl get machines -A -o wide
    
    echo -e "\n=== Proxmox Clusters ==="
    kubectl get proxmoxclusters -A -o wide
    
    echo -e "\n=== Proxmox Machines ==="
    kubectl get proxmoxmachines -A -o wide
}

# Check provider logs
check_provider_logs() {
    debug_log "Checking provider logs..."
    for namespace in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capi-proxmox-system; do
        echo "=== Logs from $namespace ==="
        kubectl logs -n $namespace -l control-plane=controller-manager --tail=100
    done
}

# Check machine logs
check_machine_logs() {
    debug_log "Checking machine logs..."
    for machine in $(kubectl get machines -A -o jsonpath='{.items[*].metadata.name}'); do
        echo "=== Logs for machine $machine ==="
        kubectl logs -n default -l cluster.x-k8s.io/machine-name=$machine --tail=100
    done
}

# Check Proxmox connection
check_proxmox_connection() {
    debug_log "Checking Proxmox connection..."
    kubectl get secret proxmox-credentials -o jsonpath='{.data.url}' | base64 -d
    echo -e "\nChecking Proxmox provider pods..."
    kubectl get pods -n capi-proxmox-system -o wide
}

# Check events
check_events() {
    debug_log "Checking events..."
    echo "=== Cluster Events ==="
    kubectl get events --field-selector involvedObject.kind=Cluster
    
    echo -e "\n=== Machine Events ==="
    kubectl get events --field-selector involvedObject.kind=Machine
    
    echo -e "\n=== ProxmoxCluster Events ==="
    kubectl get events --field-selector involvedObject.kind=ProxmoxCluster
    
    echo -e "\n=== ProxmoxMachine Events ==="
    kubectl get events --field-selector involvedObject.kind=ProxmoxMachine
}

# Main menu
while true; do
    echo -e "\n${YELLOW}Proxmox Cluster API Debug Menu${NC}"
    echo "1. Check Cluster API providers"
    echo "2. Check cluster resources"
    echo "3. Check provider logs"
    echo "4. Check machine logs"
    echo "5. Check Proxmox connection"
    echo "6. Check events"
    echo "7. Run all checks"
    echo "8. Exit"
    
    read -p "Select an option (1-8): " option
    
    case $option in
        1) check_providers ;;
        2) check_resources ;;
        3) check_provider_logs ;;
        4) check_machine_logs ;;
        5) check_proxmox_connection ;;
        6) check_events ;;
        7)
            check_providers
            check_resources
            check_provider_logs
            check_machine_logs
            check_proxmox_connection
            check_events
            ;;
        8) exit 0 ;;
        *) echo "Invalid option" ;;
    esac
done 