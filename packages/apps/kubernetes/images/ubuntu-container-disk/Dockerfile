# TODO: Here we use ubuntu:22.04, as guestfish has some network issues running in ubuntu:24.04
FROM ubuntu:22.04 AS guestfish

ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get -y install \
     libguestfs-tools \
     linux-image-generic \
     wget \
     make \
     bash-completion

WORKDIR /build

FROM guestfish AS builder

ARG TARGETOS
ARG TARGETARCH

# noble is a code name for the Ubuntu 24.04 LTS release
RUN wget -O image.img https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-${TARGETARCH}.img --show-progress --output-file /dev/stdout --progress=dot:giga 2>/dev/null

ARG KUBERNETES_VERSION

RUN qemu-img resize image.img 5G \
 && eval "$(guestfish --listen --network)" \
 && guestfish --remote add-drive image.img \
 && guestfish --remote run \
 && guestfish --remote mount /dev/sda1 / \
 && guestfish --remote command "growpart /dev/sda 1 --verbose" \
 && guestfish --remote command "resize2fs /dev/sda1" \
# docker repo
 && guestfish --remote sh "curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg" \
 && guestfish --remote sh 'echo "deb [signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list' \
# kubernetes repo
 && guestfish --remote sh "curl -fsSL https://pkgs.k8s.io/core:/stable:/${KUBERNETES_VERSION}/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg" \
 && guestfish --remote sh "echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${KUBERNETES_VERSION}/deb/ /' | tee /etc/apt/sources.list.d/kubernetes.list" \
 && guestfish --remote command "apt-get check -q" \
# install containerd
 && guestfish --remote command "apt-get update -q" \
 && guestfish --remote command "apt-get install -yq containerd.io" \
# configure containerd
 && guestfish --remote command "mkdir -p /etc/containerd" \
 && guestfish --remote sh "containerd config default | tee /etc/containerd/config.toml" \
 && guestfish --remote command "sed -i '/SystemdCgroup/ s/=.*/= true/' /etc/containerd/config.toml" \
 && guestfish --remote command "containerd config dump >/dev/null" \
# install kubernetes
 && guestfish --remote command "apt-get install -yq kubelet kubeadm" \
# clean apt cache
 && guestfish --remote sh 'apt-get clean && rm -rf /var/lib/apt/lists/*' \
# write system configuration
 && guestfish --remote sh 'printf "%s\n" net.bridge.bridge-nf-call-iptables=1 net.bridge.bridge-nf-call-ip6tables=1 net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1 net.ipv6.conf.all.disable_ipv6=0 net.ipv4.tcp_congestion_control=bbr vm.overcommit_memory=1 kernel.panic=10 kernel.panic_on_oops=1 fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=524288 | tee > /etc/sysctl.d/kubernetes.conf' \
 && guestfish --remote sh 'printf "%s\n" overlay br_netfilter | tee /etc/modules-load.d/kubernetes.conf' \
 && guestfish --remote sh "rm -f /etc/resolv.conf && ln -s ../run/systemd/resolve/stub-resolv.conf /etc/resolv.conf" \
# umount all and exit
 && guestfish --remote umount-all \
 && guestfish --remote exit

FROM scratch
COPY --from=builder /build/image.img /disk/image.qcow2
