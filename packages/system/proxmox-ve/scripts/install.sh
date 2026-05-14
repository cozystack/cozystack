#!/bin/bash

# Proxmox VE Helm Chart Installation Script

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Default values
NAMESPACE="proxmox-ve"
RELEASE_NAME="proxmox-ve"
VALUES_FILE=""
DRY_RUN=false
WAIT=false

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

# Function to show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    -n, --namespace NAME     Kubernetes namespace (default: proxmox-ve)
    -r, --release NAME       Helm release name (default: proxmox-ve)
    -f, --values FILE        Values file path
    -d, --dry-run           Dry run mode
    -w, --wait              Wait for deployment to be ready
    -h, --help              Show this help message

Examples:
    $0 -f my-values.yaml
    $0 -n my-namespace -r my-release -f values.yaml
    $0 --dry-run --values example-values.yaml
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--namespace)
            NAMESPACE="$2"
            shift 2
            ;;
        -r|--release)
            RELEASE_NAME="$2"
            shift 2
            ;;
        -f|--values)
            VALUES_FILE="$2"
            shift 2
            ;;
        -d|--dry-run)
            DRY_RUN=true
            shift
            ;;
        -w|--wait)
            WAIT=true
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Check prerequisites
check_prerequisites() {
    print_status "Checking prerequisites..."
    
    # Check if helm is installed
    if ! command -v helm &> /dev/null; then
        print_error "Helm is not installed. Please install Helm first."
        exit 1
    fi
    
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

# Create namespace
create_namespace() {
    print_status "Creating namespace: $NAMESPACE"
    
    if kubectl get namespace "$NAMESPACE" &> /dev/null; then
        print_warning "Namespace $NAMESPACE already exists."
    else
        kubectl create namespace "$NAMESPACE"
        print_status "Namespace $NAMESPACE created."
    fi
}

# Install Helm chart
install_chart() {
    print_status "Installing Helm chart..."
    
    # Build helm command
    HELM_CMD="helm install $RELEASE_NAME . --namespace $NAMESPACE"
    
    # Add values file if specified
    if [[ -n "$VALUES_FILE" ]]; then
        if [[ ! -f "$VALUES_FILE" ]]; then
            print_error "Values file not found: $VALUES_FILE"
            exit 1
        fi
        HELM_CMD="$HELM_CMD --values $VALUES_FILE"
    fi
    
    # Add dry-run flag if specified
    if [[ "$DRY_RUN" == true ]]; then
        HELM_CMD="$HELM_CMD --dry-run"
    fi
    
    # Add wait flag if specified
    if [[ "$WAIT" == true ]]; then
        HELM_CMD="$HELM_CMD --wait"
    fi
    
    print_status "Running: $HELM_CMD"
    
    # Execute helm command
    if eval "$HELM_CMD"; then
        print_status "Helm chart installed successfully."
    else
        print_error "Failed to install Helm chart."
        exit 1
    fi
}

# Verify installation
verify_installation() {
    if [[ "$DRY_RUN" == true ]]; then
        print_status "Dry run completed. No resources were created."
        return
    fi
    
    print_status "Verifying installation..."
    
    # Check if release exists
    if helm list --namespace "$NAMESPACE" | grep -q "$RELEASE_NAME"; then
        print_status "Release $RELEASE_NAME found in namespace $NAMESPACE."
    else
        print_error "Release $RELEASE_NAME not found in namespace $NAMESPACE."
        exit 1
    fi
    
    # Check pod status
    print_status "Checking pod status..."
    kubectl get pods --namespace "$NAMESPACE" -l app.kubernetes.io/instance="$RELEASE_NAME"
    
    print_status "Installation verification completed."
}

# Main execution
main() {
    print_status "Starting Proxmox VE installation..."
    
    check_prerequisites
    create_namespace
    install_chart
    verify_installation
    
    print_status "Proxmox VE installation completed successfully!"
    
    if [[ "$DRY_RUN" == false ]]; then
        print_status "You can now:"
        print_status "1. Check the status: kubectl get pods --namespace $NAMESPACE"
        print_status "2. View logs: kubectl logs --namespace $NAMESPACE -l app.kubernetes.io/instance=$RELEASE_NAME"
        print_status "3. Uninstall: helm uninstall $RELEASE_NAME --namespace $NAMESPACE"
    fi
}

# Run main function
main "$@"
