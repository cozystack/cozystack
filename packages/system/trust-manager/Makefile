export NAME=trust-manager
export NAMESPACE=cozy-cert-manager

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add jetstack https://charts.jetstack.io
	helm repo update jetstack
	helm pull jetstack/trust-manager --version v0.22.1 --untar --untardir charts
	mkdir -p ../trust-manager-crds/templates
	rm -rf ../trust-manager-crds/templates/*
	mv charts/trust-manager/templates/crd-*.yaml ../trust-manager-crds/templates/
	cp charts/trust-manager/templates/_helpers.tpl ../trust-manager-crds/templates/
