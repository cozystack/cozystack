export NAME=harbor-system
export NAMESPACE=cozy-system

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add harbor https://helm.goharbor.io
	helm repo update harbor
	helm pull harbor/harbor --untar --untardir charts
