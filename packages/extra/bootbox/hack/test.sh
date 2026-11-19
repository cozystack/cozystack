apk add iptables iproute2 qemu-system-x86_64 qemu-img

iptables -t nat -D POSTROUTING -s 10.8.2.0/24 ! -d 10.8.2.0/24 -j MASQUERADE 2>/dev/null || true
iptables -t nat -A POSTROUTING -s 10.8.2.0/24 ! -d 10.8.2.0/24 -j MASQUERADE

ip link del tap0 2>/dev/null || true
ip tuntap add dev tap0 mode tap
ip link set tap0 up
ip addr add 10.8.2.1/24 dev tap0


rm -f data.img
qemu-img create data.img 100G

qemu-system-x86_64 -machine type=pc,accel=kvm -cpu host -smp 4 -m 8192 \
  -device virtio-net,netdev=net0,mac=d6:fa:af:52:25:93 -netdev tap,id=net0,ifname=tap0,script=no,downscript=no \
  -drive file=data.img,if=virtio,format=raw \
  -nographic
