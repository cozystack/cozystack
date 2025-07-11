export NAME=hetzner-robotlb
export NAMESPACE=kube-system

include ../../../scripts/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/intreecom/charts/robotlb --untar --untardir charts
