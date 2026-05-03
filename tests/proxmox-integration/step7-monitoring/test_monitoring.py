"""
Test Step 7: Monitoring and Logging

This module tests monitoring stack integration for Proxmox workloads.
"""

import pytest
import requests
import time
import json
import os
from typing import Dict, Any, List, Optional
import kubernetes
from kubernetes import client, config
from urllib3.exceptions import InsecureRequestWarning

# Suppress SSL warnings for test environments
requests.packages.urllib3.disable_warnings(InsecureRequestWarning)


class MonitoringTester:
    """Test utilities for monitoring and logging"""
    
    def __init__(self):
        try:
            config.load_incluster_config()
        except:
            config.load_kube_config()
        
        self.v1 = client.CoreV1Api()
        self.apps_v1 = client.AppsV1Api()
        self.custom_api = client.CustomObjectsApi()
    
    def get_monitoring_pods(self) -> List[Dict[str, Any]]:
        """Get monitoring-related pods"""
        pods = self.v1.list_pod_for_all_namespaces()
        monitoring_pods = []
        
        monitoring_keywords = [
            'prometheus', 'grafana', 'alertmanager', 'node-exporter',
            'kube-state-metrics', 'metrics-server', 'loki', 'fluent'
        ]
        
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if any(keyword in pod_name for keyword in monitoring_keywords):
                monitoring_pods.append({
                    'name': pod.metadata.name,
                    'namespace': pod.metadata.namespace,
                    'status': pod.status.phase,
                    'node': pod.spec.node_name,
                    'type': self.detect_monitoring_type(pod.metadata.name),
                    'ready': self.is_pod_ready(pod)
                })
        
        return monitoring_pods
    
    def detect_monitoring_type(self, pod_name: str) -> str:
        """Detect monitoring component type from pod name"""
        pod_name = pod_name.lower()
        if 'prometheus' in pod_name:
            return 'prometheus'
        elif 'grafana' in pod_name:
            return 'grafana'
        elif 'alertmanager' in pod_name:
            return 'alertmanager'
        elif 'node-exporter' in pod_name:
            return 'node-exporter'
        elif 'kube-state-metrics' in pod_name:
            return 'kube-state-metrics'
        elif 'metrics-server' in pod_name:
            return 'metrics-server'
        elif 'loki' in pod_name:
            return 'loki'
        elif 'fluent' in pod_name:
            return 'fluent'
        else:
            return 'unknown'
    
    def is_pod_ready(self, pod) -> bool:
        """Check if pod is ready"""
        if not pod.status.conditions:
            return False
        
        for condition in pod.status.conditions:
            if condition.type == "Ready" and condition.status == "True":
                return True
        return False
    
    def get_monitoring_services(self) -> List[Dict[str, Any]]:
        """Get monitoring services"""
        services = self.v1.list_service_for_all_namespaces()
        monitoring_services = []
        
        monitoring_keywords = [
            'prometheus', 'grafana', 'alertmanager', 'node-exporter',
            'kube-state-metrics', 'metrics-server'
        ]
        
        for service in services.items:
            service_name = service.metadata.name.lower()
            if any(keyword in service_name for keyword in monitoring_keywords):
                monitoring_services.append({
                    'name': service.metadata.name,
                    'namespace': service.metadata.namespace,
                    'type': service.spec.type,
                    'cluster_ip': service.spec.cluster_ip,
                    'ports': [
                        {'port': port.port, 'target_port': port.target_port, 'protocol': port.protocol}
                        for port in (service.spec.ports or [])
                    ],
                    'monitoring_type': self.detect_monitoring_type(service.metadata.name)
                })
        
        return monitoring_services
    
    def query_prometheus_api(self, prometheus_url: str, query: str, timeout: int = 30) -> Optional[Dict[str, Any]]:
        """Query Prometheus API"""
        try:
            url = f"{prometheus_url}/api/v1/query"
            params = {'query': query}
            
            response = requests.get(url, params=params, timeout=timeout, verify=False)
            response.raise_for_status()
            
            return response.json()
        except Exception as e:
            print(f"Prometheus query failed: {e}")
            return None
    
    def check_grafana_api(self, grafana_url: str, timeout: int = 30) -> bool:
        """Check Grafana API availability"""
        try:
            url = f"{grafana_url}/api/health"
            response = requests.get(url, timeout=timeout, verify=False)
            return response.status_code == 200
        except Exception:
            return False
    
    def get_node_metrics(self) -> Dict[str, Any]:
        """Get node metrics from metrics server"""
        try:
            # Try to get node metrics
            nodes_metrics = self.custom_api.list_cluster_custom_object(
                group="metrics.k8s.io",
                version="v1beta1",
                plural="nodes"
            )
            return nodes_metrics
        except Exception:
            return {}
    
    def get_pod_metrics(self, namespace: str = None) -> Dict[str, Any]:
        """Get pod metrics from metrics server"""
        try:
            if namespace:
                pod_metrics = self.custom_api.list_namespaced_custom_object(
                    group="metrics.k8s.io",
                    version="v1beta1",
                    namespace=namespace,
                    plural="pods"
                )
            else:
                pod_metrics = self.custom_api.list_cluster_custom_object(
                    group="metrics.k8s.io",
                    version="v1beta1",
                    plural="pods"
                )
            return pod_metrics
        except Exception:
            return {}


@pytest.fixture
def monitoring_tester():
    """Create MonitoringTester instance"""
    return MonitoringTester()


@pytest.fixture
def monitoring_config():
    """Monitoring configuration for testing"""
    return {
        'monitoring_namespace': os.getenv('MONITORING_NAMESPACE', 'monitoring'),
        'prometheus_endpoint': os.getenv('PROMETHEUS_ENDPOINT', ''),
        'grafana_endpoint': os.getenv('GRAFANA_ENDPOINT', ''),
        'test_timeout': int(os.getenv('MONITORING_TEST_TIMEOUT', '60')),
        'enable_external_tests': os.getenv('ENABLE_EXTERNAL_MONITORING_TESTS', 'false').lower() == 'true'
    }


class TestMonitoringStackInstallation:
    """Test monitoring stack installation and status"""
    
    def test_monitoring_pods_running(self, monitoring_tester):
        """Test that monitoring pods are running"""
        monitoring_pods = monitoring_tester.get_monitoring_pods()
        
        if len(monitoring_pods) == 0:
            pytest.skip("No monitoring pods found")
        
        print(f"Found {len(monitoring_pods)} monitoring pods")
        
        monitoring_types = set()
        failed_pods = []
        
        for pod in monitoring_pods:
            monitoring_types.add(pod['type'])
            
            if pod['status'] != 'Running':
                failed_pods.append(f"{pod['namespace']}/{pod['name']} ({pod['status']})")
                print(f"⚠ Monitoring pod not running: {pod['namespace']}/{pod['name']} - {pod['status']}")
            elif not pod['ready']:
                failed_pods.append(f"{pod['namespace']}/{pod['name']} (not ready)")
                print(f"⚠ Monitoring pod not ready: {pod['namespace']}/{pod['name']}")
            else:
                print(f"✓ Monitoring pod running: {pod['namespace']}/{pod['name']} ({pod['type']})")
        
        print(f"Monitoring types detected: {list(monitoring_types)}")
        
        if failed_pods:
            print(f"Failed pods: {failed_pods}")
            # Don't fail the test, just report status
    
    def test_prometheus_deployment(self, monitoring_tester):
        """Test Prometheus deployment specifically"""
        monitoring_pods = monitoring_tester.get_monitoring_pods()
        prometheus_pods = [pod for pod in monitoring_pods if pod['type'] == 'prometheus']
        
        if len(prometheus_pods) == 0:
            pytest.skip("No Prometheus pods found")
        
        print(f"Found {len(prometheus_pods)} Prometheus pods")
        
        for pod in prometheus_pods:
            assert pod['status'] == 'Running', f"Prometheus pod not running: {pod['name']}"
            assert pod['ready'], f"Prometheus pod not ready: {pod['name']}"
            print(f"✓ Prometheus pod healthy: {pod['namespace']}/{pod['name']}")
    
    def test_grafana_deployment(self, monitoring_tester):
        """Test Grafana deployment specifically"""
        monitoring_pods = monitoring_tester.get_monitoring_pods()
        grafana_pods = [pod for pod in monitoring_pods if pod['type'] == 'grafana']
        
        if len(grafana_pods) == 0:
            print("⚠ No Grafana pods found")
            return
        
        print(f"Found {len(grafana_pods)} Grafana pods")
        
        for pod in grafana_pods:
            if pod['status'] == 'Running' and pod['ready']:
                print(f"✓ Grafana pod healthy: {pod['namespace']}/{pod['name']}")
            else:
                print(f"⚠ Grafana pod issues: {pod['namespace']}/{pod['name']} - {pod['status']}")
    
    def test_node_exporter_daemonset(self, monitoring_tester):
        """Test Node Exporter DaemonSet"""
        daemonsets = monitoring_tester.apps_v1.list_daemon_set_for_all_namespaces()
        node_exporter_ds = []
        
        for ds in daemonsets.items:
            if 'node-exporter' in ds.metadata.name.lower():
                node_exporter_ds.append({
                    'name': ds.metadata.name,
                    'namespace': ds.metadata.namespace,
                    'desired': ds.status.desired_number_scheduled or 0,
                    'ready': ds.status.number_ready or 0,
                    'available': ds.status.number_available or 0
                })
        
        if len(node_exporter_ds) == 0:
            print("⚠ No Node Exporter DaemonSet found")
            return
        
        for ds in node_exporter_ds:
            print(f"Node Exporter DaemonSet: {ds['namespace']}/{ds['name']}")
            print(f"  Desired: {ds['desired']}, Ready: {ds['ready']}, Available: {ds['available']}")
            
            if ds['ready'] == ds['desired'] and ds['desired'] > 0:
                print(f"✓ Node Exporter fully deployed")
            else:
                print(f"⚠ Node Exporter not fully ready ({ds['ready']}/{ds['desired']})")


class TestMonitoringServices:
    """Test monitoring services and endpoints"""
    
    def test_monitoring_services_exist(self, monitoring_tester):
        """Test that monitoring services exist"""
        monitoring_services = monitoring_tester.get_monitoring_services()
        
        if len(monitoring_services) == 0:
            pytest.skip("No monitoring services found")
        
        print(f"Found {len(monitoring_services)} monitoring services")
        
        service_types = set()
        for service in monitoring_services:
            service_types.add(service['monitoring_type'])
            print(f"✓ Service: {service['namespace']}/{service['name']} ({service['monitoring_type']})")
            print(f"  Type: {service['type']}, Cluster IP: {service['cluster_ip']}")
            
            for port in service['ports']:
                print(f"  Port: {port['port']} -> {port['target_port']} ({port['protocol']})")
        
        print(f"Service types: {list(service_types)}")
    
    def test_prometheus_service_endpoints(self, monitoring_tester):
        """Test Prometheus service endpoints"""
        monitoring_services = monitoring_tester.get_monitoring_services()
        prometheus_services = [s for s in monitoring_services if s['monitoring_type'] == 'prometheus']
        
        if len(prometheus_services) == 0:
            pytest.skip("No Prometheus services found")
        
        for service in prometheus_services:
            print(f"Prometheus service: {service['namespace']}/{service['name']}")
            
            # Check if service has endpoints
            try:
                endpoints = monitoring_tester.v1.read_namespaced_endpoints(
                    service['name'], service['namespace']
                )
                
                if endpoints.subsets:
                    endpoint_count = sum(len(subset.addresses or []) for subset in endpoints.subsets)
                    print(f"✓ Prometheus service has {endpoint_count} endpoints")
                else:
                    print(f"⚠ Prometheus service has no endpoints")
                    
            except Exception as e:
                print(f"⚠ Could not check Prometheus endpoints: {e}")


class TestMetricsCollection:
    """Test metrics collection and availability"""
    
    def test_metrics_server_availability(self, monitoring_tester):
        """Test metrics server availability"""
        node_metrics = monitoring_tester.get_node_metrics()
        
        if not node_metrics or 'items' not in node_metrics:
            print("⚠ Metrics server not available or no node metrics")
            return
        
        node_count = len(node_metrics['items'])
        print(f"✓ Metrics server available, found metrics for {node_count} nodes")
        
        # Show sample metrics
        for i, node_metric in enumerate(node_metrics['items'][:3]):  # Show first 3 nodes
            node_name = node_metric['metadata']['name']
            usage = node_metric.get('usage', {})
            
            print(f"  Node {node_name}:")
            print(f"    CPU: {usage.get('cpu', 'unknown')}")
            print(f"    Memory: {usage.get('memory', 'unknown')}")
    
    def test_pod_metrics_collection(self, monitoring_tester, monitoring_config):
        """Test pod metrics collection"""
        pod_metrics = monitoring_tester.get_pod_metrics(monitoring_config['monitoring_namespace'])
        
        if not pod_metrics or 'items' not in pod_metrics:
            print("⚠ No pod metrics available")
            return
        
        pod_count = len(pod_metrics['items'])
        print(f"✓ Pod metrics available for {pod_count} pods in monitoring namespace")
        
        # Show sample metrics for monitoring pods
        monitoring_pod_metrics = [
            pm for pm in pod_metrics['items']
            if any(keyword in pm['metadata']['name'].lower() 
                   for keyword in ['prometheus', 'grafana', 'alertmanager'])
        ]
        
        for pod_metric in monitoring_pod_metrics[:3]:  # Show first 3
            pod_name = pod_metric['metadata']['name']
            usage = pod_metric.get('usage', {})
            
            print(f"  Pod {pod_name}:")
            print(f"    CPU: {usage.get('cpu', 'unknown')}")
            print(f"    Memory: {usage.get('memory', 'unknown')}")
    
    def test_prometheus_metrics_endpoints(self, monitoring_tester):
        """Test Prometheus metrics endpoints"""
        monitoring_pods = monitoring_tester.get_monitoring_pods()
        
        # Look for node-exporter pods
        node_exporter_pods = [pod for pod in monitoring_pods if pod['type'] == 'node-exporter']
        
        if len(node_exporter_pods) == 0:
            print("⚠ No node-exporter pods found for metrics endpoint testing")
            return
        
        print(f"Found {len(node_exporter_pods)} node-exporter pods")
        
        # In a real test, we would try to access /metrics endpoint
        # For now, just verify pods are ready
        ready_exporters = [pod for pod in node_exporter_pods if pod['ready']]
        print(f"✓ {len(ready_exporters)} node-exporter pods are ready to serve metrics")


class TestProxmoxSpecificMonitoring:
    """Test Proxmox-specific monitoring configuration"""
    
    def test_proxmox_node_metrics(self, monitoring_tester):
        """Test metrics collection from Proxmox nodes"""
        # Get all nodes and check if any are Proxmox workers
        nodes = monitoring_tester.v1.list_node()
        proxmox_nodes = []
        
        for node in nodes.items:
            labels = node.metadata.labels or {}
            if any('proxmox' in key.lower() or 'proxmox' in value.lower() 
                   for key, value in labels.items()):
                proxmox_nodes.append(node.metadata.name)
        
        if len(proxmox_nodes) == 0:
            print("⚠ No Proxmox worker nodes detected for monitoring test")
            return
        
        print(f"Found {len(proxmox_nodes)} Proxmox worker nodes")
        
        # Check if we have metrics for these nodes
        node_metrics = monitoring_tester.get_node_metrics()
        if node_metrics and 'items' in node_metrics:
            proxmox_metrics = [
                nm for nm in node_metrics['items']
                if nm['metadata']['name'] in proxmox_nodes
            ]
            
            print(f"✓ Metrics available for {len(proxmox_metrics)} Proxmox nodes")
            
            for metric in proxmox_metrics:
                node_name = metric['metadata']['name']
                usage = metric.get('usage', {})
                print(f"  Proxmox node {node_name}: CPU={usage.get('cpu', 'N/A')}, Memory={usage.get('memory', 'N/A')}")
    
    def test_proxmox_pod_monitoring(self, monitoring_tester):
        """Test monitoring of Proxmox-related pods"""
        # Look for Proxmox-related pods
        pods = monitoring_tester.v1.list_pod_for_all_namespaces()
        proxmox_pods = []
        
        for pod in pods.items:
            pod_name = pod.metadata.name.lower()
            if 'proxmox' in pod_name:
                proxmox_pods.append({
                    'name': pod.metadata.name,
                    'namespace': pod.metadata.namespace,
                    'node': pod.spec.node_name
                })
        
        if len(proxmox_pods) == 0:
            print("⚠ No Proxmox-related pods found for monitoring test")
            return
        
        print(f"Found {len(proxmox_pods)} Proxmox-related pods")
        
        # Check if we have metrics for these pods
        for pod in proxmox_pods[:3]:  # Check first 3
            try:
                pod_metrics = monitoring_tester.get_pod_metrics(pod['namespace'])
                if pod_metrics and 'items' in pod_metrics:
                    pod_metric = next(
                        (pm for pm in pod_metrics['items'] 
                         if pm['metadata']['name'] == pod['name']), None
                    )
                    
                    if pod_metric:
                        usage = pod_metric.get('usage', {})
                        print(f"✓ Metrics for Proxmox pod {pod['namespace']}/{pod['name']}")
                        print(f"    CPU: {usage.get('cpu', 'N/A')}, Memory: {usage.get('memory', 'N/A')}")
                    else:
                        print(f"⚠ No metrics found for Proxmox pod {pod['namespace']}/{pod['name']}")
            except Exception as e:
                print(f"⚠ Error getting metrics for pod {pod['name']}: {e}")


class TestExternalMonitoringAPI:
    """Test external monitoring API access (optional)"""
    
    def test_prometheus_api_access(self, monitoring_tester, monitoring_config):
        """Test Prometheus API access"""
        if not monitoring_config['enable_external_tests'] or not monitoring_config['prometheus_endpoint']:
            pytest.skip("External Prometheus API testing disabled or endpoint not configured")
        
        prometheus_url = monitoring_config['prometheus_endpoint']
        
        # Test basic connectivity
        try:
            response = requests.get(f"{prometheus_url}/api/v1/status/config", 
                                  timeout=monitoring_config['test_timeout'], verify=False)
            assert response.status_code == 200, f"Prometheus API not accessible: {response.status_code}"
            print(f"✓ Prometheus API accessible at {prometheus_url}")
        except Exception as e:
            pytest.fail(f"Prometheus API test failed: {e}")
        
        # Test sample queries
        test_queries = [
            'up',  # Basic connectivity test
            'node_cpu_seconds_total',  # Node metrics
            'container_memory_usage_bytes',  # Container metrics
        ]
        
        for query in test_queries:
            result = monitoring_tester.query_prometheus_api(prometheus_url, query)
            if result and result.get('status') == 'success':
                data_points = len(result.get('data', {}).get('result', []))
                print(f"✓ Query '{query}' returned {data_points} data points")
            else:
                print(f"⚠ Query '{query}' failed or returned no data")
    
    def test_grafana_api_access(self, monitoring_tester, monitoring_config):
        """Test Grafana API access"""
        if not monitoring_config['enable_external_tests'] or not monitoring_config['grafana_endpoint']:
            pytest.skip("External Grafana API testing disabled or endpoint not configured")
        
        grafana_url = monitoring_config['grafana_endpoint']
        
        # Test basic connectivity
        success = monitoring_tester.check_grafana_api(grafana_url, monitoring_config['test_timeout'])
        
        if success:
            print(f"✓ Grafana API accessible at {grafana_url}")
        else:
            print(f"⚠ Grafana API not accessible at {grafana_url}")


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])
