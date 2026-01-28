#!/usr/bin/env python3
"""
Proxmox Integration System Integrity Checker

Comprehensive validation of all Proxmox integration components.
"""

import subprocess
import json
import sys
import os
from typing import Dict, List, Tuple
from datetime import datetime
import requests
from urllib3.exceptions import InsecureRequestWarning

# Suppress SSL warnings
requests.packages.urllib3.disable_warnings(InsecureRequestWarning)


class Colors:
    """ANSI color codes"""
    RED = '\033[0;31m'
    GREEN = '\033[0;32m'
    YELLOW = '\033[1;33m'
    BLUE = '\033[0;34m'
    MAGENTA = '\033[0;35m'
    CYAN = '\033[0;36m'
    NC = '\033[0m'  # No Color


class IntegrityChecker:
    """Main integrity checker class"""
    
    def __init__(self, kubeconfig: str = None):
        self.kubeconfig = kubeconfig or os.getenv('KUBECONFIG', '/root/cozy/mgr-cozy/kubeconfig')
        self.checks_total = 0
        self.checks_passed = 0
        self.checks_failed = 0
        self.checks_warning = 0
        self.results = []
        
    def print_header(self, text: str):
        """Print section header"""
        print(f"\n{Colors.BLUE}{'=' * 60}{Colors.NC}")
        print(f"{Colors.BLUE}{text}{Colors.NC}")
        print(f"{Colors.BLUE}{'=' * 60}{Colors.NC}\n")
    
    def print_check(self, text: str):
        """Print check being performed"""
        print(f"{Colors.YELLOW}⏳ Checking: {text}{Colors.NC}")
        self.checks_total += 1
    
    def print_success(self, text: str):
        """Print success message"""
        print(f"{Colors.GREEN}✅ PASS: {text}{Colors.NC}")
        self.checks_passed += 1
        self.results.append(('PASS', text))
    
    def print_fail(self, text: str):
        """Print failure message"""
        print(f"{Colors.RED}❌ FAIL: {text}{Colors.NC}")
        self.checks_failed += 1
        self.results.append(('FAIL', text))
    
    def print_warning(self, text: str):
        """Print warning message"""
        print(f"{Colors.YELLOW}⚠️  WARN: {text}{Colors.NC}")
        self.checks_warning += 1
        self.results.append(('WARN', text))
    
    def print_info(self, text: str):
        """Print info message"""
        print(f"{Colors.CYAN}ℹ️  INFO: {text}{Colors.NC}")
    
    def kubectl(self, *args) -> Tuple[int, str, str]:
        """Run kubectl command"""
        cmd = ['kubectl'] + list(args)
        if self.kubeconfig:
            cmd = ['kubectl', '--kubeconfig', self.kubeconfig] + list(args)
        
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=30
            )
            return result.returncode, result.stdout, result.stderr
        except subprocess.TimeoutExpired:
            return 1, '', 'Command timed out'
        except Exception as e:
            return 1, '', str(e)
    
    def check_kubernetes_api(self):
        """Check Kubernetes API connectivity"""
        self.print_header("1. Kubernetes Cluster Health")
        
        self.print_check("Kubernetes API connectivity")
        rc, stdout, stderr = self.kubectl('cluster-info')
        if rc == 0:
            self.print_success("Kubernetes API is accessible")
            for line in stdout.split('\n')[:3]:
                if line.strip():
                    self.print_info(line.strip())
        else:
            self.print_fail(f"Cannot connect to Kubernetes API: {stderr}")
    
    def check_nodes(self):
        """Check all nodes status"""
        self.print_check("Node status")
        rc, stdout, stderr = self.kubectl('get', 'nodes', '--no-headers')
        
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            total_nodes = len(lines)
            ready_nodes = len([l for l in lines if ' Ready ' in l])
            
            if ready_nodes == total_nodes and total_nodes > 0:
                self.print_success(f"All {total_nodes} nodes are Ready")
            else:
                self.print_warning(f"Only {ready_nodes}/{total_nodes} nodes Ready")
            
            # Print node details
            rc2, stdout2, _ = self.kubectl('get', 'nodes', '-o', 'wide')
            for line in stdout2.split('\n')[:6]:
                if line.strip():
                    print(f"  {line}")
        else:
            self.print_fail("Cannot retrieve node status")
    
    def check_proxmox_worker(self):
        """Check Proxmox worker node"""
        self.print_check("Proxmox worker node")
        rc, stdout, stderr = self.kubectl('get', 'nodes', '-o', 'json')
        
        if rc == 0:
            try:
                nodes = json.loads(stdout)
                proxmox_nodes = []
                
                for node in nodes.get('items', []):
                    node_name = node['metadata']['name']
                    os_image = node['status']['nodeInfo']['osImage']
                    kernel = node['status']['nodeInfo']['kernelVersion']
                    
                    # Check if it's a Proxmox node (Debian with pve kernel)
                    if 'pve' in kernel.lower() or 'proxmox' in os_image.lower():
                        proxmox_nodes.append({
                            'name': node_name,
                            'os': os_image,
                            'kernel': kernel,
                            'status': node['status']['conditions'][-1]['type']
                        })
                
                if proxmox_nodes:
                    self.print_success(f"Proxmox worker node(s) found: {len(proxmox_nodes)}")
                    for pnode in proxmox_nodes:
                        self.print_info(f"  Node: {pnode['name']}, OS: {pnode['os']}, Kernel: {pnode['kernel']}")
                else:
                    self.print_warning("No Proxmox worker nodes detected")
            except Exception as e:
                self.print_fail(f"Error parsing node data: {e}")
        else:
            self.print_fail("Cannot retrieve nodes")
    
    def check_capi_components(self):
        """Check Cluster API components"""
        self.print_header("2. Cluster API Components")
        
        # Check CAPI namespace
        self.print_check("CAPI namespace")
        rc, stdout, stderr = self.kubectl('get', 'namespace', 'cozy-cluster-api')
        if rc == 0:
            self.print_success("CAPI namespace exists")
        else:
            self.print_fail("CAPI namespace not found")
        
        # Check CAPI controller pods
        self.print_check("CAPI controller pods")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'cozy-cluster-api', '--no-headers')
        
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            total = len(lines)
            
            if running > 0:
                self.print_success(f"CAPI controllers running: {running}/{total}")
            else:
                self.print_fail("No CAPI controllers running")
        else:
            self.print_fail("Cannot check CAPI pods")
    
    def check_proxmox_provider(self):
        """Check Proxmox CAPI provider"""
        self.print_check("Proxmox CAPI provider (capmox)")
        
        # Check namespace
        rc, stdout, stderr = self.kubectl('get', 'namespace', 'capmox-system')
        if rc != 0:
            self.print_fail("capmox-system namespace not found")
            return
        
        # Check pods
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'capmox-system', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            
            if running > 0:
                self.print_success(f"Proxmox CAPI provider running: {running} pod(s)")
                rc2, stdout2, _ = self.kubectl('get', 'pods', '-n', 'capmox-system')
                for line in stdout2.split('\n')[:5]:
                    if line.strip():
                        print(f"  {line}")
            else:
                self.print_fail("Proxmox CAPI provider not running")
        else:
            self.print_fail("Cannot check capmox pods")
    
    def check_proxmox_crds(self):
        """Check Proxmox CRDs"""
        self.print_check("Proxmox CRDs")
        rc, stdout, stderr = self.kubectl('get', 'crd')
        
        if rc == 0:
            required_crds = [
                'proxmoxclusters.infrastructure.cluster.x-k8s.io',
                'proxmoxmachines.infrastructure.cluster.x-k8s.io',
                'proxmoxclustertemplates.infrastructure.cluster.x-k8s.io',
                'proxmoxmachinetemplates.infrastructure.cluster.x-k8s.io'
            ]
            
            found_crds = [crd for crd in required_crds if crd in stdout]
            
            if len(found_crds) == len(required_crds):
                self.print_success(f"All {len(required_crds)} Proxmox CRDs installed")
                for crd in found_crds:
                    self.print_info(f"  ✓ {crd}")
            else:
                missing = set(required_crds) - set(found_crds)
                self.print_fail(f"Missing CRDs: {', '.join(missing)}")
        else:
            self.print_fail("Cannot check CRDs")
    
    def check_proxmox_clusters(self):
        """Check ProxmoxCluster resources"""
        self.print_check("ProxmoxCluster resources")
        rc, stdout, stderr = self.kubectl('get', 'proxmoxcluster', '-A')
        
        if rc == 0 and stdout.strip() and 'No resources found' not in stdout:
            lines = [l for l in stdout.split('\n') if l.strip() and 'NAMESPACE' not in l]
            ready_clusters = len([l for l in lines if 'true' in l.lower()])
            total_clusters = len(lines)
            
            if ready_clusters == total_clusters and total_clusters > 0:
                self.print_success(f"All ProxmoxCluster resources Ready: {ready_clusters}/{total_clusters}")
                print(f"  {stdout.split(chr(10))[0]}")  # Header
                for line in lines:
                    print(f"  {line}")
            else:
                self.print_warning(f"ProxmoxCluster status: {ready_clusters}/{total_clusters} Ready")
        else:
            self.print_info("No ProxmoxCluster resources found")
    
    def check_network_stack(self):
        """Check network stack components"""
        self.print_header("3. Network Stack Health")
        
        # CoreDNS
        self.print_check("CoreDNS")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'kube-system', '-l', 'k8s-app=kube-dns', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            if running > 0:
                self.print_success(f"CoreDNS running: {running} pod(s)")
            else:
                self.print_fail("CoreDNS not running")
        
        # Cilium
        self.print_check("Cilium CNI")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'cozy-cilium', '-l', 'app.kubernetes.io/name=cilium', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            if running > 0:
                self.print_success(f"Cilium running: {running} pod(s)")
            else:
                self.print_warning("Cilium not fully running")
        
        # Kube-OVN
        self.print_check("Kube-OVN controller")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'cozy-kubeovn', '-l', 'app=kube-ovn-controller', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            if running > 0:
                self.print_success(f"Kube-OVN controller running: {running} pod(s)")
            else:
                self.print_fail("Kube-OVN controller not running")
    
    def check_storage_stack(self):
        """Check storage components"""
        self.print_header("4. Storage Stack Health")
        
        # CSI drivers
        self.print_check("CSI drivers")
        rc, stdout, stderr = self.kubectl('get', 'csidriver', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            proxmox_csi = [l for l in lines if 'proxmox' in l.lower()]
            
            if proxmox_csi:
                self.print_success(f"Proxmox CSI driver found")
                for line in proxmox_csi:
                    self.print_info(f"  {line}")
            else:
                self.print_warning("No Proxmox CSI driver found")
        
        # Storage classes
        self.print_check("Storage classes")
        rc, stdout, stderr = self.kubectl('get', 'storageclass', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            if len(lines) > 0:
                self.print_success(f"Storage classes found: {len(lines)}")
            else:
                self.print_warning("No storage classes found")
    
    def check_proxmox_api(self):
        """Check Proxmox API accessibility"""
        self.print_header("5. Proxmox API Connectivity")
        
        # Get Proxmox credentials from secret
        self.print_check("Proxmox credentials")
        rc, stdout, stderr = self.kubectl('get', 'secret', 'capmox-credentials', '-n', 'capmox-system', '-o', 'json')
        
        if rc != 0:
            self.print_fail("Proxmox credentials secret not found")
            return
        
        try:
            secret = json.loads(stdout)
            import base64
            
            endpoint = base64.b64decode(secret['data']['PROXMOX_ENDPOINT']).decode('utf-8')
            username = base64.b64decode(secret['data']['PROXMOX_USER']).decode('utf-8')
            password = base64.b64decode(secret['data']['PROXMOX_PASSWORD']).decode('utf-8')
            
            self.print_success("Proxmox credentials loaded")
            self.print_info(f"  Endpoint: {endpoint}")
            self.print_info(f"  User: {username}")
            
            # Test API connection
            self.print_check("Proxmox API authentication")
            try:
                auth_url = f"{endpoint}/api2/json/access/ticket"
                response = requests.post(
                    auth_url,
                    data={'username': username, 'password': password},
                    verify=False,
                    timeout=10
                )
                
                if response.status_code == 200:
                    data = response.json()
                    if 'data' in data and 'ticket' in data['data']:
                        self.print_success("Proxmox API authentication successful")
                        
                        # Get version
                        ticket = data['data']['ticket']
                        csrf = data['data']['CSRFPreventionToken']
                        
                        version_url = f"{endpoint}/api2/json/version"
                        headers = {
                            'Cookie': f'PVEAuthCookie={ticket}',
                            'CSRFPreventionToken': csrf
                        }
                        
                        ver_response = requests.get(version_url, headers=headers, verify=False, timeout=5)
                        if ver_response.status_code == 200:
                            version_data = ver_response.json()
                            if 'data' in version_data:
                                version = version_data['data'].get('version', 'unknown')
                                self.print_info(f"  Proxmox VE version: {version}")
                    else:
                        self.print_fail("Invalid response from Proxmox API")
                else:
                    self.print_fail(f"Proxmox API returned status {response.status_code}")
            except Exception as e:
                self.print_fail(f"Cannot connect to Proxmox API: {e}")
        
        except Exception as e:
            self.print_fail(f"Cannot decode credentials: {e}")
    
    def check_monitoring(self):
        """Check monitoring components"""
        self.print_header("6. Monitoring Stack")
        
        # Prometheus
        self.print_check("Prometheus")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'cozy-monitoring', '-l', 'app.kubernetes.io/name=prometheus', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            if running > 0:
                self.print_success(f"Prometheus running: {running} pod(s)")
            else:
                self.print_warning("Prometheus not running")
        else:
            self.print_warning("Prometheus not found")
        
        # Grafana
        self.print_check("Grafana")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-n', 'cozy-monitoring', '-l', 'app.kubernetes.io/name=grafana', '--no-headers')
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            running = len([l for l in lines if ' Running ' in l])
            if running > 0:
                self.print_success(f"Grafana running: {running} pod(s)")
            else:
                self.print_warning("Grafana not running")
        else:
            self.print_warning("Grafana not found")
    
    def check_workload_health(self):
        """Check overall workload health"""
        self.print_header("7. Workload Health Summary")
        
        self.print_check("Pods in error states")
        rc, stdout, stderr = self.kubectl('get', 'pods', '-A', '--no-headers')
        
        if rc == 0:
            lines = [l for l in stdout.split('\n') if l.strip()]
            error_states = ['Error', 'CrashLoopBackOff', 'ImagePullBackOff', 'Unknown']
            
            error_pods = []
            for line in lines:
                for state in error_states:
                    if state in line:
                        error_pods.append(line)
                        break
            
            if len(error_pods) == 0:
                self.print_success("No pods in error states")
            elif len(error_pods) < 10:
                self.print_warning(f"{len(error_pods)} pods in error states")
                for pod in error_pods[:5]:
                    parts = pod.split()
                    if len(parts) >= 3:
                        self.print_info(f"  {parts[0]}/{parts[1]}: {parts[2]}")
            else:
                self.print_fail(f"{len(error_pods)} pods in error states")
    
    def generate_summary(self):
        """Generate and print summary"""
        self.print_header("INTEGRITY CHECK SUMMARY")
        
        print(f"{Colors.BLUE}Total Checks: {self.checks_total}{Colors.NC}")
        print(f"{Colors.GREEN}Passed: {self.checks_passed}{Colors.NC}")
        print(f"{Colors.RED}Failed: {self.checks_failed}{Colors.NC}")
        print(f"{Colors.YELLOW}Warnings: {self.checks_warning}{Colors.NC}")
        print()
        
        if self.checks_total > 0:
            success_rate = (self.checks_passed * 100) // self.checks_total
            print(f"{Colors.BLUE}Success Rate: {success_rate}%{Colors.NC}")
            print()
        
        # Determine overall status
        if self.checks_failed == 0 and self.checks_warning < 5:
            print(f"{Colors.GREEN}✅ OVERALL STATUS: HEALTHY{Colors.NC}")
            print(f"{Colors.GREEN}Proxmox integration is fully operational!{Colors.NC}")
            return 0
        elif self.checks_failed < 3:
            print(f"{Colors.YELLOW}⚠️  OVERALL STATUS: DEGRADED{Colors.NC}")
            print(f"{Colors.YELLOW}Proxmox integration has some issues but is functional{Colors.NC}")
            return 1
        else:
            print(f"{Colors.RED}❌ OVERALL STATUS: CRITICAL{Colors.NC}")
            print(f"{Colors.RED}Proxmox integration has critical issues!{Colors.NC}")
            return 2
    
    def run_all_checks(self):
        """Run all integrity checks"""
        print(f"{Colors.MAGENTA}")
        print("╔══════════════════════════════════════════════════════════╗")
        print("║  PROXMOX INTEGRATION SYSTEM INTEGRITY CHECKER           ║")
        print("╚══════════════════════════════════════════════════════════╝")
        print(f"{Colors.NC}")
        print(f"Started: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
        print(f"Kubeconfig: {self.kubeconfig}")
        
        # Run all check sections
        self.check_kubernetes_api()
        self.check_nodes()
        self.check_proxmox_worker()
        self.check_capi_components()
        self.check_proxmox_provider()
        self.check_proxmox_crds()
        self.check_proxmox_clusters()
        self.check_network_stack()
        self.check_storage_stack()
        self.check_proxmox_api()
        self.check_monitoring()
        self.check_workload_health()
        
        # Generate summary
        exit_code = self.generate_summary()
        
        print(f"\nCompleted: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
        
        return exit_code


def main():
    """Main entry point"""
    kubeconfig = os.getenv('KUBECONFIG')
    
    checker = IntegrityChecker(kubeconfig=kubeconfig)
    exit_code = checker.run_all_checks()
    
    sys.exit(exit_code)


if __name__ == '__main__':
    main()

