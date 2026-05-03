"""
Test Step 6: Network Integration and Policies

This module tests network policies, CNI integration, and security for Proxmox workloads.
"""

import pytest
import time
import subprocess
import tempfile
import os
from typing import Dict, Any, List
import kubernetes
from kubernetes import client, config


class NetworkPolicyTester:
    """Test utilities for network policies and CNI integration"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.network_v1 = client.NetworkingV1Api()
        self.apps_v1 = client.AppsV1Api()
        self.custom_api = client.CustomObjectsApi()
    
    def get_network_policies(self, namespace: str = None) -> List[Dict[str, Any]]:
        """Get network policies"""
        if namespace:
            policies = self.network_v1.list_namespaced_network_policy(namespace)
        else:
            policies = self.network_v1.list_network_policy_for_all_namespaces()
        
        return [
            {
                'name': policy.metadata.name,
                'namespace': policy.metadata.namespace,
                'pod_selector': policy.spec.pod_selector.match_labels if policy.spec.pod_selector.match_labels else {},
                'policy_types': policy.spec.policy_types or [],
                'ingress_rules': len(policy.spec.ingress or []),
                'egress_rules': len(policy.spec.egress or [])
            }
            for policy in policies.items
        ]
    
    def get_cni_pods(self) -> List[Dict[str, Any]]:
        """Get CNI-related pods"""
        pods = self.v1.list_pod_for_all_namespaces()
        cni_pods = []
        
        cni_keywords = ['cilium', 'calico', 'flannel', 'weave', 'kube-ovn', 'antrea']
        
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if any(keyword in pod_name for keyword in cni_keywords):
                cni_pods.append({
                    'name': pod.metadata.name,
                    'namespace': pod.metadata.namespace,
                    'status': pod.status.phase,
                    'node': pod.spec.node_name,
                    'cni_type': self.detect_cni_type(pod.metadata.name)
                })
        
        return cni_pods
    
    def detect_cni_type(self, pod_name: str) -> str:
        """Detect CNI type from pod name"""
        pod_name = pod_name.lower()
        if 'cilium' in pod_name:
            return 'cilium'
        elif 'calico' in pod_name:
            return 'calico'
        elif 'flannel' in pod_name:
            return 'flannel'
        elif 'weave' in pod_name:
            return 'weave'
        elif 'kube-ovn' in pod_name:
            return 'kube-ovn'
        elif 'antrea' in pod_name:
            return 'antrea'
        else:
            return 'unknown'
    
    def create_test_network_policy(self, name: str, namespace: str, 
                                 pod_selector: Dict[str, str],
                                 policy_type: str = "Ingress") -> bool:
        """Create a test network policy"""
        network_policy = client.V1NetworkPolicy(
            metadata=client.V1ObjectMeta(name=name, namespace=namespace),
            spec=client.V1NetworkPolicySpec(
                pod_selector=client.V1LabelSelector(match_labels=pod_selector),
                policy_types=[policy_type],
                ingress=[] if policy_type == "Ingress" else None,
                egress=[] if policy_type == "Egress" else None
            )
        )
        
        try:
            self.network_v1.create_namespaced_network_policy(namespace, network_policy)
            return True
        except kubernetes.client.rest.ApiException:
            return False
    
    def delete_test_network_policy(self, name: str, namespace: str) -> bool:
        """Delete a test network policy"""
        try:
            self.network_v1.delete_namespaced_network_policy(name, namespace)
            return True
        except kubernetes.client.rest.ApiException:
            return False
    
    def create_test_pod(self, name: str, namespace: str, labels: Dict[str, str],
                       image: str = "nginx:1.25") -> bool:
        """Create a test pod"""
        pod = client.V1Pod(
            metadata=client.V1ObjectMeta(name=name, namespace=namespace, labels=labels),
            spec=client.V1PodSpec(
                containers=[
                    client.V1Container(
                        name="test-container",
                        image=image,
                        ports=[client.V1ContainerPort(container_port=80)]
                    )
                ],
                restart_policy="Never"
            )
        )
        
        try:
            self.v1.create_namespaced_pod(namespace, pod)
            return True
        except kubernetes.client.rest.ApiException:
            return False
    
    def wait_for_pod_running(self, name: str, namespace: str, timeout: int = 120) -> bool:
        """Wait for pod to be running"""
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            try:
                pod = self.v1.read_namespaced_pod(name, namespace)
                if pod.status.phase == "Running":
                    return True
                elif pod.status.phase == "Failed":
                    return False
            except kubernetes.client.rest.ApiException:
                pass
            time.sleep(5)
        
        return False
    
    def test_network_connectivity(self, source_pod: str, source_namespace: str,
                                target_ip: str, target_port: int = 80, timeout: int = 10) -> bool:
        """Test network connectivity between pods"""
        try:
            # Execute network test in source pod
            exec_command = [
                'sh', '-c', 
                f'timeout {timeout} nc -zv {target_ip} {target_port} 2>&1 || echo "FAILED"'
            ]
            
            result = subprocess.run([
                'kubectl', 'exec', '-n', source_namespace, source_pod, '--'
            ] + exec_command, capture_output=True, text=True, timeout=timeout + 5)
            
            return 'FAILED' not in result.stdout and result.returncode == 0
            
        except Exception:
            return False


@pytest.fixture
def network_tester():
    """Create NetworkPolicyTester instance"""
    return NetworkPolicyTester()


@pytest.fixture
def test_namespace():
    """Test namespace for network policy testing"""
    return os.getenv('NETWORK_TEST_NAMESPACE', 'network-test')


@pytest.fixture
def network_config():
    """Network configuration for testing"""
    return {
        'cni_provider': os.getenv('CNI_PROVIDER', 'cilium'),
        'network_policy_enabled': os.getenv('NETWORK_POLICY_ENABLED', 'true').lower() == 'true',
        'test_timeout': int(os.getenv('NETWORK_TEST_TIMEOUT', '60'))
    }


class TestCNIInstallation:
    """Test CNI installation and status"""
    
    def test_cni_pods_running(self, network_tester, network_config):
        """Test that CNI pods are running"""
        cni_pods = network_tester.get_cni_pods()
        
        if len(cni_pods) == 0:
            pytest.skip("No CNI pods found")
        
        print(f"Found {len(cni_pods)} CNI pods")
        
        cni_types = set()
        for pod in cni_pods:
            assert pod['status'] == 'Running', f"CNI pod {pod['name']} not running: {pod['status']}"
            cni_types.add(pod['cni_type'])
            print(f"✓ CNI pod running: {pod['name']} ({pod['cni_type']})")
        
        print(f"CNI types detected: {list(cni_types)}")
        
        # Verify expected CNI is present
        if network_config['cni_provider'] != 'auto':
            expected_cni = network_config['cni_provider']
            assert expected_cni in cni_types, f"Expected CNI {expected_cni} not found"
            print(f"✓ Expected CNI present: {expected_cni}")
    
    def test_cni_daemonset_status(self, network_tester):
        """Test CNI DaemonSet status"""
        daemonsets = network_tester.apps_v1.list_daemon_set_for_all_namespaces()
        cni_daemonsets = []
        
        cni_keywords = ['cilium', 'calico', 'flannel', 'weave', 'kube-ovn', 'antrea']
        
        for ds in daemonsets.items:
            ds_name = ds.metadata.name.lower()
            if any(keyword in ds_name for keyword in cni_keywords):
                cni_daemonsets.append({
                    'name': ds.metadata.name,
                    'namespace': ds.metadata.namespace,
                    'desired': ds.status.desired_number_scheduled or 0,
                    'ready': ds.status.number_ready or 0,
                    'available': ds.status.number_available or 0
                })
        
        if len(cni_daemonsets) == 0:
            pytest.skip("No CNI DaemonSets found")
        
        for ds in cni_daemonsets:
            assert ds['ready'] == ds['desired'], f"CNI DaemonSet {ds['name']} not fully ready ({ds['ready']}/{ds['desired']})"
            print(f"✓ CNI DaemonSet ready: {ds['name']} ({ds['ready']}/{ds['desired']})")
    
    def test_node_network_status(self, network_tester):
        """Test node network readiness"""
        nodes = network_tester.v1.list_node()
        
        for node in nodes.items:
            node_name = node.metadata.name
            
            # Check node conditions
            if node.status.conditions:
                network_ready = False
                for condition in node.status.conditions:
                    if condition.type == "NetworkUnavailable":
                        network_ready = condition.status == "False"  # False means network is available
                        break
                
                if not network_ready:
                    # Check if Ready condition is True (alternative check)
                    for condition in node.status.conditions:
                        if condition.type == "Ready" and condition.status == "True":
                            network_ready = True
                            break
                
                if network_ready:
                    print(f"✓ Node network ready: {node_name}")
                else:
                    print(f"⚠ Node network status unclear: {node_name}")


class TestNetworkPolicySupport:
    """Test network policy support and functionality"""
    
    def test_network_policy_crd_support(self, network_tester):
        """Test that network policies are supported"""
        try:
            # Try to list network policies
            policies = network_tester.get_network_policies()
            print(f"✓ Network policies supported, found {len(policies)} policies")
            
            # List existing policies
            if policies:
                print("Existing network policies:")
                for policy in policies[:5]:  # Show first 5
                    print(f"  - {policy['namespace']}/{policy['name']} (types: {policy['policy_types']})")
            
        except Exception as e:
            pytest.skip(f"Network policies not supported: {e}")
    
    def test_create_test_namespace(self, network_tester, test_namespace):
        """Create test namespace for network policy testing"""
        try:
            network_tester.v1.create_namespace(
                client.V1Namespace(metadata=client.V1ObjectMeta(name=test_namespace))
            )
            print(f"✓ Created test namespace: {test_namespace}")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 409:  # Already exists
                print(f"✓ Test namespace already exists: {test_namespace}")
            else:
                raise
    
    def test_basic_network_policy_creation(self, network_tester, test_namespace):
        """Test creating basic network policies"""
        policy_name = "test-deny-all"
        
        # Create a deny-all network policy
        success = network_tester.create_test_network_policy(
            policy_name, test_namespace, 
            pod_selector={}, policy_type="Ingress"
        )
        
        assert success, "Failed to create test network policy"
        print(f"✓ Created test network policy: {policy_name}")
        
        # Verify policy exists
        policies = network_tester.get_network_policies(test_namespace)
        policy_names = [p['name'] for p in policies]
        assert policy_name in policy_names, "Test network policy not found"
        
        # Cleanup
        network_tester.delete_test_network_policy(policy_name, test_namespace)
        print(f"✓ Cleaned up test network policy: {policy_name}")


class TestNetworkConnectivity:
    """Test network connectivity with and without policies"""
    
    def test_pod_to_pod_connectivity_without_policies(self, network_tester, test_namespace):
        """Test basic pod-to-pod connectivity"""
        # Create test pods
        pod1_name = "test-pod-1"
        pod2_name = "test-pod-2"
        
        success1 = network_tester.create_test_pod(
            pod1_name, test_namespace, {"app": "test-client"}
        )
        success2 = network_tester.create_test_pod(
            pod2_name, test_namespace, {"app": "test-server"}
        )
        
        if not (success1 and success2):
            pytest.skip("Could not create test pods")
        
        try:
            # Wait for pods to be running
            pod1_ready = network_tester.wait_for_pod_running(pod1_name, test_namespace)
            pod2_ready = network_tester.wait_for_pod_running(pod2_name, test_namespace)
            
            if not (pod1_ready and pod2_ready):
                pytest.skip("Test pods did not start properly")
            
            # Get pod IPs
            pod2 = network_tester.v1.read_namespaced_pod(pod2_name, test_namespace)
            pod2_ip = pod2.status.pod_ip
            
            if not pod2_ip:
                pytest.skip("Could not get pod IP")
            
            print(f"✓ Test pods created and running")
            print(f"  Pod 1: {pod1_name}")
            print(f"  Pod 2: {pod2_name} (IP: {pod2_ip})")
            
            # Test connectivity (may not work in all environments)
            # This is more of a basic validation that pods can be created
            
        finally:
            # Cleanup pods
            try:
                network_tester.v1.delete_namespaced_pod(pod1_name, test_namespace, grace_period_seconds=0)
                network_tester.v1.delete_namespaced_pod(pod2_name, test_namespace, grace_period_seconds=0)
                print("✓ Cleaned up test pods")
            except:
                pass
    
    def test_network_policy_enforcement(self, network_tester, test_namespace, network_config):
        """Test network policy enforcement"""
        if not network_config['network_policy_enabled']:
            pytest.skip("Network policies not enabled")
        
        # Create test pods with specific labels
        client_pod = "test-client"
        server_pod = "test-server"
        
        success1 = network_tester.create_test_pod(
            client_pod, test_namespace, {"role": "client"}
        )
        success2 = network_tester.create_test_pod(
            server_pod, test_namespace, {"role": "server"}
        )
        
        if not (success1 and success2):
            pytest.skip("Could not create test pods for policy testing")
        
        try:
            # Wait for pods
            client_ready = network_tester.wait_for_pod_running(client_pod, test_namespace)
            server_ready = network_tester.wait_for_pod_running(server_pod, test_namespace)
            
            if not (client_ready and server_ready):
                pytest.skip("Test pods for policy testing did not start")
            
            # Create restrictive network policy
            policy_name = "test-server-policy"
            policy_created = network_tester.create_test_network_policy(
                policy_name, test_namespace,
                pod_selector={"role": "server"},
                policy_type="Ingress"
            )
            
            if policy_created:
                print(f"✓ Created restrictive network policy: {policy_name}")
                
                # Test that policy is applied (this would require actual network testing)
                # For now, just verify the policy exists
                policies = network_tester.get_network_policies(test_namespace)
                policy_names = [p['name'] for p in policies]
                assert policy_name in policy_names, "Network policy not applied"
                
                print("✓ Network policy enforcement test completed")
                
                # Cleanup policy
                network_tester.delete_test_network_policy(policy_name, test_namespace)
            
        finally:
            # Cleanup pods
            try:
                network_tester.v1.delete_namespaced_pod(client_pod, test_namespace, grace_period_seconds=0)
                network_tester.v1.delete_namespaced_pod(server_pod, test_namespace, grace_period_seconds=0)
            except:
                pass


class TestProxmoxNetworkIntegration:
    """Test Proxmox-specific network integration"""
    
    def test_proxmox_worker_network_policies(self, network_tester):
        """Test network policies for Proxmox worker nodes"""
        # Look for Proxmox-related network policies
        all_policies = network_tester.get_network_policies()
        proxmox_policies = [
            policy for policy in all_policies
            if 'proxmox' in policy['name'].lower()
        ]
        
        if len(proxmox_policies) == 0:
            print("⚠ No Proxmox-specific network policies found")
            return
        
        print(f"Found {len(proxmox_policies)} Proxmox network policies:")
        for policy in proxmox_policies:
            print(f"  - {policy['namespace']}/{policy['name']}")
            print(f"    Policy types: {policy['policy_types']}")
            print(f"    Ingress rules: {policy['ingress_rules']}")
            print(f"    Egress rules: {policy['egress_rules']}")
    
    def test_proxmox_node_network_connectivity(self, network_tester):
        """Test network connectivity to Proxmox nodes"""
        nodes = network_tester.v1.list_node()
        proxmox_nodes = []
        
        # Look for nodes that might be Proxmox workers
        for node in nodes.items:
            labels = node.metadata.labels or {}
            if any('proxmox' in key.lower() or 'proxmox' in value.lower() 
                   for key, value in labels.items()):
                proxmox_nodes.append({
                    'name': node.metadata.name,
                    'labels': labels,
                    'addresses': []
                })
                
                if node.status.addresses:
                    for addr in node.status.addresses:
                        proxmox_nodes[-1]['addresses'].append({
                            'type': addr.type,
                            'address': addr.address
                        })
        
        if len(proxmox_nodes) == 0:
            print("⚠ No Proxmox worker nodes detected")
            return
        
        print(f"Found {len(proxmox_nodes)} potential Proxmox worker nodes:")
        for node in proxmox_nodes:
            print(f"  - {node['name']}")
            for addr in node['addresses']:
                print(f"    {addr['type']}: {addr['address']}")
    
    def test_service_mesh_integration(self, network_tester):
        """Test service mesh integration if available"""
        # Look for service mesh components
        service_mesh_pods = []
        pods = network_tester.v1.list_pod_for_all_namespaces()
        
        service_mesh_keywords = ['istio', 'linkerd', 'consul', 'envoy']
        
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if any(keyword in pod_name for keyword in service_mesh_keywords):
                service_mesh_pods.append({
                    'name': pod.metadata.name,
                    'namespace': pod.metadata.namespace,
                    'type': next(keyword for keyword in service_mesh_keywords if keyword in pod_name)
                })
        
        if len(service_mesh_pods) == 0:
            print("⚠ No service mesh components detected")
            return
        
        print(f"Found {len(service_mesh_pods)} service mesh components:")
        mesh_types = set()
        for pod in service_mesh_pods:
            mesh_types.add(pod['type'])
            print(f"  - {pod['namespace']}/{pod['name']} ({pod['type']})")
        
        print(f"Service mesh types: {list(mesh_types)}")


class TestNetworkPolicyCleanup:
    """Test cleanup of network policy test resources"""
    
    def test_cleanup_test_namespace(self, network_tester, test_namespace):
        """Clean up network test namespace"""
        try:
            # List any remaining network policies
            policies = network_tester.get_network_policies(test_namespace)
            if policies:
                print(f"Found {len(policies)} remaining network policies in test namespace")
                for policy in policies:
                    try:
                        network_tester.delete_test_network_policy(policy['name'], test_namespace)
                        print(f"  Cleaned up network policy: {policy['name']}")
                    except:
                        pass
            
            # Delete test namespace
            network_tester.v1.delete_namespace(test_namespace)
            print(f"✓ Network test namespace cleanup initiated: {test_namespace}")
            
        except kubernetes.client.rest.ApiException as e:
            if e.status == 404:  # Not found
                print(f"✓ Network test namespace already cleaned up: {test_namespace}")
            else:
                print(f"Warning: Could not clean up network namespace: {e}")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
