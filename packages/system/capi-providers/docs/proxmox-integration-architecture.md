# Proxmox Kubernetes Integration Architecture

## Overview

This document describes the integration of Proxmox VE with Kubernetes using operator patterns, where Proxmox acts as a worker node and VMs serve as Kubernetes tenants using Kamaji.

## Architecture Components

### 1. Core Components

- **Cluster API Proxmox Provider** (ionos-cloud/cluster-api-provider-proxmox)
- **Proxmox CSI Driver** (proxmox-csi)
- **Kube-OVN + Cilium** (networking)
- **Kamaji** (VM as Kubernetes tenants)
- **Custom Operators** (management and orchestration)

### 2. Integration Flow

```
Proxmox VE → Cluster API → Kubernetes Control Plane → Kamaji → VM Tenants
     ↓
Proxmox CSI → Storage Management
     ↓
Kube-OVN + Cilium → Network Management
```

## Component Details

### Cluster API Proxmox Provider

**Purpose**: Manages Proxmox VMs as Kubernetes nodes
**Features**:
- VM lifecycle management
- Node provisioning and deprovisioning
- Integration with Proxmox API

### Proxmox CSI

**Purpose**: Provides persistent storage for Kubernetes
**Features**:
- Dynamic volume provisioning
- Volume snapshots and cloning
- Integration with Proxmox storage backends

### Kube-OVN + Cilium

**Purpose**: Advanced networking capabilities
**Features**:
- Multi-tenant network isolation
- Service mesh capabilities
- Network policies enforcement

### Kamaji

**Purpose**: Manages VMs as Kubernetes tenants
**Features**:
- VM-based Kubernetes clusters
- Resource isolation
- Multi-tenancy support

## Network Architecture

### Control Plane Network
- Proxmox management network
- Kubernetes API server access
- Cluster API communication

### Data Plane Network
- VM-to-VM communication
- Pod-to-pod networking
- External service access

### Storage Network
- Proxmox storage backend access
- CSI driver communication
- Volume provisioning

## Security Considerations

1. **Network Isolation**
   - Separate networks for different components
   - Firewall rules and network policies
   - VPN access for management

2. **Authentication & Authorization**
   - Proxmox API authentication
   - Kubernetes RBAC
   - CSI driver permissions

3. **Data Protection**
   - Volume encryption
   - Backup and recovery
   - Access logging

## Deployment Strategy

### Phase 1: Infrastructure Setup
1. Deploy Cluster API Proxmox provider
2. Configure Proxmox CSI
3. Set up networking (Kube-OVN + Cilium)

### Phase 2: Tenant Management
1. Deploy Kamaji
2. Configure VM templates
3. Set up tenant isolation

### Phase 3: Operations
1. Deploy monitoring and logging
2. Set up backup and recovery
3. Implement security policies
