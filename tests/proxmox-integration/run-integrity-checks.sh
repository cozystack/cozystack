#!/bin/bash
# Run all integrity checks for Proxmox integration
# This script runs both shell-based and Python-based integrity checks

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
NC='\033[0m'

# Configuration
KUBECONFIG=${KUBECONFIG:-"/root/cozy/mgr-cozy/kubeconfig"}
LOG_DIR="./logs"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
LOG_FILE="${LOG_DIR}/integrity-check-${TIMESTAMP}.log"

# Create log directory
mkdir -p "$LOG_DIR"

echo -e "${MAGENTA}"
echo "╔════════════════════════════════════════════════════════════╗"
echo "║     PROXMOX INTEGRATION COMPREHENSIVE INTEGRITY CHECK      ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo -e "${NC}"
echo ""
echo "Date: $(date)"
echo "Kubeconfig: $KUBECONFIG"
echo "Log file: $LOG_FILE"
echo ""

# Export kubeconfig
export KUBECONFIG

# Function to run check and capture result
run_check() {
    local check_name=$1
    local check_command=$2
    
    echo -e "\n${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}Running: $check_name${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}\n"
    
    if eval "$check_command" 2>&1 | tee -a "$LOG_FILE"; then
        echo -e "${GREEN}✅ $check_name: PASSED${NC}" | tee -a "$LOG_FILE"
        return 0
    else
        local exit_code=$?
        if [ $exit_code -eq 1 ]; then
            echo -e "${YELLOW}⚠️  $check_name: DEGRADED${NC}" | tee -a "$LOG_FILE"
        else
            echo -e "${RED}❌ $check_name: FAILED${NC}" | tee -a "$LOG_FILE"
        fi
        return $exit_code
    fi
}

# Track overall status
OVERALL_STATUS=0

#=============================================================================
# 1. Basic System Integrity Check (Shell-based)
#=============================================================================
if [ -f "./system-integrity-check.sh" ]; then
    chmod +x ./system-integrity-check.sh
    if run_check "Basic System Integrity Check" "./system-integrity-check.sh"; then
        :
    else
        OVERALL_STATUS=1
    fi
else
    echo -e "${YELLOW}⚠️  system-integrity-check.sh not found, skipping${NC}"
fi

#=============================================================================
# 2. Comprehensive Python Integrity Check
#=============================================================================
echo -e "\n${BLUE}═══════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Running: Comprehensive Python Integrity Check${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}\n"

if [ -f "./integrity_checker.py" ]; then
    chmod +x ./integrity_checker.py
    if python3 ./integrity_checker.py 2>&1 | tee -a "$LOG_FILE"; then
        echo -e "${GREEN}✅ Python Integrity Check: PASSED${NC}" | tee -a "$LOG_FILE"
    else
        exit_code=$?
        if [ $exit_code -eq 1 ]; then
            echo -e "${YELLOW}⚠️  Python Integrity Check: DEGRADED${NC}" | tee -a "$LOG_FILE"
            OVERALL_STATUS=1
        else
            echo -e "${RED}❌ Python Integrity Check: FAILED${NC}" | tee -a "$LOG_FILE"
            OVERALL_STATUS=2
        fi
    fi
else
    echo -e "${YELLOW}⚠️  integrity_checker.py not found, skipping${NC}"
fi

#=============================================================================
# 3. Proxmox API Direct Test
#=============================================================================
echo -e "\n${BLUE}═══════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Running: Proxmox API Direct Test${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}\n"

# Get credentials
PROXMOX_ENDPOINT=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_ENDPOINT}' 2>/dev/null | base64 -d)
PROXMOX_USER=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_USER}' 2>/dev/null | base64 -d)
PROXMOX_PASS=$(kubectl get secret capmox-credentials -n capmox-system -o jsonpath='{.data.PROXMOX_PASSWORD}' 2>/dev/null | base64 -d)

if [ -n "$PROXMOX_ENDPOINT" ] && [ -n "$PROXMOX_USER" ] && [ -n "$PROXMOX_PASS" ]; then
    echo "Testing Proxmox API at: $PROXMOX_ENDPOINT"
    
    # Get ticket
    TICKET_RESPONSE=$(curl -k -s -d "username=$PROXMOX_USER&password=$PROXMOX_PASS" \
        ${PROXMOX_ENDPOINT}/api2/json/access/ticket)
    
    if echo "$TICKET_RESPONSE" | grep -q "ticket"; then
        echo -e "${GREEN}✅ Proxmox API authentication: PASSED${NC}"
        
        TICKET=$(echo "$TICKET_RESPONSE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["ticket"])' 2>/dev/null)
        CSRF=$(echo "$TICKET_RESPONSE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["CSRFPreventionToken"])' 2>/dev/null)
        
        # Test version endpoint
        VERSION_RESPONSE=$(curl -k -s -H "Cookie: PVEAuthCookie=$TICKET" \
            ${PROXMOX_ENDPOINT}/api2/json/version)
        
        if echo "$VERSION_RESPONSE" | grep -q "version"; then
            VERSION=$(echo "$VERSION_RESPONSE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["version"])' 2>/dev/null)
            echo -e "${GREEN}✅ Proxmox VE version: $VERSION${NC}"
        fi
        
        # Test nodes endpoint
        NODES_RESPONSE=$(curl -k -s -H "Cookie: PVEAuthCookie=$TICKET" \
            ${PROXMOX_ENDPOINT}/api2/json/nodes)
        
        if echo "$NODES_RESPONSE" | grep -q "node"; then
            NODE_COUNT=$(echo "$NODES_RESPONSE" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null)
            echo -e "${GREEN}✅ Proxmox nodes found: $NODE_COUNT${NC}"
        fi
        
        # Test storage endpoint
        STORAGE_RESPONSE=$(curl -k -s -H "Cookie: PVEAuthCookie=$TICKET" \
            ${PROXMOX_ENDPOINT}/api2/json/storage)
        
        if echo "$STORAGE_RESPONSE" | grep -q "storage"; then
            STORAGE_COUNT=$(echo "$STORAGE_RESPONSE" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null)
            echo -e "${GREEN}✅ Proxmox storage pools found: $STORAGE_COUNT${NC}"
        fi
    else
        echo -e "${RED}❌ Proxmox API authentication: FAILED${NC}"
        OVERALL_STATUS=2
    fi
else
    echo -e "${YELLOW}⚠️  Cannot retrieve Proxmox credentials from cluster${NC}"
    OVERALL_STATUS=1
fi

#=============================================================================
# 4. Integration Components Summary
#=============================================================================
echo -e "\n${BLUE}═══════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Integration Components Summary${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}\n"

# Create summary table
echo "Component                    Status"
echo "─────────────────────────────────────────────────────"

# Kubernetes
K8S_STATUS=$(kubectl cluster-info >/dev/null 2>&1 && echo "✅ Running" || echo "❌ Failed")
echo "Kubernetes API               $K8S_STATUS"

# Nodes
READY_NODES=$(kubectl get nodes --no-headers 2>/dev/null | grep " Ready " | wc -l)
echo "Nodes Ready                  ✅ $READY_NODES"

# CAPI
CAPI_RUNNING=$(kubectl get pods -n cozy-cluster-api --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$CAPI_RUNNING" -gt 0 ]; then
    echo "CAPI Controllers             ✅ $CAPI_RUNNING running"
else
    echo "CAPI Controllers             ❌ 0 running"
fi

# Proxmox Provider
CAPMOX_RUNNING=$(kubectl get pods -n capmox-system --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$CAPMOX_RUNNING" -gt 0 ]; then
    echo "Proxmox CAPI Provider        ✅ $CAPMOX_RUNNING running"
else
    echo "Proxmox CAPI Provider        ❌ 0 running"
fi

# ProxmoxCluster
CLUSTER_READY=$(kubectl get proxmoxcluster -A --no-headers 2>/dev/null | grep -i "true" | wc -l)
if [ "$CLUSTER_READY" -gt 0 ]; then
    echo "ProxmoxCluster Ready         ✅ $CLUSTER_READY"
else
    echo "ProxmoxCluster Ready         ⚠️  0"
fi

# CoreDNS
DNS_RUNNING=$(kubectl get pods -n kube-system -l k8s-app=kube-dns --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$DNS_RUNNING" -gt 0 ]; then
    echo "CoreDNS                      ✅ $DNS_RUNNING running"
else
    echo "CoreDNS                      ❌ 0 running"
fi

# Kube-OVN
KUBEOVN_RUNNING=$(kubectl get pods -n cozy-kubeovn -l app=kube-ovn-controller --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$KUBEOVN_RUNNING" -gt 0 ]; then
    echo "Kube-OVN Controller          ✅ $KUBEOVN_RUNNING running"
else
    echo "Kube-OVN Controller          ❌ 0 running"
fi

# Cilium
CILIUM_RUNNING=$(kubectl get pods -n cozy-cilium --no-headers 2>/dev/null | grep " Running " | wc -l)
if [ "$CILIUM_RUNNING" -gt 0 ]; then
    echo "Cilium CNI                   ✅ $CILIUM_RUNNING running"
else
    echo "Cilium CNI                   ⚠️  0 running"
fi

echo "─────────────────────────────────────────────────────"

#=============================================================================
# Final Report
#=============================================================================
echo -e "\n${MAGENTA}═══════════════════════════════════════════════════════════${NC}"
echo -e "${MAGENTA}FINAL REPORT${NC}"
echo -e "${MAGENTA}═══════════════════════════════════════════════════════════${NC}\n"

echo "Full log saved to: $LOG_FILE"
echo ""

if [ "$OVERALL_STATUS" -eq 0 ]; then
    echo -e "${GREEN}╔══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║         ✅ ALL INTEGRITY CHECKS PASSED ✅                ║${NC}"
    echo -e "${GREEN}╚══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${GREEN}Proxmox integration is fully operational and healthy!${NC}"
elif [ "$OVERALL_STATUS" -eq 1 ]; then
    echo -e "${YELLOW}╔══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${YELLOW}║      ⚠️  INTEGRITY CHECKS COMPLETED WITH WARNINGS       ║${NC}"
    echo -e "${YELLOW}╚══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${YELLOW}Proxmox integration is functional but has some issues.${NC}"
    echo -e "${YELLOW}Review the log file for details: $LOG_FILE${NC}"
else
    echo -e "${RED}╔══════════════════════════════════════════════════════════╗${NC}"
    echo -e "${RED}║         ❌ INTEGRITY CHECKS FAILED ❌                    ║${NC}"
    echo -e "${RED}╚══════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "${RED}Proxmox integration has critical issues!${NC}"
    echo -e "${RED}Review the log file immediately: $LOG_FILE${NC}"
fi

echo ""
echo "To view the full log: cat $LOG_FILE"
echo "To view errors only: grep -E 'FAIL|ERROR' $LOG_FILE"
echo ""

exit $OVERALL_STATUS

