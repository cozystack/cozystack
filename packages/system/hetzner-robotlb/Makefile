export NAME=hetzner-robotlb
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/treetscom/charts/robotlb --untar --untardir charts
