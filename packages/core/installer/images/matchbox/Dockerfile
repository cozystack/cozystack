FROM quay.io/poseidon/matchbox:v0.10.0

ARG TARGETARCH

COPY _out/assets/initramfs-metal-$TARGETARCH.xz /var/lib/matchbox/assets/initramfs.xz
COPY _out/assets/kernel-$TARGETARCH /var/lib/matchbox/assets/vmlinuz
COPY packages/core/installer/images/matchbox/groups /var/lib/matchbox/groups
COPY packages/core/installer/images/matchbox/profiles /var/lib/matchbox/profiles
