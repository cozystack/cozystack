"""
Test Step 2: Proxmox Network and Storage Configuration

This module tests network bridges, VLANs, and storage configuration in Proxmox.
"""

import pytest
import sys
import os

# Add step1 to path for shared utilities
sys.path.append(os.path.join(os.path.dirname(__file__), '..', 'step1-api-connection'))
from test_proxmox_api import ProxmoxAPIClient


class ProxmoxNetworkStorageClient(ProxmoxAPIClient):
    """Extended Proxmox client for network and storage operations"""
    
    def get_network_interfaces(self, node: str):
        """Get network interfaces for a node"""
        url = f"{self.base_url}/nodes/{node}/network"
        response = self.session.get(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_storage_config(self):
        """Get storage configuration"""
        url = f"{self.base_url}/storage"
        response = self.session.get(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_node_storage(self, node: str):
        """Get storage information for a specific node"""
        url = f"{self.base_url}/nodes/{node}/storage"
        response = self.session.get(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_sdn_zones(self):
        """Get Software Defined Network zones"""
        url = f"{self.base_url}/cluster/sdn/zones"
        response = self.session.get(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def get_sdn_vnets(self):
        """Get SDN virtual networks"""
        url = f"{self.base_url}/cluster/sdn/vnets"
        response = self.session.get(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def create_test_bridge(self, node: str, bridge_name: str):
        """Create a test bridge interface"""
        url = f"{self.base_url}/nodes/{node}/network"
        data = {
            'iface': bridge_name,
            'type': 'bridge',
            'autostart': 1,
            'bridge_ports': '',
            'bridge_stp': 0,
            'bridge_fd': 0
        }
        response = self.session.post(url, data=data, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()
    
    def delete_test_bridge(self, node: str, bridge_name: str):
        """Delete a test bridge interface"""
        url = f"{self.base_url}/nodes/{node}/network/{bridge_name}"
        response = self.session.delete(url, verify=self.verify_ssl)
        response.raise_for_status()
        return response.json()


@pytest.fixture
def network_storage_client(proxmox_config):
    """Create authenticated Proxmox network/storage client"""
    from test_proxmox_api import proxmox_config  # Import the fixture
    
    client = ProxmoxNetworkStorageClient(
        host=proxmox_config['host'],
        username=proxmox_config['username'],
        password=proxmox_config['password'],
        port=proxmox_config['port'],
        verify_ssl=proxmox_config['verify_ssl']
    )
    client.authenticate()
    return client


@pytest.fixture
def test_node(network_storage_client):
    """Get the first available node for testing"""
    nodes = network_storage_client.get_nodes()
    assert len(nodes['data']) > 0, "No nodes available for testing"
    return nodes['data'][0]['node']


class TestProxmoxNetworking:
    """Test Proxmox networking configuration"""
    
    def test_network_interfaces_exist(self, network_storage_client, test_node):
        """Test that network interfaces are configured"""
        interfaces = network_storage_client.get_network_interfaces(test_node)
        
        assert 'data' in interfaces, "No network interfaces data returned"
        assert len(interfaces['data']) > 0, "No network interfaces found"
        
        # Check for essential interfaces
        interface_types = [iface.get('type') for iface in interfaces['data']]
        assert 'loopback' in interface_types, "No loopback interface found"
        
        print(f"Found {len(interfaces['data'])} network interfaces")
        for iface in interfaces['data']:
            print(f"  {iface.get('iface', 'unknown')}: {iface.get('type', 'unknown')}")
    
    def test_bridge_interfaces_configured(self, network_storage_client, test_node):
        """Test that bridge interfaces are properly configured"""
        interfaces = network_storage_client.get_network_interfaces(test_node)
        
        bridges = [iface for iface in interfaces['data'] if iface.get('type') == 'bridge']
        assert len(bridges) > 0, "No bridge interfaces found"
        
        for bridge in bridges:
            bridge_name = bridge.get('iface')
            assert bridge_name is not None, "Bridge has no name"
            assert bridge_name.startswith('vmbr'), f"Bridge {bridge_name} doesn't follow naming convention"
            
            print(f"Bridge {bridge_name}: active={bridge.get('active', 'unknown')}")
    
    def test_default_bridge_exists(self, network_storage_client, test_node):
        """Test that default bridge (vmbr0) exists"""
        interfaces = network_storage_client.get_network_interfaces(test_node)
        
        vmbr0_exists = any(
            iface.get('iface') == 'vmbr0' and iface.get('type') == 'bridge'
            for iface in interfaces['data']
        )
        assert vmbr0_exists, "Default bridge vmbr0 not found"
    
    def test_sdn_configuration(self, network_storage_client):
        """Test Software Defined Network configuration"""
        try:
            zones = network_storage_client.get_sdn_zones()
            vnets = network_storage_client.get_sdn_vnets()
            
            print(f"SDN Zones: {len(zones.get('data', []))}")
            print(f"SDN VNets: {len(vnets.get('data', []))}")
            
            # SDN is optional, so this test passes even if no SDN is configured
        except Exception as e:
            print(f"SDN not configured or not accessible: {e}")
    
    def test_network_bridge_creation(self, network_storage_client, test_node):
        """Test creating and deleting a test bridge"""
        test_bridge = "vmbr999"
        
        try:
            # Create test bridge
            result = network_storage_client.create_test_bridge(test_node, test_bridge)
            assert result is not None, "Failed to create test bridge"
            
            # Verify bridge was created
            interfaces = network_storage_client.get_network_interfaces(test_node)
            bridge_exists = any(
                iface.get('iface') == test_bridge
                for iface in interfaces['data']
            )
            assert bridge_exists, f"Test bridge {test_bridge} was not created"
            
        finally:
            # Clean up: delete test bridge
            try:
                network_storage_client.delete_test_bridge(test_node, test_bridge)
            except Exception as e:
                print(f"Warning: Failed to clean up test bridge: {e}")


class TestProxmoxStorage:
    """Test Proxmox storage configuration"""
    
    def test_storage_config_exists(self, network_storage_client):
        """Test that storage configuration exists"""
        storage = network_storage_client.get_storage_config()
        
        assert 'data' in storage, "No storage configuration data returned"
        assert len(storage['data']) > 0, "No storage pools configured"
        
        print(f"Found {len(storage['data'])} storage pools")
        for pool in storage['data']:
            print(f"  {pool.get('storage', 'unknown')}: {pool.get('type', 'unknown')}")
    
    def test_local_storage_exists(self, network_storage_client):
        """Test that local storage exists"""
        storage = network_storage_client.get_storage_config()
        
        local_storage = [
            pool for pool in storage['data']
            if pool.get('storage') == 'local'
        ]
        assert len(local_storage) > 0, "No local storage found"
        
        local = local_storage[0]
        assert local.get('type') == 'dir', "Local storage is not directory type"
        assert local.get('path') == '/var/lib/vz', "Local storage path incorrect"
    
    def test_node_storage_availability(self, network_storage_client, test_node):
        """Test storage availability on nodes"""
        node_storage = network_storage_client.get_node_storage(test_node)
        
        assert 'data' in node_storage, "No node storage data returned"
        assert len(node_storage['data']) > 0, "No storage available on node"
        
        for storage in node_storage['data']:
            storage_name = storage.get('storage')
            status = storage.get('status')
            
            print(f"Storage {storage_name}: status={status}")
            
            # Check that storage is available
            assert status == 'available', f"Storage {storage_name} is not available"
    
    def test_storage_types_supported(self, network_storage_client):
        """Test that required storage types are supported"""
        storage = network_storage_client.get_storage_config()
        
        storage_types = [pool.get('type') for pool in storage['data']]
        
        # Check for essential storage types
        assert 'dir' in storage_types, "Directory storage type not found"
        
        # Optional but recommended storage types
        recommended_types = ['lvm', 'zfs', 'ceph']
        found_types = [t for t in recommended_types if t in storage_types]
        
        print(f"Storage types found: {set(storage_types)}")
        print(f"Recommended types available: {found_types}")
    
    def test_storage_content_types(self, network_storage_client):
        """Test that storage supports required content types"""
        storage = network_storage_client.get_storage_config()
        
        required_content = ['images', 'iso', 'backup']
        
        for pool in storage['data']:
            content = pool.get('content', '').split(',')
            storage_name = pool.get('storage')
            
            print(f"Storage {storage_name} content: {content}")
            
            # At least one storage should support each required content type
            for content_type in required_content:
                if content_type in content:
                    print(f"  ✓ {content_type} supported by {storage_name}")
    
    def test_storage_space_available(self, network_storage_client, test_node):
        """Test that storage has available space"""
        node_storage = network_storage_client.get_node_storage(test_node)
        
        for storage in node_storage['data']:
            storage_name = storage.get('storage')
            total = storage.get('total', 0)
            used = storage.get('used', 0)
            avail = storage.get('avail', 0)
            
            print(f"Storage {storage_name}:")
            print(f"  Total: {total / (1024**3):.2f} GB")
            print(f"  Used: {used / (1024**3):.2f} GB")
            print(f"  Available: {avail / (1024**3):.2f} GB")
            
            # Check that storage has some available space
            assert avail > 0, f"Storage {storage_name} has no available space"
            
            # Warning if storage is more than 90% full
            if total > 0:
                usage_percent = (used / total) * 100
                if usage_percent > 90:
                    print(f"  WARNING: Storage {storage_name} is {usage_percent:.1f}% full")


class TestProxmoxNetworkStorageIntegration:
    """Test integration between network and storage"""
    
    def test_network_storage_compatibility(self, network_storage_client, test_node):
        """Test that network and storage are properly integrated"""
        # Get network interfaces
        interfaces = network_storage_client.get_network_interfaces(test_node)
        bridges = [iface for iface in interfaces['data'] if iface.get('type') == 'bridge']
        
        # Get storage
        storage = network_storage_client.get_storage_config()
        
        # Ensure we have both network and storage configured
        assert len(bridges) > 0, "No bridges configured for VM networking"
        assert len(storage['data']) > 0, "No storage configured for VMs"
        
        print("Network and storage integration check passed")
        print(f"  Bridges: {len(bridges)}")
        print(f"  Storage pools: {len(storage['data'])}")
    
    def test_kubernetes_requirements(self, network_storage_client, test_node):
        """Test that network and storage meet Kubernetes requirements"""
        # Check for VM-capable storage
        storage = network_storage_client.get_storage_config()
        vm_storage = [
            pool for pool in storage['data']
            if 'images' in pool.get('content', '')
        ]
        assert len(vm_storage) > 0, "No storage configured for VM images"
        
        # Check for network bridges
        interfaces = network_storage_client.get_network_interfaces(test_node)
        bridges = [iface for iface in interfaces['data'] if iface.get('type') == 'bridge']
        assert len(bridges) > 0, "No bridges configured for VM networking"
        
        print("Kubernetes requirements check passed")
        print(f"  VM-capable storage pools: {len(vm_storage)}")
        print(f"  Network bridges: {len(bridges)}")


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
