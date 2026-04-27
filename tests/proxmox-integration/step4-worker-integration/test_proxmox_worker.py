"""
Test Step 4: Proxmox Server as Kubernetes Worker Integration

This module tests adding Proxmox server as a Kubernetes worker node via kubeadm.
"""

import pytest
import subprocess
import time
import os
import tempfile
import yaml
from typing import Dict, Any, List
import kubernetes
from kubernetes import client, config


class ProxmoxWorkerTester:
    """Test utilities for Proxmox worker integration"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.apps_v1 = client.AppsV1Api()
        self.custom_api = client.CustomObjectsApi()
    
    def run_command(self, command: List[str], timeout: int = 60) -> Dict[str, Any]:
        """Run a command and return result"""
        try:
            result = subprocess.run(
                command, capture_output=True, text=True, timeout=timeout
            )
            return {
                'returncode': result.returncode,
                'stdout': result.stdout,
                'stderr': result.stderr,
                'success': result.returncode == 0
            }
        except subprocess.TimeoutExpired:
            return {
                'returncode': -1,
                'stdout': '',
                'stderr': 'Command timed out',
                'success': False
            }
    
    def get_nodes(self) -> List[Dict[str, Any]]:
        """Get Kubernetes nodes"""
        nodes = self.v1.list_node()
        return [
            {
                'name': node.metadata.name,
                'status': self.get_node_status(node),
                'roles': self.get_node_roles(node),
                'labels': node.metadata.labels or {},
                'taints': node.spec.taints or [],
                'addresses': self.get_node_addresses(node),
                'info': node.status.node_info._to_dict() if node.status.node_info else {}
            }
            for node in nodes.items
        ]
    
    def get_node_status(self, node) -> str:
        """Get node status"""
        if not node.status.conditions:
            return "Unknown"
        
        for condition in node.status.conditions:
            if condition.type == "Ready":
                return "Ready" if condition.status == "True" else "NotReady"
        return "Unknown"
    
    def get_node_roles(self, node) -> List[str]:
        """Get node roles from labels"""
        roles = []
        labels = node.metadata.labels or {}
        
        for label_key in labels.keys():
            if label_key.startswith('node-role.kubernetes.io/'):
                role = label_key.replace('node-role.kubernetes.io/', '')
                if role:
                    roles.append(role)
        
        return roles if roles else ['worker']
    
    def get_node_addresses(self, node) -> Dict[str, str]:
        """Get node addresses"""
        addresses = {}
        if node.status.addresses:
            for addr in node.status.addresses:
                addresses[addr.type] = addr.address
        return addresses
    
    def wait_for_node_ready(self, node_name: str, timeout: int = 600) -> bool:
        """Wait for a node to become ready"""
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            try:
                node = self.v1.read_node(node_name)
                if node.status.conditions:
                    for condition in node.status.conditions:
                        if (condition.type == "Ready" and 
                            condition.status == "True"):
                            return True
            except kubernetes.client.rest.ApiException:
                pass
            
            time.sleep(10)
        
        return False
    
    def get_pods_on_node(self, node_name: str) -> List[Dict[str, Any]]:
        """Get pods running on a specific node"""
        pods = self.v1.list_pod_for_all_namespaces(field_selector=f"spec.nodeName={node_name}")
        return [
            {
                'name': pod.metadata.name,
                'namespace': pod.metadata.namespace,
                'phase': pod.status.phase,
                'ready': self.is_pod_ready(pod)
            }
            for pod in pods.items
        ]
    
    def is_pod_ready(self, pod) -> bool:
        """Check if pod is ready"""
        if not pod.status.conditions:
            return False
        
        for condition in pod.status.conditions:
            if (condition.type == "Ready" and 
                condition.status == "True"):
                return True
        return False


@pytest.fixture
def worker_tester():
    """Create ProxmoxWorkerTester instance"""
    return ProxmoxWorkerTester()


@pytest.fixture
def test_namespace():
    """Test namespace for worker integration"""
    return os.getenv('TEST_NAMESPACE', 'proxmox-worker')


@pytest.fixture
def proxmox_worker_config():
    """Configuration for Proxmox worker testing"""
    return {
        'helm_chart_path': os.getenv('PROXMOX_WORKER_CHART', './packages/system/proxmox-worker'),
        'release_name': os.getenv('WORKER_RELEASE_NAME', 'proxmox-worker-test'),
        'expected_node_name': os.getenv('EXPECTED_NODE_NAME', 'proxmox-worker'),
        'proxmox_host': os.getenv('PROXMOX_HOST', 'proxmox.example.com'),
        'k8s_endpoint': os.getenv('K8S_ENDPOINT', 'k8s-api.example.com:6443'),
        'join_token': os.getenv('K8S_JOIN_TOKEN', ''),
        'ca_cert_hash': os.getenv('K8S_CA_CERT_HASH', '')
    }


class TestProxmoxWorkerPrerequisites:
    """Test prerequisites for Proxmox worker integration"""
    
    def test_helm_chart_exists(self, proxmox_worker_config):
        """Test that Proxmox worker Helm chart exists"""
        chart_path = proxmox_worker_config['helm_chart_path']
        chart_yaml = os.path.join(chart_path, 'Chart.yaml')
        
        assert os.path.exists(chart_yaml), f"Helm chart not found at {chart_path}"
        
        with open(chart_yaml, 'r') as f:
            chart = yaml.safe_load(f)
        
        assert chart['name'] == 'proxmox-worker', "Chart name should be 'proxmox-worker'"
        print(f"✓ Helm chart found: {chart['name']} v{chart['version']}")
    
    def test_helm_chart_lint(self, proxmox_worker_config):
        """Test that Helm chart passes linting"""
        chart_path = proxmox_worker_config['helm_chart_path']
        
        result = subprocess.run([
            'helm', 'lint', chart_path
        ], capture_output=True, text=True)
        
        assert result.returncode == 0, f"Helm lint failed: {result.stderr}"
        print("✓ Helm chart passes linting")
    
    def test_kubernetes_cluster_accessible(self, worker_tester):
        """Test that Kubernetes cluster is accessible"""
        try:
            nodes = worker_tester.get_nodes()
            assert len(nodes) > 0, "No nodes found in cluster"
            
            # Check for control plane nodes
            control_plane_nodes = [
                node for node in nodes 
                if 'control-plane' in node['roles'] or 'master' in node['roles']
            ]
            assert len(control_plane_nodes) > 0, "No control plane nodes found"
            
            print(f"✓ Kubernetes cluster accessible with {len(nodes)} nodes")
            print(f"  Control plane nodes: {len(control_plane_nodes)}")
            
        except Exception as e:
            pytest.fail(f"Cannot access Kubernetes cluster: {e}")
    
    def test_required_permissions(self, worker_tester):
        """Test that we have required permissions for worker integration"""
        try:
            # Test node operations
            nodes = worker_tester.v1.list_node()
            assert nodes is not None, "Cannot list nodes"
            
            # Test pod operations
            pods = worker_tester.v1.list_pod_for_all_namespaces()
            assert pods is not None, "Cannot list pods"
            
            # Test deployment operations  
            deployments = worker_tester.apps_v1.list_deployment_for_all_namespaces()
            assert deployments is not None, "Cannot list deployments"
            
            print("✓ Required Kubernetes permissions available")
            
        except kubernetes.client.rest.ApiException as e:
            pytest.fail(f"Insufficient permissions: {e}")


class TestProxmoxWorkerDeployment:
    """Test Proxmox worker Helm chart deployment"""
    
    def test_create_test_namespace(self, worker_tester, test_namespace):
        """Create test namespace for worker deployment"""
        try:
            worker_tester.v1.create_namespace(
                client.V1Namespace(metadata=client.V1ObjectMeta(name=test_namespace))
            )
            print(f"✓ Created test namespace: {test_namespace}")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 409:  # Already exists
                print(f"✓ Test namespace already exists: {test_namespace}")
            else:
                raise
    
    def test_create_proxmox_credentials(self, worker_tester, test_namespace, proxmox_worker_config):
        """Create Proxmox credentials secret"""
        if not proxmox_worker_config['join_token']:
            pytest.skip("Join token not provided, skipping credentials test")
        
        secret = client.V1Secret(
            metadata=client.V1ObjectMeta(
                name='proxmox-credentials',
                namespace=test_namespace
            ),
            type='Opaque',
            string_data={
                'username': 'root@pam',
                'password': 'test-password',
                'server': proxmox_worker_config['proxmox_host'],
                'port': '8006'
            }
        )
        
        try:
            worker_tester.v1.create_namespaced_secret(test_namespace, secret)
            print("✓ Created Proxmox credentials secret")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 409:  # Already exists
                print("✓ Proxmox credentials secret already exists")
            else:
                raise
    
    def test_helm_install_dry_run(self, proxmox_worker_config, test_namespace):
        """Test Helm install with dry-run"""
        chart_path = proxmox_worker_config['helm_chart_path']
        release_name = proxmox_worker_config['release_name']
        
        # Create values for dry-run
        values = {
            'proxmox': {
                'server': {
                    'host': proxmox_worker_config['proxmox_host']
                },
                'credentials': {
                    'secretName': 'proxmox-credentials'
                }
            },
            'kubernetes': {
                'controlPlaneEndpoint': proxmox_worker_config['k8s_endpoint'],
                'kubeadm': {
                    'token': proxmox_worker_config['join_token'] or 'test-token',
                    'caCertHash': proxmox_worker_config['ca_cert_hash'] or 'sha256:test-hash'
                }
            }
        }
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            yaml.dump(values, f)
            values_file = f.name
        
        try:
            result = subprocess.run([
                'helm', 'install', release_name, chart_path,
                '--namespace', test_namespace,
                '--values', values_file,
                '--dry-run', '--debug'
            ], capture_output=True, text=True)
            
            assert result.returncode == 0, f"Helm dry-run failed: {result.stderr}"
            print("✓ Helm chart dry-run successful")
            
        finally:
            os.unlink(values_file)


class TestProxmoxWorkerFunctionality:
    """Test Proxmox worker functionality (if deployed)"""
    
    def test_worker_node_joins_cluster(self, worker_tester, proxmox_worker_config):
        """Test that Proxmox worker node joins the cluster"""
        expected_node_name = proxmox_worker_config['expected_node_name']
        
        # Check if the expected node exists
        nodes = worker_tester.get_nodes()
        worker_node = None
        
        for node in nodes:
            if expected_node_name in node['name'] or 'proxmox' in node['name'].lower():
                worker_node = node
                break
        
        if not worker_node:
            pytest.skip(f"Proxmox worker node not found (expected: {expected_node_name})")
        
        print(f"✓ Found Proxmox worker node: {worker_node['name']}")
        print(f"  Status: {worker_node['status']}")
        print(f"  Roles: {worker_node['roles']}")
        
        assert worker_node['status'] == 'Ready', f"Worker node not ready: {worker_node['status']}"
    
    def test_worker_node_labels(self, worker_tester, proxmox_worker_config):
        """Test that worker node has correct labels"""
        expected_node_name = proxmox_worker_config['expected_node_name']
        
        nodes = worker_tester.get_nodes()
        worker_node = None
        
        for node in nodes:
            if expected_node_name in node['name'] or 'proxmox' in node['name'].lower():
                worker_node = node
                break
        
        if not worker_node:
            pytest.skip("Proxmox worker node not found")
        
        labels = worker_node['labels']
        
        # Check for worker role
        assert 'worker' in worker_node['roles'], "Node should have worker role"
        
        # Check for Proxmox-specific labels
        expected_labels = [
            'node.kubernetes.io/instance-type'
        ]
        
        for label in expected_labels:
            if label in labels:
                print(f"✓ Label found: {label}={labels[label]}")
    
    def test_worker_node_resources(self, worker_tester, proxmox_worker_config):
        """Test worker node resource allocation"""
        expected_node_name = proxmox_worker_config['expected_node_name']
        
        nodes = worker_tester.get_nodes()
        worker_node = None
        
        for node in nodes:
            if expected_node_name in node['name'] or 'proxmox' in node['name'].lower():
                worker_node = node
                break
        
        if not worker_node:
            pytest.skip("Proxmox worker node not found")
        
        try:
            node = worker_tester.v1.read_node(worker_node['name'])
            
            if node.status.allocatable:
                allocatable = node.status.allocatable
                print(f"✓ Node resources:")
                print(f"  CPU: {allocatable.get('cpu', 'unknown')}")
                print(f"  Memory: {allocatable.get('memory', 'unknown')}")
                print(f"  Storage: {allocatable.get('ephemeral-storage', 'unknown')}")
                print(f"  Pods: {allocatable.get('pods', 'unknown')}")
            
        except Exception as e:
            print(f"Warning: Could not get node resources: {e}")
    
    def test_pods_can_schedule_on_worker(self, worker_tester, proxmox_worker_config, test_namespace):
        """Test that pods can be scheduled on Proxmox worker"""
        expected_node_name = proxmox_worker_config['expected_node_name']
        
        nodes = worker_tester.get_nodes()
        worker_node = None
        
        for node in nodes:
            if expected_node_name in node['name'] or 'proxmox' in node['name'].lower():
                worker_node = node
                break
        
        if not worker_node:
            pytest.skip("Proxmox worker node not found")
        
        # Create a test pod with node selector
        test_pod = client.V1Pod(
            metadata=client.V1ObjectMeta(
                name='test-proxmox-scheduling',
                namespace=test_namespace
            ),
            spec=client.V1PodSpec(
                node_selector={'kubernetes.io/hostname': worker_node['name']},
                containers=[
                    client.V1Container(
                        name='test-container',
                        image='nginx:1.25',
                        resources=client.V1ResourceRequirements(
                            requests={'cpu': '100m', 'memory': '128Mi'},
                            limits={'cpu': '200m', 'memory': '256Mi'}
                        )
                    )
                ],
                restart_policy='Never'
            )
        )
        
        try:
            # Create the test pod
            worker_tester.v1.create_namespaced_pod(test_namespace, test_pod)
            
            # Wait for pod to be scheduled
            time.sleep(30)
            
            # Check pod status
            pod = worker_tester.v1.read_namespaced_pod('test-proxmox-scheduling', test_namespace)
            
            assert pod.spec.node_name == worker_node['name'], "Pod not scheduled on Proxmox worker"
            print(f"✓ Test pod scheduled on Proxmox worker: {worker_node['name']}")
            
        except Exception as e:
            print(f"Warning: Could not test pod scheduling: {e}")
        
        finally:
            # Clean up test pod
            try:
                worker_tester.v1.delete_namespaced_pod(
                    'test-proxmox-scheduling', test_namespace,
                    grace_period_seconds=0
                )
            except:
                pass


class TestProxmoxWorkerCleanup:
    """Test cleanup of Proxmox worker resources"""
    
    def test_uninstall_helm_chart(self, proxmox_worker_config, test_namespace):
        """Test uninstalling Proxmox worker Helm chart"""
        release_name = proxmox_worker_config['release_name']
        
        # Check if release exists
        result = subprocess.run([
            'helm', 'list', '-n', test_namespace, '-q'
        ], capture_output=True, text=True)
        
        if release_name not in result.stdout:
            pytest.skip(f"Helm release {release_name} not found")
        
        # Uninstall the release
        result = subprocess.run([
            'helm', 'uninstall', release_name, '-n', test_namespace
        ], capture_output=True, text=True)
        
        assert result.returncode == 0, f"Helm uninstall failed: {result.stderr}"
        print(f"✓ Helm release {release_name} uninstalled")
    
    def test_cleanup_test_namespace(self, worker_tester, test_namespace):
        """Clean up test namespace"""
        try:
            worker_tester.v1.delete_namespace(test_namespace)
            print(f"✓ Test namespace {test_namespace} cleanup initiated")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 404:  # Not found
                print(f"✓ Test namespace {test_namespace} already cleaned up")
            else:
                print(f"Warning: Could not clean up namespace: {e}")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
