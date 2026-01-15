export NAME=external-secrets-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add external-secrets https://charts.external-secrets.io
	helm repo update external-secrets
	helm pull external-secrets/external-secrets --untar --untardir charts
	rm -rf charts/external-secrets/charts