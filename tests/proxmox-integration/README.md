# Proxmox Integration Tests

This directory contains comprehensive tests for Proxmox integration with Kubernetes and CozyStack.

## Integration Steps and Tests

### Step 1: Proxmox API Connection and Authentication
- **Objective**: Verify connection to Proxmox API and authentication
- **Tests**: `step1-api-connection/`

### Step 2: Network and Storage Configuration  
- **Objective**: Configure Proxmox networking and storage for Kubernetes
- **Tests**: `step2-network-storage/`

### Step 3: VM Creation and Management via Cluster API
- **Objective**: Create and manage VMs using Cluster API Proxmox provider
- **Tests**: `step3-vm-management/`

### Step 4: Proxmox Server as Kubernetes Worker
- **Objective**: Integrate Proxmox server as worker node via kubeadm
- **Tests**: `step4-worker-integration/`

### Step 5: CSI Storage Integration
- **Objective**: Configure Proxmox CSI for persistent storage
- **Tests**: `step5-csi-storage/`

### Step 6: Network Integration and Policies
- **Objective**: Setup network policies and integration
- **Tests**: `step6-network-policies/`

### Step 7: Monitoring and Logging
- **Objective**: Configure monitoring and logging for Proxmox resources
- **Tests**: `step7-monitoring/`

### Step 8: End-to-End Integration Testing
- **Objective**: Full integration validation
- **Tests**: `step8-e2e/`

## Running Tests

```bash
# Run all tests
./run-all-tests.sh

# Run specific step tests
./run-step-tests.sh <step-number>

# Run individual test
pytest step1-api-connection/test_proxmox_api.py
```

## Test Requirements

- Python 3.8+
- pytest
- kubectl
- helm
- Proxmox VE API access
- Kubernetes cluster access
