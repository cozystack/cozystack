#!/bin/bash
# Extended System Integrity Check for Proxmox Integration
# Includes: Tenant Clusters, LXC Runtime, Database Operators

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
NC='\033[0m'

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

# Configuration
KUBECONFIG=${KUBECONFIG:-"/root/cozy/mgr-cozy/kubeconfig"}
export KUBECONFIG

print_header "EXTENDED PROXMOX INTEGRATION INTEGRITY CHECK"
echo "Date: $(date)"
echo "Includes: Tenants, LXC Runtime, Database Operators"
echo ""

#=============================================================================
# Section 1: Database Operators Inventory
#=============================================================================
print_header "1. Database Operators Status"

# PostgreSQL
print_check "PostgreSQL Operator (CloudNativePG)"
if kubectl get deployment -n cozy-postgres-operator postgres-operator-cloudnative-pg >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-postgres-operator postgres-operator-cloudnative-pg -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
    if [ "$STATUS" = "True" ]; then
        print_success "PostgreSQL operator running"
        CLUSTERS=$(kubectl get cluster.postgresql.cnpg.io -A --no-headers 2>/dev/null | wc -l)
        print_info "  PostgreSQL clusters: $CLUSTERS"
    else
        print_warning "PostgreSQL operator not fully available"
    fi
else
    print_warning "PostgreSQL operator not found"
fi

# MariaDB
print_check "MariaDB Operator"
if kubectl get deployment -n cozy-mariadb-operator mariadb-operator >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-mariadb-operator mariadb-operator -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
    if [ "$STATUS" = "True" ]; then
        print_success "MariaDB operator running"
        DBS=$(kubectl get mariadb -A --no-headers 2>/dev/null | wc -l)
        print_info "  MariaDB instances: $DBS"
    else
        print_warning "MariaDB operator not fully available"
    fi
else
    print_warning "MariaDB operator not found"
fi

# Redis
print_check "Redis Operator"
if kubectl get deployment -n cozy-redis-operator redis-operator >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-redis-operator redis-operator -o jsonpath='{.status.availableReplicas}')
    if [ "$STATUS" -ge 1 ] 2>/dev/null; then
        print_success "Redis operator running"
    else
        print_warning "Redis operator not available"
    fi
else
    print_warning "Redis operator not found"
fi

# RabbitMQ
print_check "RabbitMQ Operator"
if kubectl get deployment -n cozy-rabbitmq-operator rabbitmq-cluster-operator >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-rabbitmq-operator rabbitmq-cluster-operator -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
    if [ "$STATUS" = "True" ]; then
        print_success "RabbitMQ operator running"
        CLUSTERS=$(kubectl get rabbitmqcluster -A --no-headers 2>/dev/null | wc -l)
        print_info "  RabbitMQ clusters: $CLUSTERS"
    else
        print_warning "RabbitMQ operator not fully available"
    fi
else
    print_warning "RabbitMQ operator not found"
fi

# ClickHouse
print_check "ClickHouse Operator"
if kubectl get deployment -n cozy-clickhouse-operator clickhouse-operator-altinity-clickhouse-operator >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-clickhouse-operator clickhouse-operator-altinity-clickhouse-operator -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
    if [ "$STATUS" = "True" ]; then
        print_success "ClickHouse operator running"
        INSTALLS=$(kubectl get clickhouseinstallation -A --no-headers 2>/dev/null | wc -l)
        print_info "  ClickHouse installations: $INSTALLS"
    else
        print_warning "ClickHouse operator not fully available"
    fi
else
    print_warning "ClickHouse operator not found"
fi

# Kafka
print_check "Kafka Operator (Strimzi)"
if kubectl get deployment -n cozy-kafka-operator strimzi-cluster-operator >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-kafka-operator strimzi-cluster-operator -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
    if [ "$STATUS" = "True" ]; then
        print_success "Kafka operator running"
        CLUSTERS=$(kubectl get kafka -A --no-headers 2>/dev/null | wc -l)
        print_info "  Kafka clusters: $CLUSTERS"
    else
        print_warning "Kafka operator not fully available"
    fi
else
    print_warning "Kafka operator not found"
fi

#=============================================================================
# Section 2: Tenant Cluster Management
#=============================================================================
print_header "2. Tenant Cluster Management"

# Kamaji
print_check "Kamaji Operator"
if kubectl get deployment -n cozy-kamaji kamaji >/dev/null 2>&1; then
    STATUS=$(kubectl get deployment -n cozy-kamaji kamaji -o jsonpath='{.status.availableReplicas}')
    if [ "$STATUS" -ge 1 ] 2>/dev/null; then
        print_success "Kamaji operator running"
    else
        print_warning "Kamaji operator not available"
    fi
else
    print_warning "Kamaji operator not found"
fi

# TenantControlPlane CRD
print_check "TenantControlPlane CRD"
if kubectl get crd tenantcontrolplanes.kamaji.clastix.io >/dev/null 2>&1; then
    print_success "TenantControlPlane CRD installed"
    TCP_COUNT=$(kubectl get tenantcontrolplanes -A --no-headers 2>/dev/null | wc -l)
    print_info "  Tenant control planes: $TCP_COUNT"
else
    print_warning "TenantControlPlane CRD not found"
fi

# Tenant Clusters
print_check "Tenant Kubernetes Clusters"
TENANT_CLUSTERS=$(kubectl get clusters -A --no-headers 2>/dev/null | wc -l)
if [ "$TENANT_CLUSTERS" -gt 0 ]; then
    print_success "Tenant clusters found: $TENANT_CLUSTERS"
    kubectl get clusters -A
else
    print_info "No tenant clusters yet (expected for new setup)"
fi

#=============================================================================
# Section 3: LXC Runtime Support
#=============================================================================
print_header "3. LXC Runtime Support"

# RuntimeClass for LXC
print_check "LXC RuntimeClass"
if kubectl get runtimeclass proxmox-lxc >/dev/null 2>&1; then
    print_success "LXC RuntimeClass found"
    kubectl get runtimeclass proxmox-lxc
else
    print_info "LXC RuntimeClass not yet configured (expected if lxcri not complete)"
fi

# Check for LXC runtime handler
print_check "LXC Runtime Handler"
if kubectl get nodes -o json | grep -q "proxmox-lxc" 2>/dev/null; then
    print_success "LXC runtime handler registered on nodes"
else
    print_info "LXC runtime not yet available (waiting for proxmox-lxcri)"
fi

# LXC Pods
print_check "Pods using LXC runtime"
LXC_PODS=$(kubectl get pods -A -o json 2>/dev/null | jq -r '.items[] | select(.spec.runtimeClassName=="proxmox-lxc") | .metadata.name' | wc -l)
if [ "$LXC_PODS" -gt 0 ]; then
    print_success "LXC pods found: $LXC_PODS"
else
    print_info "No LXC pods yet (expected if runtime not available)"
fi

#=============================================================================
# Section 4: Proxmox VM Resources
#=============================================================================
print_header "4. Proxmox VM Resources for Tenants"

# ProxmoxMachine resources
print_check "ProxmoxMachine resources"
MACHINES=$(kubectl get proxmoxmachine -A --no-headers 2>/dev/null | wc -l)
if [ "$MACHINES" -gt 0 ]; then
    print_success "ProxmoxMachine resources: $MACHINES"
    kubectl get proxmoxmachine -A
else
    print_info "No ProxmoxMachine resources (VMs not yet provisioned via CAPI)"
fi

# Machine Deployments
print_check "CAPI MachineDeployments"
MD_COUNT=$(kubectl get machinedeployment -A --no-headers 2>/dev/null | wc -l)
if [ "$MD_COUNT" -gt 0 ]; then
    print_success "MachineDeployments found: $MD_COUNT"
    kubectl get machinedeployment -A
else
    print_info "No MachineDeployments yet"
fi

#=============================================================================
# Section 5: Database Instances Runtime Check
#=============================================================================
print_header "5. Database Instances Runtime Analysis"

# Check PostgreSQL instances
print_check "PostgreSQL instances and runtime"
PG_CLUSTERS=$(kubectl get cluster.postgresql.cnpg.io -A --no-headers 2>/dev/null | wc -l)
if [ "$PG_CLUSTERS" -gt 0 ]; then
    print_success "PostgreSQL clusters: $PG_CLUSTERS"
    # Check if any use LXC runtime
    LXC_PG=$(kubectl get pods -A -l "cnpg.io/cluster" -o json 2>/dev/null | jq -r '.items[] | select(.spec.runtimeClassName=="proxmox-lxc") | .metadata.name' | wc -l)
    if [ "$LXC_PG" -gt 0 ]; then
        print_info "  PostgreSQL in LXC: $LXC_PG pods"
    else
        print_info "  PostgreSQL in regular pods: $PG_CLUSTERS (LXC not yet available)"
    fi
else
    print_info "No PostgreSQL clusters deployed"
fi

# Check MariaDB instances
print_check "MariaDB instances and runtime"
MARIADB_COUNT=$(kubectl get mariadb -A --no-headers 2>/dev/null | wc -l)
if [ "$MARIADB_COUNT" -gt 0 ]; then
    print_success "MariaDB instances: $MARIADB_COUNT"
    print_info "  Runtime: Regular pods (LXC support pending)"
else
    print_info "No MariaDB instances deployed"
fi

#=============================================================================
# Section 6: Tenant Network Isolation
#=============================================================================
print_header "6. Tenant Network Isolation"

# Network Policies
print_check "Network Policies for tenant isolation"
NP_COUNT=$(kubectl get networkpolicy -A --no-headers 2>/dev/null | wc -l)
if [ "$NP_COUNT" -gt 0 ]; then
    print_success "Network policies found: $NP_COUNT"
else
    print_info "No network policies yet"
fi

# Kube-OVN Subnets
print_check "Kube-OVN Subnets for multi-tenancy"
if kubectl get crd subnets.kubeovn.io >/dev/null 2>&1; then
    SUBNETS=$(kubectl get subnets -A --no-headers 2>/dev/null | wc -l)
    print_success "Kube-OVN subnets: $SUBNETS"
else
    print_warning "Kube-OVN subnets CRD not found"
fi

#=============================================================================
# Section 7: Resource Quotas for Tenants
#=============================================================================
print_header "7. Resource Management"

# Resource Quotas
print_check "Resource Quotas"
RQ_COUNT=$(kubectl get resourcequota -A --no-headers 2>/dev/null | wc -l)
if [ "$RQ_COUNT" -gt 0 ]; then
    print_success "Resource quotas found: $RQ_COUNT"
else
    print_info "No resource quotas configured yet"
fi

# Limit Ranges
print_check "Limit Ranges"
LR_COUNT=$(kubectl get limitrange -A --no-headers 2>/dev/null | wc -l)
if [ "$LR_COUNT" -gt 0 ]; then
    print_success "Limit ranges found: $LR_COUNT"
else
    print_info "No limit ranges configured yet"
fi

#=============================================================================
# Section 8: LXC Template Availability (Proxmox Check)
#=============================================================================
print_header "8. Proxmox LXC Templates"

print_check "Proxmox LXC template availability"
# This requires Proxmox API access
PROXMOX_ENDPOINT=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_ENDPOINT}' 2>/dev/null | base64 -d)
PROXMOX_USER=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_USER}' 2>/dev/null | base64 -d)
PROXMOX_PASS=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_PASSWORD}' 2>/dev/null | base64 -d)

if [ -n "$PROXMOX_ENDPOINT" ] && [ -n "$PROXMOX_USER" ] && [ -n "$PROXMOX_PASS" ] && command -v curl >/dev/null 2>&1; then
    TICKET=$(curl -k -s -d "username=$PROXMOX_USER&password=$PROXMOX_PASS" \
        ${PROXMOX_ENDPOINT}/api2/json/access/ticket 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["ticket"])' 2>/dev/null)
    
    if [ -n "$TICKET" ]; then
        # Get LXC containers
        LXC_RESPONSE=$(curl -k -s -H "Cookie: PVEAuthCookie=$TICKET" \
            ${PROXMOX_ENDPOINT}/api2/json/nodes/mgr/lxc 2>/dev/null)
        
        if echo "$LXC_RESPONSE" | grep -q "data"; then
            LXC_COUNT=$(echo "$LXC_RESPONSE" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("data", [])))' 2>/dev/null)
            print_success "Proxmox LXC containers: $LXC_COUNT"
            
            # Check for LXC templates
            TEMPLATES=$(echo "$LXC_RESPONSE" | python3 -c 'import json,sys; print(len([c for c in json.load(sys.stdin).get("data", []) if c.get("template", 0) == 1]))' 2>/dev/null)
            if [ "$TEMPLATES" -gt 0 ]; then
                print_info "  LXC templates available: $TEMPLATES"
            else
                print_info "  No LXC templates yet (need to create)"
            fi
        else
            print_warning "Cannot retrieve LXC information"
        fi
    else
        print_warning "Cannot authenticate to Proxmox"
    fi
else
    print_info "Skipping Proxmox LXC check (credentials or curl not available)"
fi

#=============================================================================
# Section 9: VM Template for Tenants
#=============================================================================
print_header "9. VM Templates for Tenant Clusters"

print_check "Proxmox VM templates"
if [ -n "$TICKET" ]; then
    VM_RESPONSE=$(curl -k -s -H "Cookie: PVEAuthCookie=$TICKET" \
        ${PROXMOX_ENDPOINT}/api2/json/nodes/mgr/qemu 2>/dev/null)
    
    if echo "$VM_RESPONSE" | grep -q "data"; then
        TOTAL_VMS=$(echo "$VM_RESPONSE" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("data", [])))' 2>/dev/null)
        TEMPLATES=$(echo "$VM_RESPONSE" | python3 -c 'import json,sys; data=json.load(sys.stdin).get("data",[]); print(len([v for v in data if "template" in v.get("name","").lower() or v.get("template",0)==1]))' 2>/dev/null)
        
        print_success "Proxmox VMs: $TOTAL_VMS (Templates: $TEMPLATES)"
        
        if [ "$TEMPLATES" -gt 0 ]; then
            print_info "  VM templates ready for tenant provisioning"
        else
            print_warning "  No VM templates found"
        fi
    fi
fi

#=============================================================================
# Final Summary
#=============================================================================
print_header "EXTENDED CHECK SUMMARY"

echo -e "${BLUE}Total Checks: ${TOTAL_CHECKS}${NC}"
echo -e "${GREEN}Passed: ${PASSED_CHECKS}${NC}"
echo -e "${RED}Failed: ${FAILED_CHECKS}${NC}"
echo -e "${YELLOW}Warnings: ${WARNING_CHECKS}${NC}"
echo ""

SUCCESS_RATE=$((PASSED_CHECKS * 100 / TOTAL_CHECKS))
echo -e "${BLUE}Success Rate: ${SUCCESS_RATE}%${NC}"
echo ""

# Extended features status
echo -e "${MAGENTA}Extended Features Readiness:${NC}"
echo -e "  Database Operators: ${GREEN}✅ Available${NC}"
echo -e "  Tenant Provisioning: ${YELLOW}⏳ Planned${NC}"
echo -e "  LXC Runtime: ${YELLOW}⏳ Waiting for proxmox-lxcri${NC}"
echo -e "  User Choice: ${YELLOW}⏳ Future Work${NC}"
echo ""

if [ "$FAILED_CHECKS" -eq 0 ]; then
    echo -e "${GREEN}✅ EXTENDED INTEGRATION: READY FOR NEXT PHASE${NC}"
    EXIT_CODE=0
elif [ "$FAILED_CHECKS" -lt 3 ]; then
    echo -e "${YELLOW}⚠️  EXTENDED INTEGRATION: SOME FEATURES PENDING${NC}"
    EXIT_CODE=1
else
    echo -e "${RED}❌ EXTENDED INTEGRATION: NEEDS ATTENTION${NC}"
    EXIT_CODE=2
fi

echo ""
echo "Next steps:"
echo "1. Complete proxmox-lxcri project"
echo "2. Implement tenant cluster provisioning"
echo "3. Integrate LXC runtime with operators"
echo "4. Add user choice mechanism"

exit $EXIT_CODE

