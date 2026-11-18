FROM quay.io/poseidon/matchbox:v0.11.0@sha256:06bcdae85335fd00e8277b007b55cfb49d96a0114628c0f70db2b92b079d246a

# Every image carries both arches' assets, not only TARGETARCH's: the node
# matchbox runs on says nothing about the machines it boots. The arm64 group
# picks its assets by the arch iPXE reports.
COPY _out/assets/initramfs-metal-amd64.xz /var/lib/matchbox/assets/initramfs.xz
COPY _out/assets/kernel-amd64 /var/lib/matchbox/assets/vmlinuz
COPY _out/assets/initramfs-metal-arm64.xz /var/lib/matchbox/assets/initramfs-arm64.xz
COPY _out/assets/kernel-arm64 /var/lib/matchbox/assets/vmlinuz-arm64
COPY packages/core/talos/images/matchbox/groups /var/lib/matchbox/groups
COPY packages/core/talos/images/matchbox/profiles /var/lib/matchbox/profiles
