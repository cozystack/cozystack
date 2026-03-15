#!/bin/bash

# Proxmox Server Setup Script
# This script prepares Proxmox server for Kubernetes worker node

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
PROXMOX_HOST=""
PROXMOX_USER="root@pam"
PROXMOX_PASSWORD=""
PROXMOX_PORT="8006"
INSECURE=false
NODE_NAME="proxmox-worker"
POD_CIDR="10.244.0.0/16"
SERVICE_CIDR="10.96.0.0/12"
CLUSTER_DOMAIN="cluster.local"

# Function to print colored output
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    -h, --host HOST              Proxmox server hostname or IP
    -u, --user USER              Proxmox username (default: root@pam)
    -p, --password PASSWORD      Proxmox password
    -P, --port PORT              Proxmox port (default: 8006)
    -k, --insecure               Allow insecure connections
    -n, --node-name NAME         Node name (default: proxmox-worker)
    --pod-cidr CIDR              Pod CIDR (default: 10.244.0.0/16)
    --service-cidr CIDR          Service CIDR (default: 10.96.0.0/12)
    --cluster-domain DOMAIN      Cluster domain (default: cluster.local)
    --help                       Show this help message

Examples:
    $0 -h proxmox.example.com -p mypassword
    $0 -h 192.168.1.100 -u admin@pam -p mypassword -k

EOF
}

# Function to check prerequisites
check_prerequisites() {
    print_info "Checking prerequisites..."
    
    # Check if required tools are available
    local tools=("curl" "wget" "gpg" "apt-transport-https" "ca-certificates")
    for tool in "${tools[@]}"; do
        if ! command -v "$tool" &> /dev/null; then
            print_error "$tool is not installed"
            exit 1
        fi
    done
    
    print_success "Prerequisites check passed"
}

# Function to install containerd
install_containerd() {
    print_info "Installing containerd..."
    
    # Add Docker repository
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
    
    # Update package list
    apt-get update
    
    # Install containerd
    apt-get install -y containerd.io
    
    print_success "Containerd installed successfully"
}

# Function to configure containerd
configure_containerd() {
    print_info "Configuring containerd..."
    
    # Create containerd config directory
    mkdir -p /etc/containerd
    
    # Backup existing config
    if [ -f /etc/containerd/config.toml ]; then
        cp /etc/containerd/config.toml /etc/containerd/config.toml.bak
    fi
    
    # Generate containerd config
    cat > /etc/containerd/config.toml << 'EOF'
version = 2
root = "/var/lib/containerd"
state = "/run/containerd"

[grpc]
  address = "/run/containerd/containerd.sock"
  uid = 0
  gid = 0
  max_recv_message_size = 16777216
  max_send_message_size = 16777216

[ttrpc]
  address = "/run/containerd/containerd.sock.ttrpc"
  uid = 0
  gid = 0

[debug]
  address = ""
  uid = 0
  gid = 0
  level = ""

[metrics]
  address = ""
  grpc_histogram = false

[cgroup]
  path = ""

[timeouts]
  "io.containerd.timeout.shim.cleanup" = "5s"
  "io.containerd.timeout.shim.load" = "5s"
  "io.containerd.timeout.shim.shutdown" = "3s"
  "io.containerd.timeout.task.state" = "2s"

[plugins]
  [plugins."io.containerd.gc.v1.scheduler"]
    pause_threshold = 0.02
    deletion_threshold = 0
    mutation_threshold = 100
    schedule_delay = "0s"
    startup_delay = "100ms"
  [plugins."io.containerd.grpc.v1.cri"]
    disable_tcp_service = true
    stream_server_address = "127.0.0.1"
    stream_server_port = "0"
    stream_idle_timeout = "4h0m0s"
    enable_selinux = false
    selinux_category_range = 1024
    sandbox_image = "registry.k8s.io/pause:3.9"
    stats_collect_period = 10
    systemd_cgroup = false
    enable_tls_streaming = false
    max_container_log_line_size = 16384
    disable_cgroup = false
    disable_apparmor = false
    restrict_oom_score_adj = false
    max_concurrent_downloads = 3
    disable_proc_mount = false
    unset_seccomp_profile = ""
    tolerate_missing_hugetlb_controller = true
    disable_hugetlb_controller = true
    ignore_image_defined_volumes = false
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "overlayfs"
      default_runtime_name = "runc"
      no_pivot = false
      disable_snapshot_annotations = true
      discard_unpacked_layers = false
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes]
        [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
          runtime_type = "io.containerd.runc.v2"
          runtime_engine = ""
          runtime_root = ""
          privileged_without_host_devices = false
          base_runtime_spec = ""
          [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
            SystemdCgroup = true
    [plugins."io.containerd.grpc.v1.cri".cni]
      bin_dir = "/opt/cni/bin"
      conf_dir = "/etc/cni/net.d"
      max_conf_num = 1
      conf_template = ""
    [plugins."io.containerd.grpc.v1.cri".registry]
      [plugins."io.containerd.grpc.v1.cri".registry.mirrors]
      [plugins."io.containerd.grpc.v1.cri".registry.configs]
    [plugins."io.containerd.grpc.v1.cri".image_decryption]
      key_model = ""
    [plugins."io.containerd.grpc.v1.cri".x509_key_pair_streaming]
      tls_cert_file = ""
      tls_private_key_file = ""
EOF
    
    # Start and enable containerd
    systemctl daemon-reload
    systemctl enable containerd
    systemctl start containerd
    
    print_success "Containerd configured successfully"
}

# Function to install Kubernetes tools
install_kubernetes_tools() {
    print_info "Installing Kubernetes tools..."
    
    # Add Kubernetes repository
    curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | gpg --dearmor -o /usr/share/keyrings/kubernetes-archive-keyring.gpg
    echo "deb [signed-by=/usr/share/keyrings/kubernetes-archive-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list
    
    # Update package list
    apt-get update
    
    # Install Kubernetes tools
    apt-get install -y kubelet kubeadm kubectl
    
    # Hold packages to prevent automatic updates
    apt-mark hold kubelet kubeadm kubectl
    
    print_success "Kubernetes tools installed successfully"
}

# Function to configure kubelet
configure_kubelet() {
    print_info "Configuring kubelet..."
    
    # Create kubelet config directory
    mkdir -p /etc/kubernetes
    
    # Generate kubelet configuration
    cat > /etc/kubernetes/kubelet-config.yaml << EOF
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    enabled: true
    cacheTTL: 0s
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 0s
    cacheUnauthorizedTTL: 0s
clusterDomain: $CLUSTER_DOMAIN
clusterDNS:
  - $(echo $SERVICE_CIDR | cut -d'/' -f1 | sed 's/0$/10/')
cpuManagerPolicy: none
cgroupDriver: systemd
containerRuntimeEndpoint: unix:///run/containerd/containerd.sock
evictionHard:
  imagefs.available: 15%
  memory.available: 100Mi
  nodefs.available: 10%
  nodefs.inodesFree: 5%
evictionSoft:
  imagefs.available: 15%
  memory.available: 100Mi
  nodefs.available: 10%
  nodefs.inodesFree: 5%
evictionSoftGracePeriod:
  imagefs.available: 1m
  memory.available: 1m
  nodefs.available: 1m
  nodefs.inodesFree: 1m
evictionMaxPodGracePeriod: 0
evictionPressureTransitionPeriod: 0s
fileCheckFrequency: 20s
healthzBindAddress: 127.0.0.1
healthzPort: 10248
httpCheckFrequency: 20s
imageMinimumGCAge: 0s
imageGCHighThresholdPercent: 85
imageGCLowThresholdPercent: 80
logging:
  format: json
  flushFrequency: 0
  options:
    json:
      infoBufferSize: "0"
  verbosity: 0
memorySwap: {}
nodeStatusReportFrequency: 0s
nodeStatusUpdateFrequency: 0s
rotateCertificates: true
runtimeRequestTimeout: 2m
staticPodPath: /etc/kubernetes/manifests
streamingConnectionIdleTimeout: 4h0m0s
syncFrequency: 1m0s
volumeStatsAggPeriod: 1m0s
serverTLSBootstrap: true
EOF
    
    print_success "Kubelet configured successfully"
}

# Function to configure system
configure_system() {
    print_info "Configuring system..."
    
    # Disable swap
    swapoff -a
    sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab
    
    # Enable IP forwarding
    echo 'net.ipv4.ip_forward = 1' >> /etc/sysctl.conf
    echo 'net.bridge.bridge-nf-call-iptables = 1' >> /etc/sysctl.conf
    echo 'net.bridge.bridge-nf-call-ip6tables = 1' >> /etc/sysctl.conf
    sysctl --system
    
    # Load required kernel modules
    cat > /etc/modules-load.d/k8s.conf << EOF
br_netfilter
overlay
EOF
    
    modprobe br_netfilter
    modprobe overlay
    
    print_success "System configured successfully"
}

# Function to show post-installation information
show_post_install_info() {
    print_info "Post-installation information:"
    echo ""
    echo "Node name: $NODE_NAME"
    echo "Pod CIDR: $POD_CIDR"
    echo "Service CIDR: $SERVICE_CIDR"
    echo "Cluster domain: $CLUSTER_DOMAIN"
    echo ""
    echo "To join the cluster, you need:"
    echo "1. Kubernetes control plane endpoint"
    echo "2. Join token"
    echo "3. CA certificate hash"
    echo ""
    echo "Example kubeadm join command:"
    echo "kubeadm join <control-plane-endpoint>:6443 --token <token> --discovery-token-ca-cert-hash sha256:<hash>"
    echo ""
    echo "To start kubelet:"
    echo "systemctl enable kubelet"
    echo "systemctl start kubelet"
    echo ""
}

# Main function
main() {
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--host)
                PROXMOX_HOST="$2"
                shift 2
                ;;
            -u|--user)
                PROXMOX_USER="$2"
                shift 2
                ;;
            -p|--password)
                PROXMOX_PASSWORD="$2"
                shift 2
                ;;
            -P|--port)
                PROXMOX_PORT="$2"
                shift 2
                ;;
            -k|--insecure)
                INSECURE=true
                shift
                ;;
            -n|--node-name)
                NODE_NAME="$2"
                shift 2
                ;;
            --pod-cidr)
                POD_CIDR="$2"
                shift 2
                ;;
            --service-cidr)
                SERVICE_CIDR="$2"
                shift 2
                ;;
            --cluster-domain)
                CLUSTER_DOMAIN="$2"
                shift 2
                ;;
            --help)
                show_usage
                exit 0
                ;;
            *)
                print_error "Unknown option: $1"
                show_usage
                exit 1
                ;;
        esac
    done
    
    # Validate required parameters
    if [ -z "$PROXMOX_HOST" ]; then
        print_error "Proxmox host is required"
        show_usage
        exit 1
    fi
    
    print_info "Starting Proxmox server setup..."
    print_info "Host: $PROXMOX_HOST"
    print_info "User: $PROXMOX_USER"
    print_info "Port: $PROXMOX_PORT"
    print_info "Insecure: $INSECURE"
    print_info "Node name: $NODE_NAME"
    print_info "Pod CIDR: $POD_CIDR"
    print_info "Service CIDR: $SERVICE_CIDR"
    print_info "Cluster domain: $CLUSTER_DOMAIN"
    echo ""
    
    check_prerequisites
    configure_system
    install_containerd
    configure_containerd
    install_kubernetes_tools
    configure_kubelet
    show_post_install_info
    
    print_success "Proxmox server setup completed!"
}

# Run main function
main "$@"
