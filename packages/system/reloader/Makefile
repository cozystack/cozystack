export NAME=reloader
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add stakater https://stakater.github.io/stakater-charts
	helm repo update
	helm pull stakater/reloader --untar --untardir charts
