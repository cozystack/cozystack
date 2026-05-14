#!/bin/bash
# System Integrity Check for Proxmox Integration
# This script performs comprehensive checks of all integration components

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Counters
TOTAL_CHECKS=0
PASSED_CHECKS=0
FAILED_CHECKS=0
WARNING_CHECKS=0

# Functions
print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

print_check() {
    echo -e "${YELLOW}⏳ Checking: $1${NC}"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
}

print_success() {
    echo -e "${GREEN}✅ PASS: $1${NC}"
    PASSED_CHECKS=$((PASSED_CHECKS + 1))
}

print_fail() {
    echo -e "${RED}❌ FAIL: $1${NC}"
    FAILED_CHECKS=$((FAILED_CHECKS + 1))
}

print_warning() {
    echo -e "${YELLOW}⚠️  WARN: $1${NC}"
    WARNING_CHECKS=$((WARNING_CHECKS + 1))
}

print_info() {
    echo -e "${BLUE}ℹ️  INFO: $1${NC}"
}

# Load configuration
if [ -f "config.env" ]; then
    source config.env
    print_info "Loaded configuration from config.env"
else
    print_warning "config.env not found, using defaults"
fi

# Set defaults
KUBECONFIG=${KUBECONFIG:-"/root/cozy/mgr-cozy/kubeconfig"}
PROXMOX_HOST=${PROXMOX_HOST:-"10.0.0.1"}
PROXMOX_PORT=${PROXMOX_PORT:-"8006"}

print_header "PROXMOX INTEGRATION SYSTEM INTEGRITY CHECK"
echo "Date: $(date)"
echo "Cluster: ${KUBECONFIG}"
echo ""

#=============================================================================
# Section 1: Kubernetes Cluster Health
#=============================================================================
print_header "1. Kubernetes Cluster Health Checks"

# 1.1 Check cluster connectivity
print_check "Kubernetes API connectivity"
if kubectl cluster-info >/dev/null 2>&1; then
    print_success "Kubernetes API is accessible"
    kubectl cluster-info | head -3
else
    print_fail "Cannot connect to Kubernetes API"
fi

# 1.2 Check all nodes are Ready
print_check "Node status"
NOT_READY=$(kubectl get nodes --no-headers | grep -v " Ready " | wc -l)
TOTAL_NODES=$(kubectl get nodes --no-headers | wc -l)
if [ "$NOT_READY" -eq 0 ]; then
    print_success "All $TOTAL_NODES nodes are Ready"
    kubectl get nodes
else
    print_fail "$NOT_READY out of $TOTAL_NODES nodes are not Ready"
    kubectl get nodes
fi

# 1.3 Check Proxmox worker node
print_check "Proxmox worker node"
if kubectl get nodes | grep -E "mgr.cp.if.ua|pve" >/dev/null 2>&1; then
    print_success "Proxmox worker node found"
    kubectl get nodes -o wide | grep -E "mgr.cp.if.ua|pve|NAME"
else
    print_warning "No Proxmox worker node found (may not be required)"
fi

#=============================================================================
# Section 2: Proxmox API Health
#=============================================================================
print_header "2. Proxmox API Health Checks"

# 2.1 Check Proxmox host reachability
print_check "Proxmox host reachability (${PROXMOX_HOST})"
if ping -c 3 -W 2 ${PROXMOX_HOST} >/dev/null 2>&1; then
    print_success "Proxmox host is reachable"
else
    print_fail "Cannot reach Proxmox host at ${PROXMOX_HOST}"
fi

# 2.2 Check Proxmox API port
print_check "Proxmox API port (${PROXMOX_PORT})"
if timeout 5 bash -c "cat < /dev/null > /dev/tcp/${PROXMOX_HOST}/${PROXMOX_PORT}" 2>/dev/null; then
    print_success "Proxmox API port ${PROXMOX_PORT} is open"
else
    print_fail "Cannot connect to Proxmox API port ${PROXMOX_PORT}"
fi

# 2.3 Check Proxmox API version
print_check "Proxmox API version"
if command -v curl >/dev/null 2>&1; then
    VERSION=$(curl -k -s https://${PROXMOX_HOST}:${PROXMOX_PORT}/api2/json/version 2>/dev/null | grep -o '"version":"[^"]*"' | cut -d'"' -f4)
    if [ -n "$VERSION" ]; then
        print_success "Proxmox VE version: $VERSION"
    else
        print_warning "Cannot retrieve version (may need authentication)"
    fi
else
    print_warning "curl not available, skipping API version check"
fi

#=============================================================================
# Section 3: Cluster API Components
#=============================================================================
print_header "3. Cluster API Components Check"

# 3.1 Check CAPI namespace
print_check "CAPI namespace existence"
if kubectl get namespace cozy-cluster-api >/dev/null 2>&1; then
    print_success "Namespace cozy-cluster-api exists"
else
    print_fail "Namespace cozy-cluster-api not found"
fi

# 3.2 Check CAPI controllers
print_check "CAPI controller pods"
CAPI_PODS=$(kubectl get pods -n cozy-cluster-api --no-headers 2>/dev/null | wc -l)
CAPI_RUNNING=$(kubectl get pods -n cozy-cluster-api --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$CAPI_RUNNING" -gt 0 ]; then
    print_success "CAPI controllers running: $CAPI_RUNNING/$CAPI_PODS pods"
    kubectl get pods -n cozy-cluster-api | grep -E "NAME|Running" | head -10
else
    print_fail "No CAPI controller pods are running"
    kubectl get pods -n cozy-cluster-api | head -10
fi

# 3.3 Check Proxmox CAPI provider
print_check "Proxmox CAPI provider (capmox)"
if kubectl get namespace capmox-system >/dev/null 2>&1; then
    CAPMOX_RUNNING=$(kubectl get pods -n capmox-system --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$CAPMOX_RUNNING" -gt 0 ]; then
        print_success "Proxmox CAPI provider running"
        kubectl get pods -n capmox-system
    else
        print_fail "Proxmox CAPI provider not running"
        kubectl get pods -n capmox-system
    fi
else
    print_fail "Namespace capmox-system not found"
fi

# 3.4 Check ProxmoxCluster resources
print_check "ProxmoxCluster resources"
PROXMOX_CLUSTERS=$(kubectl get proxmoxcluster -A --no-headers 2>/dev/null | wc -l)
if [ "$PROXMOX_CLUSTERS" -gt 0 ]; then
    READY_CLUSTERS=$(kubectl get proxmoxcluster -A --no-headers 2>/dev/null | grep -i "true" | wc -l)
    print_success "ProxmoxCluster resources: $READY_CLUSTERS/$PROXMOX_CLUSTERS Ready"
    kubectl get proxmoxcluster -A
else
    print_warning "No ProxmoxCluster resources found"
fi

# 3.5 Check Cluster API CRDs
print_check "Proxmox CRDs"
PROXMOX_CRDS=$(kubectl get crd | grep proxmox | wc -l)
if [ "$PROXMOX_CRDS" -ge 4 ]; then
    print_success "Proxmox CRDs installed: $PROXMOX_CRDS"
    kubectl get crd | grep proxmox
else
    print_fail "Missing Proxmox CRDs (found: $PROXMOX_CRDS, expected: 4+)"
fi

#=============================================================================
# Section 4: Network Infrastructure
#=============================================================================
print_header "4. Network Infrastructure Checks"

# 4.1 Check CoreDNS
print_check "CoreDNS status"
COREDNS_RUNNING=$(kubectl get pods -n kube-system -l k8s-app=kube-dns --no-headers 2>/dev/null | grep " Running " | wc -l)
COREDNS_TOTAL=$(kubectl get pods -n kube-system -l k8s-app=kube-dns --no-headers 2>/dev/null | wc -l)
if [ "$COREDNS_RUNNING" -gt 0 ]; then
    print_success "CoreDNS running: $COREDNS_RUNNING/$COREDNS_TOTAL pods"
else
    print_fail "CoreDNS not running"
fi

# 4.2 Check Cilium
print_check "Cilium CNI"
if kubectl get namespace cozy-cilium >/dev/null 2>&1; then
    CILIUM_RUNNING=$(kubectl get pods -n cozy-cilium -l app.kubernetes.io/name=cilium --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$CILIUM_RUNNING" -gt 0 ]; then
        print_success "Cilium running: $CILIUM_RUNNING pods"
    else
        print_warning "Cilium pods not running"
    fi
else
    print_warning "Cilium namespace not found"
fi

# 4.3 Check Kube-OVN
print_check "Kube-OVN"
if kubectl get namespace cozy-kubeovn >/dev/null 2>&1; then
    KUBEOVN_CTRL=$(kubectl get pods -n cozy-kubeovn -l app=kube-ovn-controller --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$KUBEOVN_CTRL" -gt 0 ]; then
        print_success "Kube-OVN controller running"
    else
        print_fail "Kube-OVN controller not running"
    fi
else
    print_warning "Kube-OVN namespace not found"
fi

# 4.4 Check MetalLB
print_check "MetalLB load balancer"
if kubectl get namespace metallb-system >/dev/null 2>&1; then
    METALLB_RUNNING=$(kubectl get pods -n metallb-system --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$METALLB_RUNNING" -gt 0 ]; then
        print_success "MetalLB running: $METALLB_RUNNING pods"
    else
        print_warning "MetalLB pods not running"
    fi
else
    print_warning "MetalLB not installed"
fi

#=============================================================================
# Section 5: Storage Infrastructure
#=============================================================================
print_header "5. Storage Infrastructure Checks"

# 5.1 Check Proxmox CSI driver
print_check "Proxmox CSI driver"
PROXMOX_CSI=$(kubectl get csidriver 2>/dev/null | grep -i proxmox | wc -l)
if [ "$PROXMOX_CSI" -gt 0 ]; then
    print_success "Proxmox CSI driver found"
    kubectl get csidriver | grep -E "NAME|proxmox"
else
    print_warning "Proxmox CSI driver not found (may not be installed)"
fi

# 5.2 Check storage classes
print_check "Proxmox storage classes"
PROXMOX_SC=$(kubectl get storageclass 2>/dev/null | grep -i proxmox | wc -l)
if [ "$PROXMOX_SC" -gt 0 ]; then
    print_success "Proxmox storage classes found: $PROXMOX_SC"
    kubectl get storageclass | grep -E "NAME|proxmox"
else
    print_warning "No Proxmox storage classes found"
fi

# 5.3 Check LINSTOR
print_check "LINSTOR storage"
if kubectl get crd | grep linstor >/dev/null 2>&1; then
    print_success "LINSTOR CRDs found"
else
    print_warning "LINSTOR not detected"
fi

#=============================================================================
# Section 6: Proxmox-Specific Resources
#=============================================================================
print_header "6. Proxmox-Specific Resources"

# 6.1 Check Proxmox credentials
print_check "Proxmox credentials secret"
if kubectl get secret -n capmox-system capmox-credentials >/dev/null 2>&1; then
    print_success "Proxmox credentials secret found"
    ENDPOINT=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_ENDPOINT}' 2>/dev/null | base64 -d)
    USER=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_USER}' 2>/dev/null | base64 -d)
    print_info "Endpoint: $ENDPOINT"
    print_info "User: $USER"
else
    print_fail "Proxmox credentials secret not found"
fi

# 6.2 Check ProxmoxCluster status
print_check "ProxmoxCluster resources status"
if kubectl get proxmoxcluster -A >/dev/null 2>&1; then
    CLUSTERS=$(kubectl get proxmoxcluster -A --no-headers | wc -l)
    READY=$(kubectl get proxmoxcluster -A --no-headers | grep -i "true" | wc -l)
    if [ "$READY" -eq "$CLUSTERS" ] && [ "$CLUSTERS" -gt 0 ]; then
        print_success "All ProxmoxCluster resources Ready: $READY/$CLUSTERS"
        kubectl get proxmoxcluster -A
    elif [ "$CLUSTERS" -gt 0 ]; then
        print_warning "Some ProxmoxCluster not Ready: $READY/$CLUSTERS"
        kubectl get proxmoxcluster -A
    else
        print_warning "No ProxmoxCluster resources found"
    fi
else
    print_warning "Cannot check ProxmoxCluster (CRD may not be installed)"
fi

# 6.3 Check ProxmoxMachine resources
print_check "ProxmoxMachine resources"
if kubectl get proxmoxmachine -A >/dev/null 2>&1; then
    MACHINES=$(kubectl get proxmoxmachine -A --no-headers 2>/dev/null | wc -l)
    if [ "$MACHINES" -gt 0 ]; then
        print_success "ProxmoxMachine resources found: $MACHINES"
        kubectl get proxmoxmachine -A
    else
        print_info "No ProxmoxMachine resources (VMs not provisioned via CAPI)"
    fi
else
    print_warning "Cannot check ProxmoxMachine"
fi

#=============================================================================
# Section 7: Network Components
#=============================================================================
print_header "7. Network Components Health"

# 7.1 Check pod networking
print_check "Pod networking (sample pod)"
TEST_POD="integrity-test-$$"
if kubectl run $TEST_POD --image=busybox:1.28 --restart=Never --command -- sleep 30 >/dev/null 2>&1; then
    sleep 5
    POD_IP=$(kubectl get pod $TEST_POD -o jsonpath='{.status.podIP}' 2>/dev/null)
    if [ -n "$POD_IP" ]; then
        print_success "Pod networking working (IP: $POD_IP)"
        kubectl delete pod $TEST_POD --force --grace-period=0 >/dev/null 2>&1
    else
        print_fail "Pod created but no IP allocated"
        kubectl delete pod $TEST_POD --force --grace-period=0 >/dev/null 2>&1
    fi
else
    print_fail "Cannot create test pod"
fi

# 7.2 Check DNS resolution
print_check "DNS resolution"
if kubectl run dns-test-$$ --image=busybox:1.28 --rm --restart=Never --command -- nslookup kubernetes.default >/dev/null 2>&1; then
    print_success "DNS resolution working"
else
    print_warning "DNS test failed (may be pod security policy)"
fi

# 7.3 Check service networking
print_check "Service networking"
if kubectl get svc kubernetes -n default >/dev/null 2>&1; then
    K8S_SVC_IP=$(kubectl get svc kubernetes -n default -o jsonpath='{.spec.clusterIP}')
    print_success "Kubernetes service accessible at $K8S_SVC_IP"
else
    print_fail "Cannot access Kubernetes service"
fi

#=============================================================================
# Section 8: Storage Components
#=============================================================================
print_header "8. Storage Components Health"

# 8.1 Check CSI drivers
print_check "CSI drivers installed"
CSI_DRIVERS=$(kubectl get csidriver --no-headers 2>/dev/null | wc -l)
if [ "$CSI_DRIVERS" -gt 0 ]; then
    print_success "CSI drivers found: $CSI_DRIVERS"
    kubectl get csidriver
else
    print_warning "No CSI drivers found"
fi

# 8.2 Check storage classes
print_check "Storage classes available"
SC_COUNT=$(kubectl get storageclass --no-headers 2>/dev/null | wc -l)
if [ "$SC_COUNT" -gt 0 ]; then
    print_success "Storage classes found: $SC_COUNT"
    kubectl get storageclass
else
    print_warning "No storage classes found"
fi

# 8.3 Check persistent volumes
print_check "Persistent volumes"
PV_COUNT=$(kubectl get pv --no-headers 2>/dev/null | wc -l)
if [ "$PV_COUNT" -gt 0 ]; then
    print_info "Persistent volumes found: $PV_COUNT"
    kubectl get pv | head -10
else
    print_info "No persistent volumes (may be expected)"
fi

#=============================================================================
# Section 9: Proxmox CCM
#=============================================================================
print_header "9. Proxmox Cloud Controller Manager"

# 9.1 Check CCM pods
print_check "Proxmox CCM deployment"
if kubectl get pods -A -l app=proxmox-cloud-controller-manager --no-headers 2>/dev/null | grep " Running " >/dev/null; then
    print_success "Proxmox CCM is running"
    kubectl get pods -A -l app=proxmox-cloud-controller-manager
else
    print_warning "Proxmox CCM not found or not running"
fi

#=============================================================================
# Section 10: Monitoring Integration
#=============================================================================
print_header "10. Monitoring Integration"

# 10.1 Check Prometheus
print_check "Prometheus deployment"
if kubectl get namespace cozy-monitoring >/dev/null 2>&1; then
    PROM_RUNNING=$(kubectl get pods -n cozy-monitoring -l app.kubernetes.io/name=prometheus --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$PROM_RUNNING" -gt 0 ]; then
        print_success "Prometheus running"
    else
        print_warning "Prometheus not running"
    fi
else
    print_warning "Monitoring namespace not found"
fi

# 10.2 Check Grafana
print_check "Grafana deployment"
if kubectl get namespace cozy-monitoring >/dev/null 2>&1; then
    GRAFANA_RUNNING=$(kubectl get pods -n cozy-monitoring -l app.kubernetes.io/name=grafana --no-headers 2>/dev/null | grep " Running " | wc -l)
    if [ "$GRAFANA_RUNNING" -gt 0 ]; then
        print_success "Grafana running"
    else
        print_warning "Grafana not running"
    fi
fi

#=============================================================================
# Section 11: Integration-Specific Checks
#=============================================================================
print_header "11. Integration-Specific Checks"

# 11.1 Check if Proxmox VMs are managed by CAPI
print_check "CAPI-managed Cluster resources"
CAPI_CLUSTERS=$(kubectl get clusters -A --no-headers 2>/dev/null | wc -l)
if [ "$CAPI_CLUSTERS" -gt 0 ]; then
    print_success "CAPI Cluster resources found: $CAPI_CLUSTERS"
    kubectl get clusters -A
else
    print_info "No CAPI Cluster resources (may not have tenant clusters yet)"
fi

# 11.2 Check Machine resources
print_check "CAPI Machine resources"
MACHINES=$(kubectl get machines -A --no-headers 2>/dev/null | wc -l)
if [ "$MACHINES" -gt 0 ]; then
    print_success "CAPI Machine resources found: $MACHINES"
    kubectl get machines -A
else
    print_info "No CAPI Machine resources"
fi

# 11.3 Check HelmReleases for Proxmox components
print_check "Proxmox-related HelmReleases"
if command -v kubectl-flux >/dev/null 2>&1 || kubectl get crd helmreleases.helm.toolkit.fluxcd.io >/dev/null 2>&1; then
    PROXMOX_RELEASES=$(kubectl get helmreleases -A 2>/dev/null | grep -i proxmox | wc -l)
    if [ "$PROXMOX_RELEASES" -gt 0 ]; then
        print_success "Proxmox HelmReleases found: $PROXMOX_RELEASES"
        kubectl get helmreleases -A | grep -i proxmox
    else
        print_info "No Proxmox-specific HelmReleases found"
    fi
fi

#=============================================================================
# Section 12: Security and RBAC
#=============================================================================
print_header "12. Security and RBAC Checks"

# 12.1 Check Proxmox service accounts
print_check "Proxmox service accounts"
SA_COUNT=$(kubectl get sa -A 2>/dev/null | grep -i proxmox | wc -l)
if [ "$SA_COUNT" -gt 0 ]; then
    print_success "Proxmox service accounts found: $SA_COUNT"
    kubectl get sa -A | grep -i proxmox
else
    print_warning "No Proxmox service accounts found"
fi

# 12.2 Check Proxmox secrets
print_check "Proxmox-related secrets"
SECRET_COUNT=$(kubectl get secrets -A 2>/dev/null | grep -i proxmox | wc -l)
if [ "$SECRET_COUNT" -gt 0 ]; then
    print_success "Proxmox secrets found: $SECRET_COUNT"
    kubectl get secrets -A | grep -i proxmox | awk '{print $1, $2, $3}'
else
    print_warning "No Proxmox secrets found"
fi

#=============================================================================
# Section 13: Resource Health Summary
#=============================================================================
print_header "13. Resource Health Summary"

# 13.1 Check pods in bad state
print_check "Pods in bad states across cluster"
FAILED_PODS=$(kubectl get pods -A --no-headers 2>/dev/null | grep -E "Error|CrashLoopBackOff|ImagePullBackOff" | wc -l)
UNKNOWN_PODS=$(kubectl get pods -A --no-headers 2>/dev/null | grep "Unknown" | wc -l)
PENDING_PODS=$(kubectl get pods -A --no-headers 2>/dev/null | grep "Pending" | wc -l)

if [ "$FAILED_PODS" -eq 0 ] && [ "$UNKNOWN_PODS" -eq 0 ]; then
    print_success "No pods in error states"
else
    print_warning "Pods in bad states: Failed=$FAILED_PODS, Unknown=$UNKNOWN_PODS, Pending=$PENDING_PODS"
    if [ "$FAILED_PODS" -gt 0 ]; then
        echo "Failed pods:"
        kubectl get pods -A | grep -E "Error|CrashLoopBackOff|ImagePullBackOff" | head -5
    fi
fi

# 13.2 Check events for errors
print_check "Recent cluster events"
ERROR_EVENTS=$(kubectl get events -A --sort-by='.lastTimestamp' 2>/dev/null | grep -i "error\|failed\|warning" | tail -5 | wc -l)
if [ "$ERROR_EVENTS" -eq 0 ]; then
    print_success "No recent error events"
else
    print_info "Recent events (last 5):"
    kubectl get events -A --sort-by='.lastTimestamp' | tail -5
fi

#=============================================================================
# Final Summary
#=============================================================================
print_header "FINAL SUMMARY"

echo -e "${BLUE}Total Checks: ${TOTAL_CHECKS}${NC}"
echo -e "${GREEN}Passed: ${PASSED_CHECKS}${NC}"
echo -e "${RED}Failed: ${FAILED_CHECKS}${NC}"
echo -e "${YELLOW}Warnings: ${WARNING_CHECKS}${NC}"
echo ""

SUCCESS_RATE=$((PASSED_CHECKS * 100 / TOTAL_CHECKS))
echo -e "${BLUE}Success Rate: ${SUCCESS_RATE}%${NC}"
echo ""

# Determine overall status
if [ "$FAILED_CHECKS" -eq 0 ] && [ "$WARNING_CHECKS" -lt 5 ]; then
    echo -e "${GREEN}✅ OVERALL STATUS: HEALTHY${NC}"
    echo -e "${GREEN}Proxmox integration is fully operational!${NC}"
    EXIT_CODE=0
elif [ "$FAILED_CHECKS" -lt 3 ]; then
    echo -e "${YELLOW}⚠️  OVERALL STATUS: DEGRADED${NC}"
    echo -e "${YELLOW}Proxmox integration has some issues but is functional${NC}"
    EXIT_CODE=1
else
    echo -e "${RED}❌ OVERALL STATUS: CRITICAL${NC}"
    echo -e "${RED}Proxmox integration has critical issues!${NC}"
    EXIT_CODE=2
fi

echo ""
echo "Report generated: $(date)"
echo "Logs saved to: integrity-check-$(date +%Y%m%d-%H%M%S).log"

exit $EXIT_CODE

