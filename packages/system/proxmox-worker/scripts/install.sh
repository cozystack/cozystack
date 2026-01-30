#!/bin/bash

# Proxmox Worker Installation Script
# This script installs and configures Proxmox server as Kubernetes worker node

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
NAMESPACE="proxmox-worker"
RELEASE_NAME="proxmox-worker"
VALUES_FILE=""
DRY_RUN=false
WAIT=false
TIMEOUT="10m"

# Function to print colored output
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
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
    -n, --namespace NAMESPACE     Kubernetes namespace (default: proxmox-worker)
    -r, --release RELEASE_NAME    Helm release name (default: proxmox-worker)
    -f, --values VALUES_FILE      Custom values file
    -d, --dry-run                 Show what would be installed without installing
    -w, --wait                    Wait for installation to complete
    -t, --timeout TIMEOUT         Timeout for installation (default: 10m)
    -h, --help                    Show this help message

Examples:
    $0                                    # Install with default values
    $0 -f custom-values.yaml             # Install with custom values
    $0 -d                                # Dry run to see what would be installed
    $0 -n my-namespace -r my-release     # Install with custom namespace and release

EOF
}

# Function to check prerequisites
check_prerequisites() {
    print_info "Checking prerequisites..."
    
    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl is not installed or not in PATH"
        exit 1
    fi
    
    # Check if helm is available
    if ! command -v helm &> /dev/null; then
        print_error "helm is not installed or not in PATH"
        exit 1
    fi
    
    # Check if cluster is accessible
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster"
        exit 1
    fi
    
    print_success "Prerequisites check passed"
}

# Function to validate values
validate_values() {
    print_info "Validating values..."
    
    if [ -n "$VALUES_FILE" ] && [ ! -f "$VALUES_FILE" ]; then
        print_error "Values file not found: $VALUES_FILE"
        exit 1
    fi
    
    print_success "Values validation passed"
}

# Function to install Proxmox worker
install_proxmox_worker() {
    print_info "Installing Proxmox worker..."
    
    local helm_args=()
    
    if [ -n "$VALUES_FILE" ]; then
        helm_args+=("--values" "$VALUES_FILE")
    fi
    
    if [ "$DRY_RUN" = true ]; then
        helm_args+=("--dry-run")
        print_info "Dry run mode - no changes will be made"
    fi
    
    if [ "$WAIT" = true ]; then
        helm_args+=("--wait" "--timeout" "$TIMEOUT")
    fi
    
    # Install the chart
    helm upgrade --install "$RELEASE_NAME" . \
        --namespace "$NAMESPACE" \
        --create-namespace \
        "${helm_args[@]}"
    
    if [ "$DRY_RUN" = false ]; then
        print_success "Proxmox worker installed successfully"
    else
        print_info "Dry run completed - no changes made"
    fi
}

# Function to verify installation
verify_installation() {
    if [ "$DRY_RUN" = true ]; then
        return 0
    fi
    
    print_info "Verifying installation..."
    
    # Wait for DaemonSet to be ready
    print_info "Waiting for DaemonSet to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=proxmox-worker -n "$NAMESPACE" --timeout="$TIMEOUT" || {
        print_warning "DaemonSet not ready within timeout"
        return 1
    }
    
    # Check if node is ready
    print_info "Checking if Proxmox node is ready..."
    local node_ready=false
    for i in {1..30}; do
        if kubectl get nodes | grep -q "Ready"; then
            node_ready=true
            break
        fi
        print_info "Waiting for node to be ready... ($i/30)"
        sleep 10
    done
    
    if [ "$node_ready" = true ]; then
        print_success "Proxmox node is ready"
    else
        print_warning "Proxmox node not ready within timeout"
    fi
    
    # Show node status
    print_info "Node status:"
    kubectl get nodes -o wide
    
    # Show pod status
    print_info "Pod status:"
    kubectl get pods -n "$NAMESPACE" -o wide
}

# Function to show post-installation information
show_post_install_info() {
    if [ "$DRY_RUN" = true ]; then
        return 0
    fi
    
    print_info "Post-installation information:"
    echo ""
    echo "Namespace: $NAMESPACE"
    echo "Release: $RELEASE_NAME"
    echo ""
    echo "To check the status:"
    echo "  kubectl get pods -n $NAMESPACE"
    echo "  kubectl get nodes"
    echo ""
    echo "To view logs:"
    echo "  kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=proxmox-worker"
    echo ""
    echo "To uninstall:"
    echo "  helm uninstall $RELEASE_NAME -n $NAMESPACE"
    echo ""
}

# Main function
main() {
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
            -t|--timeout)
                TIMEOUT="$2"
                shift 2
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
    
    print_info "Starting Proxmox worker installation..."
    print_info "Namespace: $NAMESPACE"
    print_info "Release: $RELEASE_NAME"
    print_info "Values file: ${VALUES_FILE:-"default"}"
    print_info "Dry run: $DRY_RUN"
    print_info "Wait: $WAIT"
    print_info "Timeout: $TIMEOUT"
    echo ""
    
    check_prerequisites
    validate_values
    install_proxmox_worker
    verify_installation
    show_post_install_info
    
    print_success "Proxmox worker installation completed!"
}

# Run main function
main "$@"
