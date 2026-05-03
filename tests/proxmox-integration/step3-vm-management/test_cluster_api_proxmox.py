"""
Test Step 3: VM Creation and Management via Cluster API

This module tests Cluster API Proxmox provider functionality for VM lifecycle management.
"""

import pytest
import yaml
import subprocess
import time
import tempfile
import os
from typing import Dict, Any, List
import kubernetes
from kubernetes import client, config


class KubernetesClient:
    """Kubernetes client for Cluster API operations"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.custom_api = client.CustomObjectsApi()
        self.apps_v1 = client.AppsV1Api()
    
    def apply_yaml(self, yaml_content: str, namespace: str = "default") -> bool:
        """Apply YAML content to Kubernetes"""
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            f.write(yaml_content)
            f.flush()
            
            try:
                result = subprocess.run([
                    'kubectl', 'apply', '-f', f.name, '-n', namespace
                ], capture_output=True, text=True)
                return result.returncode == 0
            finally:
                os.unlink(f.name)
    
    def delete_yaml(self, yaml_content: str, namespace: str = "default") -> bool:
        """Delete resources from YAML content"""
        with tempfile.NamedTemporaryFile(mode='w', suffix='.yaml', delete=False) as f:
            f.write(yaml_content)
            f.flush()
            
            try:
                result = subprocess.run([
                    'kubectl', 'delete', '-f', f.name, '-n', namespace, '--ignore-not-found'
                ], capture_output=True, text=True)
                return result.returncode == 0
            finally:
                os.unlink(f.name)
    
    def get_custom_resource(self, group: str, version: str, plural: str, 
                          name: str, namespace: str = "default") -> Dict[str, Any]:
        """Get custom resource"""
        try:
            return self.custom_api.get_namespaced_custom_object(
                group=group, version=version, namespace=namespace, 
                plural=plural, name=name
            )
        except kubernetes.client.rest.ApiException:
            return None
    
    def list_custom_resources(self, group: str, version: str, plural: str, 
                            namespace: str = "default") -> List[Dict[str, Any]]:
        """List custom resources"""
        try:
            result = self.custom_api.list_namespaced_custom_object(
                group=group, version=version, namespace=namespace, plural=plural
            )
            return result.get('items', [])
        except kubernetes.client.rest.ApiException:
            return []
    
    def wait_for_condition(self, group: str, version: str, plural: str, 
                          name: str, condition_type: str, status: str = "True",
                          namespace: str = "default", timeout: int = 300) -> bool:
        """Wait for a condition to be met"""
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            resource = self.get_custom_resource(group, version, plural, name, namespace)
            if resource:
                conditions = resource.get('status', {}).get('conditions', [])
                for condition in conditions:
                    if (condition.get('type') == condition_type and 
                        condition.get('status') == status):
                        return True
            time.sleep(10)
        
        return False


@pytest.fixture
def k8s_client():
    """Create Kubernetes client"""
    return KubernetesClient()


@pytest.fixture
def test_namespace():
    """Test namespace for Cluster API resources"""
    return os.getenv('TEST_NAMESPACE', 'capi-test')


@pytest.fixture
def proxmox_config():
    """Proxmox configuration for Cluster API"""
    return {
        'server': os.getenv('PROXMOX_HOST', 'proxmox.example.com'),
        'username': os.getenv('PROXMOX_USERNAME', 'root@pam'),
        'password': os.getenv('PROXMOX_PASSWORD', ''),
        'node': os.getenv('PROXMOX_NODE', 'proxmox-node1'),
        'storage': os.getenv('PROXMOX_STORAGE', 'local'),
        'network_bridge': os.getenv('PROXMOX_BRIDGE', 'vmbr0'),
        'template_id': int(os.getenv('PROXMOX_TEMPLATE_ID', '1000'))
    }


class TestClusterAPIProxmoxProvider:
    """Test Cluster API Proxmox provider installation and configuration"""
    
    def test_capi_provider_installed(self, k8s_client):
        """Test that Cluster API core components are installed"""
        # Check for CAPI manager deployment
        deployments = k8s_client.apps_v1.list_deployment_for_all_namespaces()
        capi_deployments = [
            d for d in deployments.items 
            if 'cluster-api' in d.metadata.name
        ]
        
        assert len(capi_deployments) > 0, "No Cluster API deployments found"
        
        for deployment in capi_deployments:
            assert deployment.status.ready_replicas > 0, f"Deployment {deployment.metadata.name} not ready"
            print(f"✓ {deployment.metadata.name} deployment ready")
    
    def test_proxmox_provider_installed(self, k8s_client):
        """Test that Proxmox provider is installed"""
        # Check for Proxmox provider deployment
        deployments = k8s_client.apps_v1.list_deployment_for_all_namespaces()
        proxmox_deployments = [
            d for d in deployments.items 
            if 'proxmox' in d.metadata.name.lower()
        ]
        
        assert len(proxmox_deployments) > 0, "No Proxmox provider deployments found"
        
        for deployment in proxmox_deployments:
            assert deployment.status.ready_replicas > 0, f"Deployment {deployment.metadata.name} not ready"
            print(f"✓ {deployment.metadata.name} deployment ready")
    
    def test_crd_definitions_exist(self, k8s_client):
        """Test that required CRDs are installed"""
        required_crds = [
            'clusters.cluster.x-k8s.io',
            'machines.cluster.x-k8s.io',
            'machinesets.cluster.x-k8s.io',
            'proxmoxclusters.infrastructure.cluster.x-k8s.io',
            'proxmoxmachines.infrastructure.cluster.x-k8s.io'
        ]
        
        api_extensions = client.ApiextensionsV1Api()
        crds = api_extensions.list_custom_resource_definition()
        crd_names = [crd.metadata.name for crd in crds.items]
        
        for required_crd in required_crds:
            assert required_crd in crd_names, f"Required CRD {required_crd} not found"
            print(f"✓ CRD {required_crd} exists")


class TestProxmoxClusterCreation:
    """Test Proxmox cluster creation via Cluster API"""
    
    def test_create_proxmox_secret(self, k8s_client, test_namespace, proxmox_config):
        """Test creating Proxmox credentials secret"""
        secret_yaml = f"""
apiVersion: v1
kind: Secret
metadata:
  name: proxmox-credentials
  namespace: {test_namespace}
type: Opaque
stringData:
  username: {proxmox_config['username']}
  password: {proxmox_config['password']}
"""
        
        # Create namespace if it doesn't exist
        try:
            k8s_client.v1.create_namespace(
                client.V1Namespace(metadata=client.V1ObjectMeta(name=test_namespace))
            )
        except kubernetes.client.rest.ApiException as e:
            if e.status != 409:  # Ignore if namespace already exists
                raise
        
        assert k8s_client.apply_yaml(secret_yaml, test_namespace), "Failed to create Proxmox secret"
        
        # Verify secret exists
        secret = k8s_client.v1.read_namespaced_secret('proxmox-credentials', test_namespace)
        assert secret is not None, "Proxmox secret not found"
        print("✓ Proxmox credentials secret created")
    
    def test_create_proxmox_cluster(self, k8s_client, test_namespace, proxmox_config):
        """Test creating ProxmoxCluster resource"""
        cluster_yaml = f"""
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxCluster
metadata:
  name: test-proxmox-cluster
  namespace: {test_namespace}
spec:
  server: {proxmox_config['server']}
  credentialsRef:
    name: proxmox-credentials
  node: {proxmox_config['node']}
  allowedNodes:
    - {proxmox_config['node']}
"""
        
        assert k8s_client.apply_yaml(cluster_yaml, test_namespace), "Failed to create ProxmoxCluster"
        
        # Wait for cluster to be ready
        ready = k8s_client.wait_for_condition(
            'infrastructure.cluster.x-k8s.io', 'v1alpha1', 'proxmoxclusters',
            'test-proxmox-cluster', 'Ready', timeout=300, namespace=test_namespace
        )
        
        if not ready:
            # Get cluster status for debugging
            cluster = k8s_client.get_custom_resource(
                'infrastructure.cluster.x-k8s.io', 'v1alpha1', 'proxmoxclusters',
                'test-proxmox-cluster', test_namespace
            )
            print(f"Cluster status: {cluster.get('status', {})}")
        
        assert ready, "ProxmoxCluster did not become ready within timeout"
        print("✓ ProxmoxCluster ready")
    
    def test_create_cluster_api_cluster(self, k8s_client, test_namespace):
        """Test creating Cluster API Cluster resource"""
        cluster_yaml = f"""
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: test-cluster
  namespace: {test_namespace}
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
        - 10.244.0.0/16
    services:
      cidrBlocks:
        - 10.96.0.0/12
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
    kind: ProxmoxCluster
    name: test-proxmox-cluster
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: KubeadmControlPlane
    name: test-control-plane
"""
        
        assert k8s_client.apply_yaml(cluster_yaml, test_namespace), "Failed to create Cluster"
        
        # Verify cluster exists
        cluster = k8s_client.get_custom_resource(
            'cluster.x-k8s.io', 'v1beta1', 'clusters',
            'test-cluster', test_namespace
        )
        assert cluster is not None, "Cluster not found"
        print("✓ Cluster API Cluster created")


class TestProxmoxMachineCreation:
    """Test Proxmox machine creation via Cluster API"""
    
    def test_create_proxmox_machine_template(self, k8s_client, test_namespace, proxmox_config):
        """Test creating ProxmoxMachineTemplate"""
        template_yaml = f"""
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxMachineTemplate
metadata:
  name: test-machine-template
  namespace: {test_namespace}
spec:
  template:
    spec:
      sourceNode: {proxmox_config['node']}
      templateID: {proxmox_config['template_id']}
      storage: {proxmox_config['storage']}
      network:
        device: virtio
        bridge: {proxmox_config['network_bridge']}
      hardware:
        cpu: 2
        memory: 2048
        disk: 20
"""
        
        assert k8s_client.apply_yaml(template_yaml, test_namespace), "Failed to create ProxmoxMachineTemplate"
        
        # Verify template exists
        template = k8s_client.get_custom_resource(
            'infrastructure.cluster.x-k8s.io', 'v1alpha1', 'proxmoxmachinetemplates',
            'test-machine-template', test_namespace
        )
        assert template is not None, "ProxmoxMachineTemplate not found"
        print("✓ ProxmoxMachineTemplate created")
    
    def test_create_machine_deployment(self, k8s_client, test_namespace):
        """Test creating MachineDeployment for worker nodes"""
        deployment_yaml = f"""
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: test-worker-deployment
  namespace: {test_namespace}
spec:
  clusterName: test-cluster
  replicas: 1
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: test-cluster
      cluster.x-k8s.io/deployment-name: test-worker-deployment
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: test-cluster
        cluster.x-k8s.io/deployment-name: test-worker-deployment
    spec:
      clusterName: test-cluster
      version: v1.28.0
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: test-worker-bootstrap
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
        kind: ProxmoxMachineTemplate
        name: test-machine-template
"""
        
        assert k8s_client.apply_yaml(deployment_yaml, test_namespace), "Failed to create MachineDeployment"
        
        # Verify deployment exists
        deployment = k8s_client.get_custom_resource(
            'cluster.x-k8s.io', 'v1beta1', 'machinedeployments',
            'test-worker-deployment', test_namespace
        )
        assert deployment is not None, "MachineDeployment not found"
        print("✓ MachineDeployment created")
    
    def test_machine_provisioning(self, k8s_client, test_namespace):
        """Test that machines are being provisioned"""
        # Wait for machines to be created
        timeout = 600  # 10 minutes
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            machines = k8s_client.list_custom_resources(
                'cluster.x-k8s.io', 'v1beta1', 'machines', test_namespace
            )
            
            if len(machines) > 0:
                machine = machines[0]
                machine_name = machine['metadata']['name']
                
                # Check if machine has infrastructure reference
                infra_ref = machine.get('spec', {}).get('infrastructureRef')
                assert infra_ref is not None, "Machine has no infrastructure reference"
                
                print(f"✓ Machine {machine_name} created with infrastructure reference")
                
                # Check machine status
                status = machine.get('status', {})
                phase = status.get('phase', 'Unknown')
                print(f"Machine phase: {phase}")
                
                return True
            
            time.sleep(30)
        
        assert False, "No machines were created within timeout"


class TestProxmoxVMLifecycle:
    """Test VM lifecycle operations through Cluster API"""
    
    def test_vm_creation_status(self, k8s_client, test_namespace):
        """Test VM creation status through ProxmoxMachine"""
        proxmox_machines = k8s_client.list_custom_resources(
            'infrastructure.cluster.x-k8s.io', 'v1alpha1', 'proxmoxmachines', test_namespace
        )
        
        if len(proxmox_machines) == 0:
            pytest.skip("No ProxmoxMachines found to test")
        
        machine = proxmox_machines[0]
        machine_name = machine['metadata']['name']
        
        # Check machine status
        status = machine.get('status', {})
        conditions = status.get('conditions', [])
        
        print(f"ProxmoxMachine {machine_name} status:")
        for condition in conditions:
            print(f"  {condition.get('type')}: {condition.get('status')} - {condition.get('message', '')}")
        
        # Machine should have a VM ID assigned
        vm_id = status.get('vmID')
        if vm_id:
            print(f"✓ VM ID assigned: {vm_id}")
        else:
            print("VM ID not yet assigned")
    
    def test_cleanup_resources(self, k8s_client, test_namespace):
        """Clean up test resources"""
        cleanup_yaml = f"""
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: test-worker-deployment
  namespace: {test_namespace}
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: test-cluster
  namespace: {test_namespace}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxCluster
metadata:
  name: test-proxmox-cluster
  namespace: {test_namespace}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxMachineTemplate
metadata:
  name: test-machine-template
  namespace: {test_namespace}
---
apiVersion: v1
kind: Secret
metadata:
  name: proxmox-credentials
  namespace: {test_namespace}
"""
        
        # Delete resources (ignore errors)
        k8s_client.delete_yaml(cleanup_yaml, test_namespace)
        
        # Wait a bit for cleanup
        time.sleep(30)
        
        print("✓ Test resources cleanup initiated")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
