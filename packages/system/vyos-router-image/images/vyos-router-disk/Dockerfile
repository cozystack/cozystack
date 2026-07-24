# KubeVirt containerDisk wrapping the built VyOS router qcow2.
#
# `FROM scratch` + the disk at /disk/ is the KubeVirt containerDisk convention;
# CDI's `source.registry` importer (used by the vm-default-images vyos-router
# entry) extracts the disk from /disk/. Publishing the qcow2 as an OCI image this
# way — rather than a bare `oras` blob like the Talos nocloud disk — is what makes
# it CDI-consumable AND digest-pinnable (a plain HTTP import cannot verify a
# sha256). The build context is the repo root (see the Makefile), so the qcow2 is
# referenced from _out/assets where hack/build-qcow2.sh drops it.
FROM scratch
ADD _out/assets/vyos-router-amd64.qcow2 /disk/vyos-router.qcow2
