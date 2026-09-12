export NAME=proxmox-csi
export NAMESPACE=csi-proxmox

# Pinned deliberately. The chart carries the CSI driver image, and a driver
# newer than the Proxmox API it talks to fails at volume attach rather than at
# install, so the pair is bumped together and tested, not tracked.
CHART_VERSION=0.5.12

include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/sergelogvinov/charts/proxmox-csi-plugin \
	  --version $(CHART_VERSION) --untar --untardir charts
