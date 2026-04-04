#!/bin/bash

# Proxmox Integration Tests Runner
# This script runs all Proxmox integration tests in sequence

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${TEST_DIR}/logs"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
LOG_FILE="${LOG_DIR}/test_run_${TIMESTAMP}.log"

# Test steps
STEPS=(
    "step1-api-connection"
    "step2-network-storage" 
    "step3-vm-management"
    "step4-worker-integration"
    "step5-csi-storage"
    "step6-network-policies"
    "step7-monitoring"
    "step8-e2e"
)

# Function to print colored output
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1" | tee -a "$LOG_FILE"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" | tee -a "$LOG_FILE"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" | tee -a "$LOG_FILE"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "$LOG_FILE"
}

# Function to show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    -s, --step STEP           Run specific step only (1-8)
    -f, --fail-fast           Stop on first failure
    -v, --verbose             Verbose output
    -c, --config CONFIG       Configuration file
    -o, --output-dir DIR      Output directory for logs
    -h, --help               Show this help message

Environment Variables:
    PROXMOX_HOST             Proxmox server hostname
    PROXMOX_USERNAME         Proxmox username
    PROXMOX_PASSWORD         Proxmox password
    PROXMOX_NODE             Proxmox node name
    K8S_ENDPOINT             Kubernetes API endpoint
    K8S_JOIN_TOKEN           Kubernetes join token
    K8S_CA_CERT_HASH         Kubernetes CA certificate hash

Examples:
    $0                       # Run all tests
    $0 -s 1                  # Run only step 1 tests
    $0 -f -v                 # Run all tests with fail-fast and verbose
    $0 -c config.env         # Run with custom configuration

EOF
}

# Function to check prerequisites
check_prerequisites() {
    print_info "Checking prerequisites..."
    
    # Check required tools
    local tools=("python3" "pytest" "kubectl" "helm")
    for tool in "${tools[@]}"; do
        if ! command -v "$tool" &> /dev/null; then
            print_error "$tool is not installed or not in PATH"
            return 1
        fi
    done
    
    # Check Python dependencies
    if ! python3 -c "import pytest, kubernetes, requests, yaml" &> /dev/null; then
        print_warning "Installing Python dependencies..."
        pip3 install pytest kubernetes requests pyyaml urllib3
    fi
    
    # Check Kubernetes access
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot access Kubernetes cluster"
        return 1
    fi
    
    print_success "Prerequisites check passed"
    return 0
}

# Function to load configuration
load_config() {
    local config_file="$1"
    
    if [ -n "$config_file" ] && [ -f "$config_file" ]; then
        print_info "Loading configuration from $config_file"
        source "$config_file"
    fi
    
    # Set default values if not provided
    export PROXMOX_HOST="${PROXMOX_HOST:-proxmox.example.com}"
    export PROXMOX_USERNAME="${PROXMOX_USERNAME:-root@pam}"
    export PROXMOX_PASSWORD="${PROXMOX_PASSWORD:-}"
    export PROXMOX_NODE="${PROXMOX_NODE:-proxmox-node1}"
    export PROXMOX_PORT="${PROXMOX_PORT:-8006}"
    export K8S_ENDPOINT="${K8S_ENDPOINT:-k8s-api.example.com:6443}"
    export TEST_NAMESPACE="${TEST_NAMESPACE:-proxmox-test}"
    
    print_info "Configuration loaded:"
    print_info "  Proxmox Host: $PROXMOX_HOST"
    print_info "  Proxmox Node: $PROXMOX_NODE"
    print_info "  K8s Endpoint: $K8S_ENDPOINT"
    print_info "  Test Namespace: $TEST_NAMESPACE"
}

# Function to run a specific test step
run_test_step() {
    local step="$1"
    local step_dir="${TEST_DIR}/${step}"
    local step_log="${LOG_DIR}/${step}_${TIMESTAMP}.log"
    
    if [ ! -d "$step_dir" ]; then
        print_warning "Step directory not found: $step_dir"
        return 1
    fi
    
    print_info "Running test step: $step"
    
    # Find test files in step directory
    local test_files=$(find "$step_dir" -name "test_*.py" -type f)
    
    if [ -z "$test_files" ]; then
        print_warning "No test files found in $step_dir"
        return 1
    fi
    
    # Run pytest for this step
    local pytest_args=("-v" "--tb=short" "--color=yes")
    
    if [ "$VERBOSE" = true ]; then
        pytest_args+=("-s")
    fi
    
    # Add step directory to Python path
    export PYTHONPATH="${step_dir}:${TEST_DIR}:${PYTHONPATH:-}"
    
    if python3 -m pytest "${pytest_args[@]}" "$step_dir" 2>&1 | tee "$step_log"; then
        print_success "Step $step completed successfully"
        return 0
    else
        print_error "Step $step failed"
        return 1
    fi
}

# Function to generate test report
generate_report() {
    local report_file="${LOG_DIR}/test_report_${TIMESTAMP}.md"
    
    print_info "Generating test report: $report_file"
    
    cat > "$report_file" << EOF
# Proxmox Integration Test Report

**Test Run:** $(date)
**Environment:** 
- Proxmox Host: $PROXMOX_HOST
- Proxmox Node: $PROXMOX_NODE
- Kubernetes Endpoint: $K8S_ENDPOINT

## Test Results

EOF
    
    # Add results for each step
    for step in "${STEPS[@]}"; do
        local step_log="${LOG_DIR}/${step}_${TIMESTAMP}.log"
        
        echo "### $step" >> "$report_file"
        
        if [ -f "$step_log" ]; then
            if grep -q "failed" "$step_log"; then
                echo "❌ **FAILED**" >> "$report_file"
            elif grep -q "passed" "$step_log"; then
                echo "✅ **PASSED**" >> "$report_file"
            else
                echo "⚠️ **SKIPPED**" >> "$report_file"
            fi
            
            # Add summary
            local passed=$(grep -c "PASSED" "$step_log" 2>/dev/null || echo "0")
            local failed=$(grep -c "FAILED" "$step_log" 2>/dev/null || echo "0")
            local skipped=$(grep -c "SKIPPED" "$step_log" 2>/dev/null || echo "0")
            
            echo "- Passed: $passed" >> "$report_file"
            echo "- Failed: $failed" >> "$report_file"
            echo "- Skipped: $skipped" >> "$report_file"
        else
            echo "❓ **NOT RUN**" >> "$report_file"
        fi
        
        echo "" >> "$report_file"
    done
    
    print_success "Test report generated: $report_file"
}

# Main function
main() {
    local specific_step=""
    local fail_fast=false
    local config_file=""
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -s|--step)
                specific_step="$2"
                shift 2
                ;;
            -f|--fail-fast)
                fail_fast=true
                shift
                ;;
            -v|--verbose)
                VERBOSE=true
                shift
                ;;
            -c|--config)
                config_file="$2"
                shift 2
                ;;
            -o|--output-dir)
                LOG_DIR="$2"
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
    
    # Create log directory
    mkdir -p "$LOG_DIR"
    
    print_info "Starting Proxmox integration tests"
    print_info "Log directory: $LOG_DIR"
    print_info "Main log file: $LOG_FILE"
    
    # Load configuration
    load_config "$config_file"
    
    # Check prerequisites
    if ! check_prerequisites; then
        print_error "Prerequisites check failed"
        exit 1
    fi
    
    # Run specific step or all steps
    local failed_steps=()
    local success_count=0
    local total_count=0
    
    if [ -n "$specific_step" ]; then
        # Run specific step
        local step_name="step${specific_step}-"
        local found_step=""
        
        for step in "${STEPS[@]}"; do
            if [[ "$step" == "$step_name"* ]]; then
                found_step="$step"
                break
            fi
        done
        
        if [ -z "$found_step" ]; then
            print_error "Invalid step number: $specific_step"
            exit 1
        fi
        
        total_count=1
        if run_test_step "$found_step"; then
            success_count=1
        else
            failed_steps+=("$found_step")
        fi
    else
        # Run all steps
        total_count=${#STEPS[@]}
        
        for step in "${STEPS[@]}"; do
            if run_test_step "$step"; then
                ((success_count++))
            else
                failed_steps+=("$step")
                
                if [ "$fail_fast" = true ]; then
                    print_error "Stopping due to failure in $step (fail-fast mode)"
                    break
                fi
            fi
        done
    fi
    
    # Generate report
    generate_report
    
    # Print summary
    echo ""
    print_info "========================================="
    print_info "Test Summary"
    print_info "========================================="
    print_info "Total steps: $total_count"
    print_success "Successful: $success_count"
    
    if [ ${#failed_steps[@]} -gt 0 ]; then
        print_error "Failed: ${#failed_steps[@]}"
        print_error "Failed steps: ${failed_steps[*]}"
        exit 1
    else
        print_success "All tests completed successfully!"
        exit 0
    fi
}

# Run main function
main "$@"
