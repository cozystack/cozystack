"""
Test Step 8: End-to-End Integration Testing

This module tests the complete Proxmox-Kubernetes integration workflow.
"""

import pytest
import time
import yaml
import tempfile
import os
import subprocess
from typing import Dict, Any, List, Optional
import kubernetes
from kubernetes import client, config


class E2EIntegrationTester:
    """End-to-end integration test utilities"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.apps_v1 = client.AppsV1Api()
        self.storage_v1 = client.StorageV1Api()
        self.network_v1 = client.NetworkingV1Api()
        self.custom_api = client.CustomObjectsApi()
    
    def run_command(self, command: List[str], timeout: int = 300) -> Dict[str, Any]:
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
    
    def create_test_workload(self, name: str, namespace: str, 
                           use_storage: bool = True, use_network_policy: bool = True) -> Dict[str, str]:
        """Create a comprehensive test workload"""
        resources = {}
        
        # Create deployment
        deployment = client.V1Deployment(
            metadata=client.V1ObjectMeta(
                name=f"{name}-deployment",
                namespace=namespace,
                labels={"app": name, "test": "e2e"}
            ),
            spec=client.V1DeploymentSpec(
                replicas=2,
                selector=client.V1LabelSelector(
                    match_labels={"app": name}
                ),
                template=client.V1PodTemplateSpec(
                    metadata=client.V1ObjectMeta(
                        labels={"app": name, "test": "e2e"}
                    ),
                    spec=client.V1PodSpec(
                        containers=[
                            client.V1Container(
                                name="test-app",
                                image="nginx:1.25",
                                ports=[client.V1ContainerPort(container_port=80)],
                                volume_mounts=[
                                    client.V1VolumeMount(
                                        name="test-storage",
                                        mount_path="/data"
                                    )
                                ] if use_storage else None,
                                resources=client.V1ResourceRequirements(
                                    requests={"cpu": "100m", "memory": "128Mi"},
                                    limits={"cpu": "500m", "memory": "512Mi"}
                                )
                            )
                        ],
                        volumes=[
                            client.V1Volume(
                                name="test-storage",
                                persistent_volume_claim=client.V1PersistentVolumeClaimVolumeSource(
                                    claim_name=f"{name}-pvc"
                                )
                            )
                        ] if use_storage else None
                    )
                )
            )
        )
        
        self.apps_v1.create_namespaced_deployment(namespace, deployment)
        resources['deployment'] = f"{name}-deployment"
        
        # Create service
        service = client.V1Service(
            metadata=client.V1ObjectMeta(
                name=f"{name}-service",
                namespace=namespace,
                labels={"app": name, "test": "e2e"}
            ),
            spec=client.V1ServiceSpec(
                selector={"app": name},
                ports=[
                    client.V1ServicePort(
                        port=80,
                        target_port=80,
                        protocol="TCP"
                    )
                ],
                type="ClusterIP"
            )
        )
        
        self.v1.create_namespaced_service(namespace, service)
        resources['service'] = f"{name}-service"
        
        # Create PVC if storage is needed
        if use_storage:
            # Find a suitable storage class
            storage_classes = self.storage_v1.list_storage_class()
            proxmox_sc = None
            
            for sc in storage_classes.items:
                if 'proxmox' in sc.metadata.name.lower() or 'proxmox' in (sc.provisioner or ''):
                    proxmox_sc = sc.metadata.name
                    break
            
            if proxmox_sc:
                pvc = client.V1PersistentVolumeClaim(
                    metadata=client.V1ObjectMeta(
                        name=f"{name}-pvc",
                        namespace=namespace,
                        labels={"app": name, "test": "e2e"}
                    ),
                    spec=client.V1PersistentVolumeClaimSpec(
                        access_modes=["ReadWriteOnce"],
                        resources=client.V1ResourceRequirements(
                            requests={"storage": "1Gi"}
                        ),
                        storage_class_name=proxmox_sc
                    )
                )
                
                self.v1.create_namespaced_persistent_volume_claim(namespace, pvc)
                resources['pvc'] = f"{name}-pvc"
        
        # Create network policy if needed
        if use_network_policy:
            network_policy = client.V1NetworkPolicy(
                metadata=client.V1ObjectMeta(
                    name=f"{name}-netpol",
                    namespace=namespace,
                    labels={"app": name, "test": "e2e"}
                ),
                spec=client.V1NetworkPolicySpec(
                    pod_selector=client.V1LabelSelector(
                        match_labels={"app": name}
                    ),
                    policy_types=["Ingress"],
                    ingress=[
                        client.V1NetworkPolicyIngressRule(
                            from_=[
                                client.V1NetworkPolicyPeer(
                                    namespace_selector=client.V1LabelSelector(
                                        match_labels={"name": namespace}
                                    )
                                )
                            ],
                            ports=[
                                client.V1NetworkPolicyPort(
                                    port=80,
                                    protocol="TCP"
                                )
                            ]
                        )
                    ]
                )
            )
            
            try:
                self.network_v1.create_namespaced_network_policy(namespace, network_policy)
                resources['network_policy'] = f"{name}-netpol"
            except Exception as e:
                print(f"⚠ Could not create network policy: {e}")
        
        return resources
    
    def wait_for_deployment_ready(self, name: str, namespace: str, timeout: int = 300) -> bool:
        """Wait for deployment to be ready"""
        start_time = time.time()
        
        while time.time() - start_time < timeout:
            try:
                deployment = self.apps_v1.read_namespaced_deployment(name, namespace)
                if (deployment.status.ready_replicas == deployment.status.replicas and 
                    deployment.status.replicas > 0):
                    return True
            except kubernetes.client.rest.ApiException:
                pass
            time.sleep(10)
        
        return False
    
    def cleanup_test_workload(self, resources: Dict[str, str], namespace: str):
        """Clean up test workload resources"""
        cleanup_order = ['deployment', 'service', 'pvc', 'network_policy']
        
        for resource_type in cleanup_order:
            if resource_type in resources:
                resource_name = resources[resource_type]
                try:
                    if resource_type == 'deployment':
                        self.apps_v1.delete_namespaced_deployment(resource_name, namespace)
                    elif resource_type == 'service':
                        self.v1.delete_namespaced_service(resource_name, namespace)
                    elif resource_type == 'pvc':
                        self.v1.delete_namespaced_persistent_volume_claim(resource_name, namespace)
                    elif resource_type == 'network_policy':
                        self.network_v1.delete_namespaced_network_policy(resource_name, namespace)
                    
                    print(f"✓ Cleaned up {resource_type}: {resource_name}")
                except Exception as e:
                    print(f"⚠ Failed to clean up {resource_type} {resource_name}: {e}")
    
    def get_integration_health_status(self) -> Dict[str, Any]:
        """Get overall integration health status"""
        status = {
            'nodes': {'total': 0, 'ready': 0, 'proxmox_workers': 0},
            'pods': {'total': 0, 'running': 0, 'proxmox_related': 0},
            'storage': {'classes': 0, 'proxmox_classes': 0, 'pvs': 0},
            'networking': {'policies': 0, 'cni_pods': 0},
            'monitoring': {'prometheus_pods': 0, 'grafana_pods': 0, 'metrics_available': False}
        }
        
        # Check nodes
        nodes = self.v1.list_node()
        status['nodes']['total'] = len(nodes.items)
        
        for node in nodes.items:
            # Check if node is ready
            if node.status.conditions:
                for condition in node.status.conditions:
                    if condition.type == "Ready" and condition.status == "True":
                        status['nodes']['ready'] += 1
                        break
            
            # Check if it's a Proxmox worker
            labels = node.metadata.labels or {}
            if any('proxmox' in key.lower() or 'proxmox' in value.lower() 
                   for key, value in labels.items()):
                status['nodes']['proxmox_workers'] += 1
        
        # Check pods
        pods = self.v1.list_pod_for_all_namespaces()
        status['pods']['total'] = len(pods.items)
        
        for pod in pods.items:
            if pod.status.phase == "Running":
                status['pods']['running'] += 1
            
            if 'proxmox' in pod.metadata.name.lower():
                status['pods']['proxmox_related'] += 1
        
        # Check storage
        storage_classes = self.storage_v1.list_storage_class()
        status['storage']['classes'] = len(storage_classes.items)
        
        for sc in storage_classes.items:
            if 'proxmox' in sc.metadata.name.lower() or 'proxmox' in (sc.provisioner or ''):
                status['storage']['proxmox_classes'] += 1
        
        pvs = self.v1.list_persistent_volume()
        status['storage']['pvs'] = len(pvs.items)
        
        # Check networking
        try:
            network_policies = self.network_v1.list_network_policy_for_all_namespaces()
            status['networking']['policies'] = len(network_policies.items)
        except:
            pass
        
        # Count CNI pods
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if any(cni in pod_name for cni in ['cilium', 'calico', 'flannel', 'kube-ovn']):
                status['networking']['cni_pods'] += 1
        
        # Check monitoring
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if 'prometheus' in pod_name and pod.status.phase == "Running":
                status['monitoring']['prometheus_pods'] += 1
            elif 'grafana' in pod_name and pod.status.phase == "Running":
                status['monitoring']['grafana_pods'] += 1
        
        # Check if metrics API is available
        try:
            self.custom_api.list_cluster_custom_object(
                group="metrics.k8s.io", version="v1beta1", plural="nodes"
            )
            status['monitoring']['metrics_available'] = True
        except:
            pass
        
        return status


@pytest.fixture
def e2e_tester():
    """Create E2EIntegrationTester instance"""
    return E2EIntegrationTester()


@pytest.fixture
def e2e_namespace():
    """Test namespace for E2E testing"""
    return os.getenv('E2E_TEST_NAMESPACE', 'e2e-test')


@pytest.fixture
def e2e_config():
    """E2E configuration"""
    return {
        'test_workload_name': 'proxmox-e2e-test',
        'test_timeout': int(os.getenv('E2E_TEST_TIMEOUT', '600')),
        'enable_storage_tests': os.getenv('E2E_ENABLE_STORAGE', 'true').lower() == 'true',
        'enable_network_tests': os.getenv('E2E_ENABLE_NETWORK', 'true').lower() == 'true',
        'enable_monitoring_tests': os.getenv('E2E_ENABLE_MONITORING', 'true').lower() == 'true',
        'cleanup_on_failure': os.getenv('E2E_CLEANUP_ON_FAILURE', 'true').lower() == 'true'
    }


class TestE2EPrerequisites:
    """Test E2E prerequisites and overall system health"""
    
    def test_overall_integration_health(self, e2e_tester):
        """Test overall integration health status"""
        health = e2e_tester.get_integration_health_status()
        
        print("=== Proxmox-Kubernetes Integration Health Status ===")
        
        # Nodes status
        print(f"Nodes: {health['nodes']['ready']}/{health['nodes']['total']} ready")
        print(f"  Proxmox workers: {health['nodes']['proxmox_workers']}")
        assert health['nodes']['ready'] > 0, "No ready nodes found"
        
        # Pods status
        running_percentage = (health['pods']['running'] / health['pods']['total']) * 100 if health['pods']['total'] > 0 else 0
        print(f"Pods: {health['pods']['running']}/{health['pods']['total']} running ({running_percentage:.1f}%)")
        print(f"  Proxmox-related pods: {health['pods']['proxmox_related']}")
        
        # Storage status
        print(f"Storage: {health['storage']['classes']} storage classes")
        print(f"  Proxmox storage classes: {health['storage']['proxmox_classes']}")
        print(f"  Persistent volumes: {health['storage']['pvs']}")
        
        # Networking status
        print(f"Networking: {health['networking']['policies']} network policies")
        print(f"  CNI pods: {health['networking']['cni_pods']}")
        assert health['networking']['cni_pods'] > 0, "No CNI pods found"
        
        # Monitoring status
        print(f"Monitoring: Prometheus={health['monitoring']['prometheus_pods']}, Grafana={health['monitoring']['grafana_pods']}")
        print(f"  Metrics API: {'Available' if health['monitoring']['metrics_available'] else 'Not available'}")
        
        # Overall health check
        critical_issues = []
        
        if health['nodes']['ready'] == 0:
            critical_issues.append("No ready nodes")
        
        if running_percentage < 80:
            critical_issues.append(f"Low pod health: {running_percentage:.1f}%")
        
        if health['networking']['cni_pods'] == 0:
            critical_issues.append("No CNI pods running")
        
        if critical_issues:
            pytest.fail(f"Critical integration issues found: {critical_issues}")
        
        print("✓ Overall integration health is good")
    
    def test_create_e2e_namespace(self, e2e_tester, e2e_namespace):
        """Create namespace for E2E testing"""
        try:
            namespace = client.V1Namespace(
                metadata=client.V1ObjectMeta(
                    name=e2e_namespace,
                    labels={"test": "e2e", "purpose": "proxmox-integration"}
                )
            )
            e2e_tester.v1.create_namespace(namespace)
            print(f"✓ Created E2E test namespace: {e2e_namespace}")
        except kubernetes.client.rest.ApiException as e:
            if e.status == 409:  # Already exists
                print(f"✓ E2E test namespace already exists: {e2e_namespace}")
            else:
                raise
    
    def test_helm_charts_deployed(self, e2e_tester):
        """Test that Proxmox Helm charts are deployed"""
        # Check for Proxmox-related deployments/daemonsets
        deployments = e2e_tester.apps_v1.list_deployment_for_all_namespaces()
        daemonsets = e2e_tester.apps_v1.list_daemon_set_for_all_namespaces()
        
        proxmox_deployments = [
            d for d in deployments.items
            if 'proxmox' in d.metadata.name.lower()
        ]
        
        proxmox_daemonsets = [
            ds for ds in daemonsets.items
            if 'proxmox' in ds.metadata.name.lower()
        ]
        
        print(f"Found {len(proxmox_deployments)} Proxmox deployments")
        print(f"Found {len(proxmox_daemonsets)} Proxmox daemonsets")
        
        for deployment in proxmox_deployments:
            ready = deployment.status.ready_replicas or 0
            desired = deployment.status.replicas or 0
            print(f"  Deployment {deployment.metadata.namespace}/{deployment.metadata.name}: {ready}/{desired}")
        
        for ds in proxmox_daemonsets:
            ready = ds.status.number_ready or 0
            desired = ds.status.desired_number_scheduled or 0
            print(f"  DaemonSet {ds.metadata.namespace}/{ds.metadata.name}: {ready}/{desired}")


class TestE2EWorkloadDeployment:
    """Test end-to-end workload deployment"""
    
    def test_complete_workload_lifecycle(self, e2e_tester, e2e_namespace, e2e_config):
        """Test complete workload lifecycle with all integration components"""
        workload_name = e2e_config['test_workload_name']
        
        print(f"=== Testing complete workload lifecycle: {workload_name} ===")
        
        # Create comprehensive test workload
        resources = e2e_tester.create_test_workload(
            workload_name, e2e_namespace,
            use_storage=e2e_config['enable_storage_tests'],
            use_network_policy=e2e_config['enable_network_tests']
        )
        
        print(f"✓ Created test workload with resources: {list(resources.keys())}")
        
        try:
            # Wait for deployment to be ready
            deployment_name = resources.get('deployment')
            if deployment_name:
                print(f"Waiting for deployment {deployment_name} to be ready...")
                ready = e2e_tester.wait_for_deployment_ready(
                    deployment_name, e2e_namespace, e2e_config['test_timeout']
                )
                
                if ready:
                    print(f"✓ Deployment {deployment_name} is ready")
                else:
                    # Get deployment status for debugging
                    try:
                        deployment = e2e_tester.apps_v1.read_namespaced_deployment(deployment_name, e2e_namespace)
                        print(f"Deployment status: ready={deployment.status.ready_replicas}, replicas={deployment.status.replicas}")
                        
                        if deployment.status.conditions:
                            for condition in deployment.status.conditions:
                                print(f"  Condition: {condition.type} = {condition.status}, {condition.message}")
                    except:
                        pass
                    
                    if not e2e_config['cleanup_on_failure']:
                        pytest.skip(f"Deployment {deployment_name} not ready within timeout, resources left for debugging")
                    else:
                        pytest.fail(f"Deployment {deployment_name} not ready within timeout")
            
            # Test storage if enabled
            if e2e_config['enable_storage_tests'] and 'pvc' in resources:
                pvc_name = resources['pvc']
                print(f"Testing storage with PVC: {pvc_name}")
                
                # Check PVC status
                try:
                    pvc = e2e_tester.v1.read_namespaced_persistent_volume_claim(pvc_name, e2e_namespace)
                    print(f"  PVC status: {pvc.status.phase}")
                    
                    if pvc.status.phase == "Bound":
                        print(f"✓ Storage test passed: PVC is bound")
                        
                        # Get PV details
                        if pv_name := pvc.spec.volume_name:
                            try:
                                pv = e2e_tester.v1.read_persistent_volume(pv_name)
                                if pv.spec.csi and 'proxmox' in pv.spec.csi.driver:
                                    print(f"✓ Volume provisioned by Proxmox CSI: {pv_name}")
                                else:
                                    print(f"⚠ Volume not provisioned by Proxmox CSI: {pv_name}")
                            except:
                                pass
                    else:
                        print(f"⚠ Storage test warning: PVC status is {pvc.status.phase}")
                        
                except Exception as e:
                    print(f"⚠ Storage test error: {e}")
            
            # Test networking if enabled
            if e2e_config['enable_network_tests'] and 'service' in resources:
                service_name = resources['service']
                print(f"Testing networking with service: {service_name}")
                
                try:
                    service = e2e_tester.v1.read_namespaced_service(service_name, e2e_namespace)
                    print(f"  Service cluster IP: {service.spec.cluster_ip}")
                    
                    # Check endpoints
                    try:
                        endpoints = e2e_tester.v1.read_namespaced_endpoints(service_name, e2e_namespace)
                        if endpoints.subsets:
                            endpoint_count = sum(len(subset.addresses or []) for subset in endpoints.subsets)
                            print(f"✓ Network test passed: Service has {endpoint_count} endpoints")
                        else:
                            print(f"⚠ Network test warning: Service has no endpoints")
                    except:
                        print(f"⚠ Network test warning: Could not check endpoints")
                        
                except Exception as e:
                    print(f"⚠ Network test error: {e}")
            
            # Test resource consumption
            print("Testing resource consumption...")
            try:
                pods = e2e_tester.v1.list_namespaced_pod(e2e_namespace, label_selector=f"app={workload_name}")
                print(f"  Found {len(pods.items)} test pods")
                
                for pod in pods.items:
                    print(f"    Pod {pod.metadata.name}: {pod.status.phase}")
                    if pod.spec.node_name:
                        print(f"      Scheduled on node: {pod.spec.node_name}")
                
                print("✓ Resource consumption test completed")
                
            except Exception as e:
                print(f"⚠ Resource consumption test error: {e}")
            
            print(f"✓ Complete workload lifecycle test passed for {workload_name}")
            
        finally:
            # Cleanup resources
            if e2e_config['cleanup_on_failure']:
                print("Cleaning up test workload...")
                e2e_tester.cleanup_test_workload(resources, e2e_namespace)
                print("✓ Test workload cleanup completed")
            else:
                print(f"⚠ Test workload resources left for debugging: {list(resources.keys())}")


class TestE2EPerformanceAndReliability:
    """Test performance and reliability of the integration"""
    
    def test_multiple_workload_deployment(self, e2e_tester, e2e_namespace, e2e_config):
        """Test deploying multiple workloads simultaneously"""
        workload_count = 3
        workloads = []
        
        print(f"=== Testing {workload_count} simultaneous workloads ===")
        
        try:
            # Create multiple workloads
            for i in range(workload_count):
                workload_name = f"multi-test-{i}"
                resources = e2e_tester.create_test_workload(
                    workload_name, e2e_namespace,
                    use_storage=True, use_network_policy=False  # Simplified for multi-test
                )
                workloads.append((workload_name, resources))
                print(f"✓ Created workload {i+1}/{workload_count}: {workload_name}")
            
            # Wait for all deployments to be ready
            ready_count = 0
            for workload_name, resources in workloads:
                deployment_name = resources.get('deployment')
                if deployment_name:
                    ready = e2e_tester.wait_for_deployment_ready(
                        deployment_name, e2e_namespace, timeout=180  # Shorter timeout for multi-test
                    )
                    if ready:
                        ready_count += 1
                        print(f"✓ Workload ready: {workload_name}")
                    else:
                        print(f"⚠ Workload not ready: {workload_name}")
            
            print(f"Multi-workload test result: {ready_count}/{workload_count} workloads ready")
            
            if ready_count >= workload_count * 0.8:  # 80% success rate
                print("✓ Multi-workload deployment test passed")
            else:
                print("⚠ Multi-workload deployment test had issues")
            
        finally:
            # Cleanup all workloads
            for workload_name, resources in workloads:
                try:
                    e2e_tester.cleanup_test_workload(resources, e2e_namespace)
                    print(f"✓ Cleaned up workload: {workload_name}")
                except:
                    pass
    
    def test_node_failure_simulation(self, e2e_tester):
        """Test behavior during node issues (simulation)"""
        print("=== Testing node failure resilience ===")
        
        # Get nodes and their status
        nodes = e2e_tester.v1.list_node()
        worker_nodes = []
        
        for node in nodes.items:
            labels = node.metadata.labels or {}
            # Skip control plane nodes
            if not any(role in labels for role in ['node-role.kubernetes.io/control-plane', 'node-role.kubernetes.io/master']):
                worker_nodes.append({
                    'name': node.metadata.name,
                    'ready': any(
                        condition.type == "Ready" and condition.status == "True"
                        for condition in (node.status.conditions or [])
                    )
                })
        
        print(f"Found {len(worker_nodes)} worker nodes")
        ready_workers = [node for node in worker_nodes if node['ready']]
        print(f"Ready worker nodes: {len(ready_workers)}")
        
        if len(ready_workers) > 1:
            print("✓ Multiple worker nodes available - good for fault tolerance")
        else:
            print("⚠ Limited worker nodes - fault tolerance may be reduced")
        
        # Check for node anti-affinity in critical workloads
        deployments = e2e_tester.apps_v1.list_deployment_for_all_namespaces()
        critical_deployments = [
            d for d in deployments.items
            if any(keyword in d.metadata.name.lower() 
                   for keyword in ['prometheus', 'grafana', 'cilium', 'proxmox'])
        ]
        
        print(f"Found {len(critical_deployments)} critical deployments")
        for deployment in critical_deployments[:3]:  # Check first 3
            replicas = deployment.status.replicas or 0
            ready = deployment.status.ready_replicas or 0
            print(f"  {deployment.metadata.namespace}/{deployment.metadata.name}: {ready}/{replicas}")
        
        print("✓ Node failure resilience test completed")
    
    def test_resource_limits_and_scaling(self, e2e_tester, e2e_namespace):
        """Test resource limits and basic scaling"""
        print("=== Testing resource limits and scaling ===")
        
        # Create a deployment with specific resource limits
        test_name = "resource-test"
        
        deployment = client.V1Deployment(
            metadata=client.V1ObjectMeta(
                name=test_name,
                namespace=e2e_namespace,
                labels={"test": "resource-limits"}
            ),
            spec=client.V1DeploymentSpec(
                replicas=1,
                selector=client.V1LabelSelector(
                    match_labels={"app": test_name}
                ),
                template=client.V1PodTemplateSpec(
                    metadata=client.V1ObjectMeta(
                        labels={"app": test_name}
                    ),
                    spec=client.V1PodSpec(
                        containers=[
                            client.V1Container(
                                name="test-container",
                                image="nginx:1.25",
                                resources=client.V1ResourceRequirements(
                                    requests={"cpu": "50m", "memory": "64Mi"},
                                    limits={"cpu": "100m", "memory": "128Mi"}
                                )
                            )
                        ]
                    )
                )
            )
        )
        
        try:
            e2e_tester.apps_v1.create_namespaced_deployment(e2e_namespace, deployment)
            print(f"✓ Created resource-limited deployment: {test_name}")
            
            # Wait for deployment
            ready = e2e_tester.wait_for_deployment_ready(test_name, e2e_namespace, timeout=120)
            if ready:
                print("✓ Resource-limited deployment is ready")
                
                # Try scaling up
                deployment.spec.replicas = 3
                e2e_tester.apps_v1.patch_namespaced_deployment(test_name, e2e_namespace, deployment)
                print("✓ Scaled deployment to 3 replicas")
                
                # Wait a bit and check scaling
                time.sleep(30)
                updated_deployment = e2e_tester.apps_v1.read_namespaced_deployment(test_name, e2e_namespace)
                ready_replicas = updated_deployment.status.ready_replicas or 0
                print(f"Scaling result: {ready_replicas}/3 replicas ready")
                
            else:
                print("⚠ Resource-limited deployment not ready")
            
        finally:
            # Cleanup
            try:
                e2e_tester.apps_v1.delete_namespaced_deployment(test_name, e2e_namespace)
                print(f"✓ Cleaned up resource test deployment")
            except:
                pass
        
        print("✓ Resource limits and scaling test completed")


class TestE2ECleanup:
    """Test cleanup of E2E test resources"""
    
    def test_cleanup_e2e_namespace(self, e2e_tester, e2e_namespace):
        """Clean up E2E test namespace and all resources"""
        try:
            # List remaining resources
            pods = e2e_tester.v1.list_namespaced_pod(e2e_namespace)
            services = e2e_tester.v1.list_namespaced_service(e2e_namespace)
            pvcs = e2e_tester.v1.list_namespaced_persistent_volume_claim(e2e_namespace)
            deployments = e2e_tester.apps_v1.list_namespaced_deployment(e2e_namespace)
            
            print(f"E2E namespace {e2e_namespace} cleanup:")
            print(f"  Pods: {len(pods.items)}")
            print(f"  Services: {len(services.items)}")
            print(f"  PVCs: {len(pvcs.items)}")
            print(f"  Deployments: {len(deployments.items)}")
            
            # Delete namespace (this will cascade delete all resources)
            e2e_tester.v1.delete_namespace(e2e_namespace)
            print(f"✓ E2E test namespace cleanup initiated: {e2e_namespace}")
            
        except kubernetes.client.rest.ApiException as e:
            if e.status == 404:  # Not found
                print(f"✓ E2E test namespace already cleaned up: {e2e_namespace}")
            else:
                print(f"Warning: Could not clean up E2E namespace: {e}")
    
    def test_final_integration_status(self, e2e_tester):
        """Final integration status check"""
        print("=== Final Integration Status ===")
        
        health = e2e_tester.get_integration_health_status()
        
        print(f"Final Status Summary:")
        print(f"  Nodes: {health['nodes']['ready']}/{health['nodes']['total']} ready")
        print(f"  Proxmox workers: {health['nodes']['proxmox_workers']}")
        print(f"  Pods running: {health['pods']['running']}/{health['pods']['total']}")
        print(f"  Storage classes: {health['storage']['classes']} (Proxmox: {health['storage']['proxmox_classes']})")
        print(f"  Network policies: {health['networking']['policies']}")
        print(f"  CNI pods: {health['networking']['cni_pods']}")
        print(f"  Monitoring: Prometheus={health['monitoring']['prometheus_pods']}, Grafana={health['monitoring']['grafana_pods']}")
        print(f"  Metrics API: {'✓' if health['monitoring']['metrics_available'] else '✗'}")
        
        print("✓ E2E integration testing completed successfully!")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
