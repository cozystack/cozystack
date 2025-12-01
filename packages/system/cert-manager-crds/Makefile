include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add jetstack https://charts.jetstack.io
	helm repo update jetstack
	helm pull jetstack/cert-manager --untar --untardir charts
	rm -f -- `find charts/cert-manager/templates -maxdepth 1 -mindepth 1 | grep -v 'crds.yaml\|_helpers.tpl'`
