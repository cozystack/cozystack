FROM quay.io/poseidon/matchbox:v0.10.0

COPY _out/assets/initramfs-metal-amd64.xz /var/lib/matchbox/assets/initramfs.xz
COPY _out/assets/kernel-amd64 /var/lib/matchbox/assets/vmlinuz
COPY packages/core/talos/images/matchbox/groups /var/lib/matchbox/groups
COPY packages/core/talos/images/matchbox/profiles /var/lib/matchbox/profiles
