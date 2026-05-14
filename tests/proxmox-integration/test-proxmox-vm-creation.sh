#!/bin/bash
# Proxmox VM Creation Test via Cluster API
# Tests creating VMs directly in Proxmox through Kubernetes API

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
NAMESPACE="default"
CLUSTER_NAME="test-proxmox"
VM_NAME="test-vm-1"
TIMEOUT=600

echo "======================================================="
echo "Proxmox VM Creation Test via Cluster API"
echo "======================================================="
echo ""

# Check if running remotely or locally
if [ -n "$KUBECONFIG" ]; then
    KUBECTL="kubectl"
else
    KUBECTL="ssh root@mgr.cp.if.ua 'export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig && kubectl'"
fi

echo -e "${BLUE}Step 1: Checking prerequisites...${NC}"

# Check if capmox controller is running
echo "Checking Cluster API Proxmox provider..."
if ! eval "$KUBECTL -n cozy-cluster-api get pods -l control-plane=capmox-controller-manager" &>/dev/null; then
    echo -e "${YELLOW}WARNING: capmox controller not found${NC}"
    echo "Checking available CAPI providers:"
    eval "$KUBECTL -n cozy-cluster-api get pods"
else
    echo -e "${GREEN}✅ capmox controller found${NC}"
fi

# Check if ProxmoxCluster exists
echo ""
echo "Checking for existing ProxmoxCluster..."
if eval "$KUBECTL get proxmoxclusters -A" &>/dev/null; then
    echo -e "${GREEN}ProxmoxCluster CRD exists${NC}"
    eval "$KUBECTL get proxmoxclusters -A"
else
    echo -e "${RED}ERROR: ProxmoxCluster CRD not found${NC}"
    exit 1
fi

echo ""
echo -e "${BLUE}Step 2: Creating test ProxmoxMachine...${NC}"

# Create ProxmoxMachine
cat <<EOF | eval "$KUBECTL apply -f -"
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: $VM_NAME
  namespace: $NAMESPACE
  labels:
    cluster.x-k8s.io/cluster-name: $CLUSTER_NAME
spec:
  nodeName: pve
  template: ubuntu-22.04
  cores: 2
  memory: 4096
  diskSize: 20
  network:
    default:
      bridge: vmbr0
      model: virtio
EOF

echo -e "${GREEN}✅ ProxmoxMachine created${NC}"

echo ""
echo -e "${BLUE}Step 3: Monitoring VM creation...${NC}"

START_TIME=$(date +%s)

# Monitor ProxmoxMachine status
for i in {1..60}; do
    echo "Attempt $i/60..."
    
    # Get machine status
    if eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE" &>/dev/null; then
        STATUS=$(eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE -o jsonpath='{.status.ready}'" 2>/dev/null || echo "")
        VM_ID=$(eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE -o jsonpath='{.status.vmID}'" 2>/dev/null || echo "")
        
        echo "  Status: $STATUS"
        echo "  VM ID: $VM_ID"
        
        if [ "$STATUS" == "true" ]; then
            echo -e "${GREEN}✅ VM is ready!${NC}"
            break
        fi
    fi
    
    sleep 10
done

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo -e "${BLUE}Step 4: Verifying VM in Proxmox...${NC}"

# Try to get VM details
VM_ID=$(eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE -o jsonpath='{.status.vmID}'" 2>/dev/null || echo "")

if [ -n "$VM_ID" ] && [ "$VM_ID" != "null" ]; then
    echo -e "${GREEN}VM ID in Proxmox: $VM_ID${NC}"
    
    # Try to check VM on Proxmox host (if accessible)
    if command -v qm &> /dev/null; then
        echo "Checking VM on Proxmox host..."
        qm list | grep "$VM_ID" || echo "VM not found in qm list"
        qm status "$VM_ID" || echo "Could not get VM status"
    else
        echo "Note: qm command not available (not running on Proxmox host)"
    fi
else
    echo -e "${YELLOW}VM ID not yet assigned${NC}"
fi

echo ""
echo "======================================================="
echo "Test Results"
echo "======================================================="
echo ""

# Get final status
if eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE" &>/dev/null; then
    echo "ProxmoxMachine details:"
    eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE -o yaml"
    
    STATUS=$(eval "$KUBECTL get proxmoxmachine $VM_NAME -n $NAMESPACE -o jsonpath='{.status.ready}'" 2>/dev/null || echo "false")
    
    echo ""
    if [ "$STATUS" == "true" ]; then
        echo -e "${GREEN}======================================================="
        echo -e "✅ VM CREATION SUCCESSFUL"
        echo -e "=======================================================${NC}"
        echo ""
        echo "VM Details:"
        echo "  Name: $VM_NAME"
        echo "  Namespace: $NAMESPACE"
        echo "  VM ID: $VM_ID"
        echo "  Time to create: ${ELAPSED}s"
        echo ""
        echo "To check VM in Proxmox:"
        echo "  qm list"
        echo "  qm status $VM_ID"
        echo "  qm config $VM_ID"
        echo ""
        echo "To delete VM:"
        echo "  kubectl delete proxmoxmachine $VM_NAME -n $NAMESPACE"
        exit 0
    else
        echo -e "${YELLOW}======================================================="
        echo -e "⚠️  VM CREATION IN PROGRESS"
        echo -e "=======================================================${NC}"
        echo ""
        echo "VM is still being created. This might take several minutes."
        echo "Check status with:"
        echo "  kubectl get proxmoxmachine $VM_NAME -n $NAMESPACE"
        echo "  kubectl describe proxmoxmachine $VM_NAME -n $NAMESPACE"
        echo ""
        echo "Check logs:"
        echo "  kubectl -n cozy-cluster-api logs -l control-plane=capmox-controller-manager --tail=50"
        exit 1
    fi
else
    echo -e "${RED}======================================================="
    echo -e "❌ VM CREATION FAILED"
    echo -e "=======================================================${NC}"
    echo ""
    echo "ProxmoxMachine resource not found."
    echo "Check CAPI logs:"
    echo "  kubectl -n cozy-cluster-api logs -l control-plane=capmox-controller-manager"
    exit 1
fi

