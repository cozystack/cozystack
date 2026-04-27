"""
Test Step 1: Proxmox API Connection and Authentication

This module tests the basic connectivity and authentication to Proxmox VE API.
"""

import pytest
import requests
import os
import json
from typing import Dict, Any
from urllib3.exceptions import InsecureRequestWarning
import time

# Suppress SSL warnings for test environments
requests.packages.urllib3.disable_warnings(InsecureRequestWarning)


class ProxmoxAPIClient:
    """Simple Proxmox API client for testing"""
    
    def __init__(self, host: str, username: str, password: str, port: int = 8006, verify_ssl: bool = False):
        self.host = host
        self.username = username
        self.password = password
        self.port = port
        self.verify_ssl = verify_ssl
        self.base_url = f"https://{host}:{port}/api2/json"
        self.token = None
        self.csrf_token = None
        
    def authenticate(self) -> bool:
        """Authenticate with Proxmox and get tokens"""
        auth_url = f"{self.base_url}/access/ticket"
        auth_data = {
            'username': self.username,
            'password': self.password
        }
        
        try:
            response = requests.post(auth_url, data=auth_data, verify=self.verify_ssl, timeout=10)
            response.raise_for_status()
            
            result = response.json()
            if 'data' in result:
                self.token = result['data']['ticket']
                self.csrf_token = result['data']['CSRFPreventionToken']
                return True
            return False
        except Exception as e:
            print(f"Authentication failed: {e}")
            return False
    
    def get_headers(self) -> Dict[str, str]:
        """Get headers for authenticated requests"""
        headers = {
            'Cookie': f'PVEAuthCookie={self.token}',
            'CSRFPreventionToken': self.csrf_token
        }
        return headers
    
    def get_version(self) -> Dict[str, Any]:
        """Get Proxmox version information"""
        url = f"{self.base_url}/version"
        response = requests.get(url, headers=self.get_headers(), verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_nodes(self) -> Dict[str, Any]:
        """Get list of Proxmox nodes"""
        url = f"{self.base_url}/nodes"
        response = requests.get(url, headers=self.get_headers(), verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_cluster_status(self) -> Dict[str, Any]:
        """Get cluster status"""
        url = f"{self.base_url}/cluster/status"
        response = requests.get(url, headers=self.get_headers(), verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()


@pytest.fixture
def proxmox_config():
    """Load Proxmox configuration from environment or config file"""
    config = {
        'host': os.getenv('PROXMOX_HOST', 'proxmox.example.com'),
        'username': os.getenv('PROXMOX_USERNAME', 'root@pam'),
        'password': os.getenv('PROXMOX_PASSWORD', ''),
        'port': int(os.getenv('PROXMOX_PORT', '8006')),
        'verify_ssl': os.getenv('PROXMOX_VERIFY_SSL', 'false').lower() == 'true'
    }
    
    # Load from config file if exists
    config_file = os.path.join(os.path.dirname(__file__), 'proxmox-config.json')
    if os.path.exists(config_file):
        with open(config_file, 'r') as f:
            file_config = json.load(f)
            config.update(file_config)
    
    return config


@pytest.fixture
def proxmox_client(proxmox_config):
    """Create and authenticate Proxmox API client"""
    client = ProxmoxAPIClient(
        host=proxmox_config['host'],
        username=proxmox_config['username'],
        password=proxmox_config['password'],
        port=proxmox_config['port'],
        verify_ssl=proxmox_config['verify_ssl']
    )
    return client


class TestProxmoxAPIConnection:
    """Test Proxmox API connectivity and basic operations"""
    
    def test_proxmox_host_reachable(self, proxmox_config):
        """Test that Proxmox host is reachable"""
        host = proxmox_config['host']
        port = proxmox_config['port']
        
        # Test TCP connection
        import socket
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(5)
        
        try:
            result = sock.connect_ex((host, port))
            assert result == 0, f"Cannot connect to {host}:{port}"
        finally:
            sock.close()
    
    def test_proxmox_api_authentication(self, proxmox_client):
        """Test Proxmox API authentication"""
        assert proxmox_client.authenticate(), "Failed to authenticate with Proxmox API"
        assert proxmox_client.token is not None, "No authentication token received"
        assert proxmox_client.csrf_token is not None, "No CSRF token received"
    
    def test_proxmox_version_info(self, proxmox_client):
        """Test retrieving Proxmox version information"""
        proxmox_client.authenticate()
        version_info = proxmox_client.get_version()
        
        assert 'data' in version_info, "No version data returned"
        assert 'version' in version_info['data'], "No version field in response"
        assert 'release' in version_info['data'], "No release field in response"
        
        print(f"Proxmox Version: {version_info['data']['version']}")
        print(f"Proxmox Release: {version_info['data']['release']}")
    
    def test_proxmox_nodes_list(self, proxmox_client):
        """Test retrieving list of Proxmox nodes"""
        proxmox_client.authenticate()
        nodes = proxmox_client.get_nodes()
        
        assert 'data' in nodes, "No nodes data returned"
        assert len(nodes['data']) > 0, "No nodes found in cluster"
        
        for node in nodes['data']:
            assert 'node' in node, "Node name missing"
            assert 'status' in node, "Node status missing"
            assert 'type' in node, "Node type missing"
            
            print(f"Node: {node['node']}, Status: {node['status']}, Type: {node['type']}")
    
    def test_proxmox_cluster_status(self, proxmox_client):
        """Test retrieving cluster status"""
        proxmox_client.authenticate()
        cluster_status = proxmox_client.get_cluster_status()
        
        assert 'data' in cluster_status, "No cluster status data returned"
        
        for item in cluster_status['data']:
            if 'type' in item:
                print(f"Cluster item: {item.get('name', 'unknown')}, Type: {item['type']}")
    
    def test_proxmox_api_permissions(self, proxmox_client):
        """Test that the user has required permissions"""
        proxmox_client.authenticate()
        
        # Test permissions by trying to access nodes
        try:
            nodes = proxmox_client.get_nodes()
            assert 'data' in nodes, "User doesn't have permission to list nodes"
        except requests.exceptions.HTTPError as e:
            if e.response.status_code == 403:
                pytest.fail("User doesn't have sufficient permissions")
            raise
    
    def test_proxmox_api_response_time(self, proxmox_client):
        """Test Proxmox API response time"""
        proxmox_client.authenticate()
        
        start_time = time.time()
        version_info = proxmox_client.get_version()
        response_time = time.time() - start_time
        
        assert response_time < 5.0, f"API response time too slow: {response_time:.2f}s"
        print(f"API response time: {response_time:.3f}s")


class TestProxmoxAPIErrorHandling:
    """Test error handling for Proxmox API"""
    
    def test_invalid_credentials(self, proxmox_config):
        """Test handling of invalid credentials"""
        client = ProxmoxAPIClient(
            host=proxmox_config['host'],
            username='invalid_user',
            password='invalid_password',
            port=proxmox_config['port'],
            verify_ssl=proxmox_config['verify_ssl']
        )
        
        assert not client.authenticate(), "Authentication should fail with invalid credentials"
    
    def test_invalid_host(self):
        """Test handling of invalid host"""
        client = ProxmoxAPIClient(
            host='invalid.host.example.com',
            username='test',
            password='test'
        )
        
        assert not client.authenticate(), "Authentication should fail with invalid host"
    
    def test_connection_timeout(self):
        """Test handling of connection timeout"""
        # Use a non-routable IP to simulate timeout
        client = ProxmoxAPIClient(
            host='10.255.255.1',
            username='test',
            password='test'
        )
        
        assert not client.authenticate(), "Authentication should fail with timeout"


if __name__ == "__main__":
    # Run tests with pytest
    pytest.main([__file__, "-v"])
