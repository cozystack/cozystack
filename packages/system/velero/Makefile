export NAME=velero
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	# Velero
	helm repo add tanzu https://vmware-tanzu.github.io/helm-charts
	helm repo update tanzu
	helm pull tanzu/velero --untar --untardir charts --version 12.0.3
