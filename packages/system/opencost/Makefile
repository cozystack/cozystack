export NAME=opencost
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add opencost-charts https://opencost.github.io/opencost-helm-chart
	helm repo update opencost-charts
	helm pull opencost-charts/opencost --untar --untardir charts
