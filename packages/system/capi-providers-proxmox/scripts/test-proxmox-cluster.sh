#!/bin/bash

# Test script for Proxmox Cluster API integration

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Function to print colored output
print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check prerequisites
check_prerequisites() {
    print_status "Checking prerequisites..."
    
    # Check if kubectl is installed
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    # Check if kubectl can connect to cluster
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
        exit 1
    fi
    
    print_status "Prerequisites check passed."
}

# Check Cluster API Operator
check_capi_operator() {
    print_status "Checking Cluster API Operator..."
    
    if kubectl get pods -n cozy-cluster-api | grep -q "cluster-api-operator"; then
        print_status "Cluster API Operator is running."
    else
        print_error "Cluster API Operator is not running. Please install it first."
        exit 1
    fi
}

# Check Infrastructure Provider
check_infrastructure_provider() {
    print_status "Checking Infrastructure Provider..."
    
    if kubectl get infrastructureproviders | grep -q "proxmox"; then
        print_status "Proxmox Infrastructure Provider is installed."
    else
        print_error "Proxmox Infrastructure Provider is not installed."
        exit 1
    fi
}

# Test cluster creation
test_cluster_creation() {
    print_status "Testing cluster creation..."
    
    # Apply example cluster configuration
    if kubectl apply -f examples/proxmox-cluster.yaml; then
        print_status "Cluster configuration applied successfully."
    else
        print_error "Failed to apply cluster configuration."
        exit 1
    fi
    
    # Wait for cluster to be ready
    print_status "Waiting for cluster to be ready..."
    if kubectl wait --for=condition=ready cluster/proxmox-cluster --timeout=300s; then
        print_status "Cluster is ready."
    else
        print_warning "Cluster did not become ready within timeout."
    fi
}

# Check cluster status
check_cluster_status() {
    print_status "Checking cluster status..."
    
    echo "=== Clusters ==="
    kubectl get clusters
    
    echo -e "\n=== Proxmox Clusters ==="
    kubectl get proxmoxclusters
    
    echo -e "\n=== Machines ==="
    kubectl get machines
    
    echo -e "\n=== Proxmox Machines ==="
    kubectl get proxmoxmachines
}

# Cleanup
cleanup() {
    print_status "Cleaning up test resources..."
    
    kubectl delete -f examples/proxmox-cluster.yaml --ignore-not-found=true
    
    print_status "Cleanup completed."
}

# Main execution
main() {
    print_status "Starting Proxmox Cluster API integration test..."
    
    check_prerequisites
    check_capi_operator
    check_infrastructure_provider
    
    if [[ "${1:-}" == "--test-cluster" ]]; then
        test_cluster_creation
        check_cluster_status
        
        read -p "Press Enter to cleanup test resources..."
        cleanup
    else
        print_status "Basic checks completed. Use --test-cluster to test cluster creation."
    fi
    
    print_status "Proxmox Cluster API integration test completed!"
}

# Run main function
main "$@"
