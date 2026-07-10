FROM quay.io/poseidon/matchbox:v0.11.0@sha256:06bcdae85335fd00e8277b007b55cfb49d96a0114628c0f70db2b92b079d246a

COPY _out/assets/initramfs-metal-amd64.xz /var/lib/matchbox/assets/initramfs.xz
COPY _out/assets/kernel-amd64 /var/lib/matchbox/assets/vmlinuz
COPY packages/core/talos/images/matchbox/groups /var/lib/matchbox/groups
COPY packages/core/talos/images/matchbox/profiles /var/lib/matchbox/profiles
