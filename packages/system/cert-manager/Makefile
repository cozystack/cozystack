export NAME=cert-manager
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add jetstack https://charts.jetstack.io
	helm repo update jetstack
	helm pull jetstack/cert-manager --untar --untardir charts
	mv charts/cert-manager/templates/crd-*.yaml ../cert-manager-crds/templates/
	cp charts/cert-manager/templates/_helpers.tpl ../cert-manager-crds/templates/
