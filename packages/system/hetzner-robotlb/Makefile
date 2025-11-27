export NAME=hetzner-robotlb
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/intreecom/charts/robotlb --untar --untardir charts
