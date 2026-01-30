# Kube-OVN + Cilium Networking Setup

## Overview

This guide covers setting up Kube-OVN with Cilium for advanced networking in Proxmox Kubernetes integration.

## Prerequisites

- Kubernetes cluster with Proxmox nodes
- CNI plugin support
- Network policies enabled

## Step 1: Install Kube-OVN

1. Install Kube-OVN:
```bash
kubectl apply -f https://raw.githubusercontent.com/kubeovn/kube-ovn/master/yamls/kube-ovn.yaml
```

2. Verify installation:
```bash
kubectl get pods -n kube-system -l app=kube-ovn
```

## Step 2: Configure Kube-OVN

1. Create Kube-OVN configuration:
```yaml
# kube-ovn-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-ovn-config
  namespace: kube-system
data:
  default_interface_name: "eth0"
  default_cidr: "10.16.0.0/16"
  default_exclude_ips: "10.16.0.1"
  default_gateway: "10.16.0.1"
  default_gateway_check: "true"
  default_logical_gateway: "false"
  default_u2o_interconnection: "false"
  default_allow_live_migration: "true"
  default_vlan_id: "0"
  default_vlan_range: "1,4095"
  default_vpc: "ovn-cluster"
  default_vpc_subnet: "10.16.0.0/16"
  default_vpc_dns: "10.16.0.1"
  default_vpc_gateway: "10.16.0.1"
  default_vpc_gateway_check: "true"
  default_vpc_logical_gateway: "false"
  default_vpc_u2o_interconnection: "false"
  default_vpc_allow_live_migration: "true"
  default_vpc_vlan_id: "0"
  default_vpc_vlan_range: "1,4095"
  default_vpc_subnet_pool: "10.16.0.0/16"
  default_vpc_subnet_pool_start: "10.16.0.0"
  default_vpc_subnet_pool_end: "10.16.255.255"
  default_vpc_subnet_pool_cidr: "10.16.0.0/16"
  default_vpc_subnet_pool_gateway: "10.16.0.1"
  default_vpc_subnet_pool_exclude_ips: "10.16.0.1"
  default_vpc_subnet_pool_allow_live_migration: "true"
  default_vpc_subnet_pool_vlan_id: "0"
  default_vpc_subnet_pool_vlan_range: "1,4095"
  default_vpc_subnet_pool_u2o_interconnection: "false"
  default_vpc_subnet_pool_logical_gateway: "false"
  default_vpc_subnet_pool_gateway_check: "true"
  default_vpc_subnet_pool_dns: "10.16.0.1"
  default_vpc_subnet_pool_interface: "eth0"
  default_vpc_subnet_pool_mtu: "1500"
  default_vpc_subnet_pool_mac_address: ""
  default_vpc_subnet_pool_dhcp_options: ""
  default_vpc_subnet_pool_private: "false"
  default_vpc_subnet_pool_nat_outgoing: "true"
  default_vpc_subnet_pool_gateway_type: "distributed"
  default_vpc_subnet_pool_allow_live_migration: "true"
  default_vpc_subnet_pool_vlan_id: "0"
  default_vpc_subnet_pool_vlan_range: "1,4095"
  default_vpc_subnet_pool_u2o_interconnection: "false"
  default_vpc_subnet_pool_logical_gateway: "false"
  default_vpc_subnet_pool_gateway_check: "true"
  default_vpc_subnet_pool_dns: "10.16.0.1"
  default_vpc_subnet_pool_interface: "eth0"
  default_vpc_subnet_pool_mtu: "1500"
  default_vpc_subnet_pool_mac_address: ""
  default_vpc_subnet_pool_dhcp_options: ""
  default_vpc_subnet_pool_private: "false"
  default_vpc_subnet_pool_nat_outgoing: "true"
  default_vpc_subnet_pool_gateway_type: "distributed"
```

2. Apply configuration:
```bash
kubectl apply -f kube-ovn-config.yaml
```

## Step 3: Install Cilium

1. Install Cilium:
```bash
cilium install --version 1.14.0
```

2. Verify installation:
```bash
cilium status
```

## Step 4: Configure Cilium

1. Create Cilium configuration:
```yaml
# cilium-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cilium-config
  namespace: kube-system
data:
  # Identity allocation mode selects how identities are shared between cilium
  # nodes.  Possible values are "crd" or "kvstore".
  identity-allocation-mode: crd

  # If you want to run cilium in debug mode change this value to true
  debug: "false"

  # The agent can be put into the following three policy enforcement modes
  # default, always and never.
  enable-policy: "default"

  # Enable IPv4 connectivity between endpoints
  enable-ipv4: "true"

  # Enable IPv6 connectivity between endpoints
  enable-ipv6: "false"

  # Enable L7 proxy for L7 policy enforcement and visibility
  enable-l7-proxy: "true"

  # wait for kube-dns to be ready before starting cilium
  wait-for-kube-dns: "true"

  # wait for Cilium to be ready before starting other pods
  wait-for-cilium: "true"

  # Enable prometheus metrics on the configured port at /metrics
  prometheus-serve-addr: ":9962"

  # Enable tracing
  enable-tracing: "true"

  # Enable Hubble gRPC service
  enable-hubble: "true"

  # Unix domain socket for Hubble server to listen to
  hubble-socket-path: "/var/run/cilium/hubble.sock"

  # An additional address for Hubble server to listen to (e.g. ":4244")
  hubble-listen-address: ":4244"

  # Enable Hubble metrics server
  enable-hubble-metrics: "true"

  # List of metrics to enable
  hubble-metrics: "dns,drop,tcp,flow,port-distribution,icmp,http"

  # Enable Hubble metrics server
  enable-hubble-metrics: "true"

  # List of metrics to enable
  hubble-metrics: "dns,drop,tcp,flow,port-distribution,icmp,http"

  # Enable Hubble metrics server
  enable-hubble-metrics: "true"

  # List of metrics to enable
  hubble-metrics: "dns,drop,tcp,flow,port-distribution,icmp,http"
```

2. Apply configuration:
```bash
kubectl apply -f cilium-config.yaml
```

## Step 5: Configure Network Policies

1. Create network policy:
```yaml
# network-policy.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-dns
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          name: kube-system
    ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
```

2. Apply network policies:
```bash
kubectl apply -f network-policy.yaml
```

## Step 6: Test Networking

1. Create test pods:
```yaml
# test-pods.yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod-1
  labels:
    app: test
spec:
  containers:
  - name: test-container
    image: busybox
    command: ['sh', '-c', 'sleep 3600']
---
apiVersion: v1
kind: Pod
metadata:
  name: test-pod-2
  labels:
    app: test
spec:
  containers:
  - name: test-container
    image: busybox
    command: ['sh', '-c', 'sleep 3600']
```

2. Apply test pods:
```bash
kubectl apply -f test-pods.yaml
```

3. Test connectivity:
```bash
kubectl exec test-pod-1 -- ping -c 3 test-pod-2
```

## Troubleshooting

### Common Issues

1. **Kube-OVN Not Starting**:
   - Check CNI configuration
   - Verify network interfaces
   - Review Kube-OVN logs

2. **Cilium Not Starting**:
   - Check kernel version
   - Verify BPF support
   - Review Cilium logs

3. **Network Policies Not Working**:
   - Check policy enforcement
   - Verify pod selectors
   - Review policy logs

### Debug Commands

```bash
# Check Kube-OVN status
kubectl get pods -n kube-system -l app=kube-ovn

# Check Cilium status
cilium status

# Check network policies
kubectl get networkpolicies

# Check pod connectivity
kubectl exec test-pod-1 -- ping -c 3 test-pod-2
```
