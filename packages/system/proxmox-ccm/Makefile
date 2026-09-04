export NAME=proxmox-ccm
export NAMESPACE=kube-system

CHART_VERSION=0.2.30

include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/sergelogvinov/charts/proxmox-cloud-controller-manager \
	  --version $(CHART_VERSION) --untar --untardir charts
