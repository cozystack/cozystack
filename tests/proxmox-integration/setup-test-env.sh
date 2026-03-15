#!/bin/bash

# Proxmox Integration Test Environment Setup
# This script sets up the test environment for Proxmox integration tests

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Function to show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    -p, --python-only         Install only Python dependencies
    -k, --kubectl-only        Install only kubectl
    -h, --helm-only           Install only Helm
    -c, --config-only         Create configuration files only
    --all                     Install everything (default)
    --help                    Show this help message

Examples:
    $0                        # Setup everything
    $0 -p                     # Install only Python dependencies
    $0 -c                     # Create config files only

EOF
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to install Python dependencies
install_python_deps() {
    print_info "Installing Python dependencies..."
    
    if ! command_exists python3; then
        print_error "Python 3 is not installed"
        return 1
    fi
    
    if ! command_exists pip3; then
        print_error "pip3 is not installed"
        return 1
    fi
    
    # Upgrade pip
    python3 -m pip install --upgrade pip
    
    # Install dependencies
    pip3 install -r "${SCRIPT_DIR}/requirements.txt"
    
    print_success "Python dependencies installed"
}

# Function to install kubectl
install_kubectl() {
    print_info "Installing kubectl..."
    
    if command_exists kubectl; then
        print_info "kubectl already installed: $(kubectl version --client --short 2>/dev/null || echo 'unknown version')"
        return 0
    fi
    
    # Detect OS
    local os="linux"
    local arch="amd64"
    
    if [[ "$OSTYPE" == "darwin"* ]]; then
        os="darwin"
    fi
    
    if [[ "$(uname -m)" == "arm64" ]] || [[ "$(uname -m)" == "aarch64" ]]; then
        arch="arm64"
    fi
    
    # Download and install kubectl
    local kubectl_version
    kubectl_version=$(curl -L -s https://dl.k8s.io/release/stable.txt)
    
    curl -LO "https://dl.k8s.io/release/${kubectl_version}/bin/${os}/${arch}/kubectl"
    chmod +x kubectl
    sudo mv kubectl /usr/local/bin/
    
    print_success "kubectl installed: $(kubectl version --client --short)"
}

# Function to install Helm
install_helm() {
    print_info "Installing Helm..."
    
    if command_exists helm; then
        print_info "Helm already installed: $(helm version --short 2>/dev/null || echo 'unknown version')"
        return 0
    fi
    
    # Install Helm using the official installer
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
    
    print_success "Helm installed: $(helm version --short)"
}

# Function to create configuration files
create_config_files() {
    print_info "Creating configuration files..."
    
    # Create config.env if it doesn't exist
    local config_file="${SCRIPT_DIR}/config.env"
    if [ ! -f "$config_file" ]; then
        cp "${SCRIPT_DIR}/config.example.env" "$config_file"
        print_warning "Created $config_file - please edit it with your values"
    else
        print_info "Configuration file already exists: $config_file"
    fi
    
    # Create pytest.ini
    local pytest_config="${SCRIPT_DIR}/pytest.ini"
    if [ ! -f "$pytest_config" ]; then
        cat > "$pytest_config" << EOF
[tool:pytest]
testpaths = .
python_files = test_*.py
python_classes = Test*
python_functions = test_*
addopts = 
    -v
    --tb=short
    --strict-markers
    --disable-warnings
    --color=yes
markers =
    slow: marks tests as slow (deselect with '-m "not slow"')
    integration: marks tests as integration tests
    api: marks tests as API tests
    network: marks tests as network tests
    storage: marks tests as storage tests
    worker: marks tests as worker integration tests
    e2e: marks tests as end-to-end tests
timeout = 300
EOF
        print_success "Created pytest configuration: $pytest_config"
    fi
    
    # Create .gitignore
    local gitignore="${SCRIPT_DIR}/.gitignore"
    if [ ! -f "$gitignore" ]; then
        cat > "$gitignore" << EOF
# Test configuration
config.env

# Test logs and reports
logs/
*.log
test_report_*.md
.pytest_cache/
__pycache__/
*.pyc
*.pyo

# Coverage reports
htmlcov/
.coverage
coverage.xml

# IDE files
.vscode/
.idea/
*.swp
*.swo

# Temporary files
*.tmp
*.temp
temp_*.yaml
EOF
        print_success "Created .gitignore: $gitignore"
    fi
    
    # Create logs directory
    mkdir -p "${SCRIPT_DIR}/logs"
    print_success "Created logs directory"
}

# Function to verify installation
verify_installation() {
    print_info "Verifying installation..."
    
    local all_good=true
    
    # Check Python and dependencies
    if python3 -c "import pytest, kubernetes, requests, yaml" 2>/dev/null; then
        print_success "✓ Python dependencies"
    else
        print_error "✗ Python dependencies"
        all_good=false
    fi
    
    # Check kubectl
    if command_exists kubectl; then
        print_success "✓ kubectl"
    else
        print_error "✗ kubectl"
        all_good=false
    fi
    
    # Check Helm
    if command_exists helm; then
        print_success "✓ Helm"
    else
        print_error "✗ Helm"
        all_good=false
    fi
    
    # Check Kubernetes access (optional)
    if kubectl cluster-info &>/dev/null; then
        print_success "✓ Kubernetes cluster accessible"
    else
        print_warning "⚠ Kubernetes cluster not accessible (configure kubectl first)"
    fi
    
    if [ "$all_good" = true ]; then
        print_success "Environment setup completed successfully!"
        print_info ""
        print_info "Next steps:"
        print_info "1. Edit ${SCRIPT_DIR}/config.env with your Proxmox and Kubernetes details"
        print_info "2. Configure kubectl to access your Kubernetes cluster"
        print_info "3. Run tests with: ${SCRIPT_DIR}/run-all-tests.sh"
    else
        print_error "Some components failed to install"
        return 1
    fi
}

# Main function
main() {
    local install_python=false
    local install_kubectl=false
    local install_helm=false
    local create_config=false
    local install_all=true
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -p|--python-only)
                install_python=true
                install_all=false
                shift
                ;;
            -k|--kubectl-only)
                install_kubectl=true
                install_all=false
                shift
                ;;
            -h|--helm-only)
                install_helm=true
                install_all=false
                shift
                ;;
            -c|--config-only)
                create_config=true
                install_all=false
                shift
                ;;
            --all)
                install_all=true
                shift
                ;;
            --help)
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
    
    print_info "Setting up Proxmox integration test environment..."
    
    # Install components based on options
    if [ "$install_all" = true ] || [ "$install_python" = true ]; then
        install_python_deps
    fi
    
    if [ "$install_all" = true ] || [ "$install_kubectl" = true ]; then
        install_kubectl
    fi
    
    if [ "$install_all" = true ] || [ "$install_helm" = true ]; then
        install_helm
    fi
    
    if [ "$install_all" = true ] || [ "$create_config" = true ]; then
        create_config_files
    fi
    
    # Verify installation
    verify_installation
}

# Run main function
main "$@"
