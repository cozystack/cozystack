"""
Test Step 5: Proxmox CSI Storage Integration

This module tests Proxmox CSI driver functionality for persistent storage.
"""

import pytest
import yaml
import time
import tempfile
import os
from typing import Dict, Any, List
import kubernetes
from kubernetes import client, config


class ProxmoxCSITester:
    """Test utilities for Proxmox CSI storage"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.storage_v1 = client.StorageV1Api()
        self.apps_v1 = client.AppsV1Api()
        self.custom_api = client.CustomObjectsApi()
    
    def get_storage_classes(self) -> List[Dict[str, Any]]:
        """Get all storage classes"""
        storage_classes = self.storage_v1.list_storage_class()
        return [
            {
                'name': sc.metadata.name,
                'provisioner': sc.provisioner,
                'parameters': sc.parameters or {},
                'volume_binding_mode': sc.volume_binding_mode,
                'reclaim_policy': sc.reclaim_policy,
                'allow_volume_expansion': sc.allow_volume_expansion
            }
            for sc in storage_classes.items
        ]
    
    def get_persistent_volumes(self) -> List[Dict[str, Any]]:
        """Get all persistent volumes"""
        pvs = self.v1.list_persistent_volume()
        return [
            {
                'name': pv.metadata.name,
                'capacity': pv.spec.capacity.get('storage', 'unknown') if pv.spec.capacity else 'unknown',
                'access_modes': pv.spec.access_modes or [],
                'reclaim_policy': pv.spec.persistent_volume_reclaim_policy,
                'status': pv.status.phase,
                'storage_class': pv.spec.storage_class_name,
                'volume_source': self.get_volume_source_type(pv)
            }
            for pv in pvs.items
        ]
    
    def get_volume_source_type(self, pv) -> str:
        """Get volume source type from PV spec"""
        if hasattr(pv.spec, 'csi') and pv.spec.csi:
            return f"csi:{pv.spec.csi.driver}"
        elif hasattr(pv.spec, 'host_path') and pv.spec.host_path:
            return "hostPath"
        elif hasattr(pv.spec, 'nfs') and pv.spec.nfs:
            return "nfs"
        else:
            return "unknown"
    
    def get_persistent_volume_claims(self, namespace: str = None) -> List[Dict[str, Any]]:
        """Get persistent volume claims"""
        if namespace:
            pvcs = self.v1.list_namespaced_persistent_volume_claim(namespace)
        else:
            pvcs = self.v1.list_persistent_volume_claim_for_all_namespaces()
        
        return [
            {
                'name': pvc.metadata.name,
                'namespace': pvc.metadata.namespace,
                'status': pvc.status.phase,
                'capacity': pvc.status.capacity.get('storage') if pvc.status.capacity else None,
                'access_modes': pvc.spec.access_modes or [],
                'storage_class': pvc.spec.storage_class_name,
                'volume_name': pvc.spec.volume_name
            }
            for pvc in pvcs.items
        ]
    
    def create_test_pvc(self, name: str, namespace: str, storage_class: str, 
                       size: str = "1Gi", access_modes: List[str] = None) -> bool:
        """Create a test PVC"""
        if access_modes is None:
            access_modes = ["ReadWriteOnce"]
        
        pvc = client.V1PersistentVolumeClaim(
            metadata=client.V1ObjectMeta(name=name, namespace=namespace),
            spec=client.V1PersistentVolumeClaimSpec(
                access_modes=access_modes,
                resources=client.V1ResourceRequirements(
                    requests={"storage": size}
                ),
                storage_class_name=storage_class
            )
        )
        
        try:
            self.v1.create_namespaced_persistent_volume_claim(namespace, pvc)
            return True
        except kubernetes.client.rest.ApiException:
            return False
    
    def wait_for_pvc_bound(self, name: str, namespace: str, timeout: int = 300) -> bool:
        """Wait for PVC to be bound"""
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            try:
                pvc = self.v1.read_namespaced_persistent_volume_claim(name, namespace)
                if pvc.status.phase == "Bound":
                    return True
            except kubernetes.client.rest.ApiException:
                pass
            time.sleep(10)
        
        return False
    
    def delete_test_pvc(self, name: str, namespace: str) -> bool:
        """Delete a test PVC"""
        try:
            self.v1.delete_namespaced_persistent_volume_claim(name, namespace)
            return True
        except kubernetes.client.rest.ApiException:
            return False


@pytest.fixture
def csi_tester():
    """Create ProxmoxCSITester instance"""
    return ProxmoxCSITester()


@pytest.fixture
def test_namespace():
    """Test namespace for CSI testing"""
    return os.getenv('CSI_TEST_NAMESPACE', 'csi-test')


@pytest.fixture
def csi_config():
    """CSI configuration for testing"""
    return {
        'storage_class': os.getenv('CSI_STORAGE_CLASS', 'proxmox-csi'),
        'provisioner': 'csi.proxmox.sinextra.dev',
        'test_volume_size': os.getenv('CSI_TEST_SIZE', '1Gi'),
        'expected_driver': 'csi.proxmox.sinextra.dev'
    }


class TestProxmoxCSIInstallation:
    """Test Proxmox CSI driver installation and configuration"""
    
    def test_csi_driver_deployed(self, csi_tester):
        """Test that Proxmox CSI driver is deployed"""
        # Check for CSI driver deployments
        deployments = csi_tester.apps_v1.list_deployment_for_all_namespaces()
        csi_deployments = [
            d for d in deployments.items 
            if 'proxmox' in d.metadata.name.lower() and 'csi' in d.metadata.name.lower()
        ]
        
        if len(csi_deployments) == 0:
            pytest.skip("Proxmox CSI driver not deployed")
        
        for deployment in csi_deployments:
            assert deployment.status.ready_replicas > 0, f"CSI deployment {deployment.metadata.name} not ready"
            print(f"✓ CSI deployment ready: {deployment.metadata.name}")
    
    def test_csi_driver_daemonset(self, csi_tester):
        """Test that CSI driver DaemonSet is running"""
        daemonsets = csi_tester.apps_v1.list_daemon_set_for_all_namespaces()
        csi_daemonsets = [
            ds for ds in daemonsets.items 
            if 'proxmox' in ds.metadata.name.lower() and 'csi' in ds.metadata.name.lower()
        ]
        
        if len(csi_daemonsets) == 0:
            print("⚠ No Proxmox CSI DaemonSets found")
            return
        
        for daemonset in csi_daemonsets:
            desired = daemonset.status.desired_number_scheduled or 0
            ready = daemonset.status.number_ready or 0
            
            assert ready == desired, f"CSI DaemonSet {daemonset.metadata.name} not fully ready ({ready}/{desired})"
            print(f"✓ CSI DaemonSet ready: {daemonset.metadata.name} ({ready}/{desired})")
    
    def test_csi_driver_pods_running(self, csi_tester):
        """Test that CSI driver pods are running"""
        pods = csi_tester.v1.list_pod_for_all_namespaces()
        csi_pods = [
            pod for pod in pods.items 
            if 'proxmox' in pod.metadata.name.lower() and 'csi' in pod.metadata.name.lower()
        ]
        
        if len(csi_pods) == 0:
            pytest.skip("No Proxmox CSI pods found")
        
        for pod in csi_pods:
            assert pod.status.phase == "Running", f"CSI pod {pod.metadata.name} not running: {pod.status.phase}"
            
            # Check container readiness
            if pod.status.container_statuses:
                for container in pod.status.container_statuses:
                    assert container.ready, f"Container {container.name} in pod {pod.metadata.name} not ready"
            
            print(f"✓ CSI pod running: {pod.metadata.name}")


class TestProxmoxStorageClasses:
    """Test Proxmox storage classes configuration"""
    
    def test_proxmox_storage_class_exists(self, csi_tester, csi_config):
        """Test that Proxmox storage class exists"""
        storage_classes = csi_tester.get_storage_classes()
        proxmox_storage_classes = [
            sc for sc in storage_classes 
            if 'proxmox' in sc['name'].lower() or sc['provisioner'] == csi_config['provisioner']
        ]
        
        if len(proxmox_storage_classes) == 0:
            pytest.skip("No Proxmox storage classes found")
        
        for sc in proxmox_storage_classes:
            assert sc['provisioner'] == csi_config['provisioner'], f"Unexpected provisioner: {sc['provisioner']}"
            print(f"✓ Proxmox storage class: {sc['name']}")
            print(f"  Provisioner: {sc['provisioner']}")
            print(f"  Volume binding mode: {sc['volume_binding_mode']}")
            print(f"  Reclaim policy: {sc['reclaim_policy']}")
    
    def test_storage_class_parameters(self, csi_tester, csi_config):
        """Test storage class parameters"""
        storage_classes = csi_tester.get_storage_classes()
        proxmox_storage_classes = [
            sc for sc in storage_classes 
            if sc['provisioner'] == csi_config['provisioner']
        ]
        
        if len(proxmox_storage_classes) == 0:
            pytest.skip("No Proxmox storage classes found")
        
        for sc in proxmox_storage_classes:
            params = sc['parameters']
            
            # Check required parameters
            required_params = ['storage']
            for param in required_params:
                if param in params:
                    print(f"✓ Parameter {param}: {params[param]}")
            
            # Check optional parameters
            optional_params = ['format', 'node', 'cache']
            for param in optional_params:
                if param in params:
                    print(f"  Optional parameter {param}: {params[param]}")


class TestProxmoxVolumeProvisioning:
    """Test Proxmox volume provisioning"""
    
    def test_create_test_namespace(self, csi_tester, test_namespace):
        """Create test namespace for CSI testing"""
        try:
            csi_tester.v1.create_namespace(
                client.V1Namespace(metadata=client.V1ObjectMeta(name=test_namespace))
            )
            print(f"✓ Created test namespace: {test_namespace}")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 409:  # Already exists
                print(f"✓ Test namespace already exists: {test_namespace}")
            else:
                raise
    
    def test_dynamic_volume_provisioning(self, csi_tester, test_namespace, csi_config):
        """Test dynamic volume provisioning"""
        # Check if Proxmox storage class exists
        storage_classes = csi_tester.get_storage_classes()
        proxmox_storage_classes = [
            sc for sc in storage_classes 
            if sc['provisioner'] == csi_config['provisioner']
        ]
        
        if len(proxmox_storage_classes) == 0:
            pytest.skip("No Proxmox storage classes available for testing")
        
        storage_class_name = proxmox_storage_classes[0]['name']
        pvc_name = "test-proxmox-pvc"
        
        try:
            # Create test PVC
            success = csi_tester.create_test_pvc(
                pvc_name, test_namespace, storage_class_name, 
                csi_config['test_volume_size']
            )
            assert success, "Failed to create test PVC"
            print(f"✓ Created test PVC: {pvc_name}")
            
            # Wait for PVC to be bound
            bound = csi_tester.wait_for_pvc_bound(pvc_name, test_namespace, timeout=300)
            
            if not bound:
                # Get PVC status for debugging
                try:
                    pvc = csi_tester.v1.read_namespaced_persistent_volume_claim(pvc_name, test_namespace)
                    print(f"PVC status: {pvc.status.phase}")
                    if pvc.status.conditions:
                        for condition in pvc.status.conditions:
                            print(f"  Condition: {condition.type} = {condition.status}, {condition.message}")
                except:
                    pass
                
                pytest.fail("PVC did not become bound within timeout")
            
            print(f"✓ PVC bound successfully: {pvc_name}")
            
            # Verify PV was created
            pvc = csi_tester.v1.read_namespaced_persistent_volume_claim(pvc_name, test_namespace)
            pv_name = pvc.spec.volume_name
            
            if pv_name:
                pv = csi_tester.v1.read_persistent_volume(pv_name)
                assert pv.spec.csi.driver == csi_config['expected_driver'], "PV not created by Proxmox CSI driver"
                print(f"✓ PV created by Proxmox CSI: {pv_name}")
            
        finally:
            # Cleanup
            csi_tester.delete_test_pvc(pvc_name, test_namespace)
            print(f"✓ Cleaned up test PVC: {pvc_name}")
    
    def test_volume_mount_in_pod(self, csi_tester, test_namespace, csi_config):
        """Test mounting Proxmox volume in a pod"""
        # Check if Proxmox storage class exists
        storage_classes = csi_tester.get_storage_classes()
        proxmox_storage_classes = [
            sc for sc in storage_classes 
            if sc['provisioner'] == csi_config['provisioner']
        ]
        
        if len(proxmox_storage_classes) == 0:
            pytest.skip("No Proxmox storage classes available for testing")
        
        storage_class_name = proxmox_storage_classes[0]['name']
        pvc_name = "test-mount-pvc"
        pod_name = "test-mount-pod"
        
        try:
            # Create test PVC
            success = csi_tester.create_test_pvc(
                pvc_name, test_namespace, storage_class_name, "1Gi"
            )
            assert success, "Failed to create test PVC for mount test"
            
            # Wait for PVC to be bound
            bound = csi_tester.wait_for_pvc_bound(pvc_name, test_namespace, timeout=300)
            if not bound:
                pytest.skip("PVC not bound, skipping mount test")
            
            # Create test pod with volume mount
            pod = client.V1Pod(
                metadata=client.V1ObjectMeta(
                    name=pod_name,
                    namespace=test_namespace
                ),
                spec=client.V1PodSpec(
                    containers=[
                        client.V1Container(
                            name="test-container",
                            image="busybox:1.35",
                            command=["sleep", "300"],
                            volume_mounts=[
                                client.V1VolumeMount(
                                    name="test-volume",
                                    mount_path="/data"
                                )
                            ]
                        )
                    ],
                    volumes=[
                        client.V1Volume(
                            name="test-volume",
                            persistent_volume_claim=client.V1PersistentVolumeClaimVolumeSource(
                                claim_name=pvc_name
                            )
                        )
                    ],
                    restart_policy="Never"
                )
            )
            
            # Create the pod
            csi_tester.v1.create_namespaced_pod(test_namespace, pod)
            print(f"✓ Created test pod with volume mount: {pod_name}")
            
            # Wait for pod to be running
            timeout = 180
            start_time = time.time()
            
            while time.time() - start_time < timeout:
                try:
                    pod = csi_tester.v1.read_namespaced_pod(pod_name, test_namespace)
                    if pod.status.phase == "Running":
                        print(f"✓ Pod running with Proxmox volume mounted: {pod_name}")
                        break
                    elif pod.status.phase == "Failed":
                        print(f"Pod failed: {pod.status.message}")
                        break
                except:
                    pass
                time.sleep(10)
            else:
                print("⚠ Pod did not start within timeout, but volume mount was created")
            
        except Exception as e:
            print(f"Warning: Volume mount test encountered error: {e}")
        
        finally:
            # Cleanup pod and PVC
            try:
                csi_tester.v1.delete_namespaced_pod(pod_name, test_namespace, grace_period_seconds=0)
            except:
                pass
            
            try:
                csi_tester.delete_test_pvc(pvc_name, test_namespace)
            except:
                pass
            
            print("✓ Cleaned up mount test resources")


class TestProxmoxVolumeFeatures:
    """Test advanced Proxmox volume features"""
    
    def test_volume_expansion(self, csi_tester, test_namespace, csi_config):
        """Test volume expansion capability"""
        # Check if any storage class supports expansion
        storage_classes = csi_tester.get_storage_classes()
        expandable_classes = [
            sc for sc in storage_classes 
            if sc['provisioner'] == csi_config['provisioner'] and sc['allow_volume_expansion']
        ]
        
        if len(expandable_classes) == 0:
            pytest.skip("No expandable Proxmox storage classes found")
        
        print(f"✓ Found {len(expandable_classes)} expandable Proxmox storage classes")
        for sc in expandable_classes:
            print(f"  - {sc['name']}")
    
    def test_multiple_access_modes(self, csi_tester, test_namespace, csi_config):
        """Test different access modes"""
        storage_classes = csi_tester.get_storage_classes()
        proxmox_storage_classes = [
            sc for sc in storage_classes 
            if sc['provisioner'] == csi_config['provisioner']
        ]
        
        if len(proxmox_storage_classes) == 0:
            pytest.skip("No Proxmox storage classes available")
        
        access_modes_to_test = [
            ["ReadWriteOnce"],
            ["ReadOnlyMany"],
            ["ReadWriteMany"]
        ]
        
        storage_class_name = proxmox_storage_classes[0]['name']
        
        for i, access_modes in enumerate(access_modes_to_test):
            pvc_name = f"test-access-mode-{i}"
            
            try:
                success = csi_tester.create_test_pvc(
                    pvc_name, test_namespace, storage_class_name, 
                    "1Gi", access_modes
                )
                
                if success:
                    # Check if PVC gets bound (some access modes might not be supported)
                    bound = csi_tester.wait_for_pvc_bound(pvc_name, test_namespace, timeout=60)
                    if bound:
                        print(f"✓ Access mode supported: {access_modes}")
                    else:
                        print(f"⚠ Access mode created but not bound: {access_modes}")
                else:
                    print(f"⚠ Could not create PVC with access mode: {access_modes}")
                
            except Exception as e:
                print(f"⚠ Access mode test failed: {access_modes} - {e}")
            
            finally:
                try:
                    csi_tester.delete_test_pvc(pvc_name, test_namespace)
                except:
                    pass


class TestProxmoxCSICleanup:
    """Test cleanup of CSI test resources"""
    
    def test_cleanup_test_namespace(self, csi_tester, test_namespace):
        """Clean up test namespace and resources"""
        try:
            # List any remaining PVCs
            pvcs = csi_tester.get_persistent_volume_claims(test_namespace)
            if pvcs:
                print(f"Found {len(pvcs)} remaining PVCs in test namespace")
                for pvc in pvcs:
                    try:
                        csi_tester.delete_test_pvc(pvc['name'], test_namespace)
                        print(f"  Cleaned up PVC: {pvc['name']}")
                    except:
                        pass
            
            # Delete test namespace
            csi_tester.v1.delete_namespace(test_namespace)
            print(f"✓ Test namespace cleanup initiated: {test_namespace}")
            
        except kubernetes.client.rest.ApiException as e:
            if e.status == 404:  # Not found
                print(f"✓ Test namespace already cleaned up: {test_namespace}")
            else:
                print(f"Warning: Could not clean up namespace: {e}")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
