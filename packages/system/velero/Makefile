export NAME=velero
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	# Velero
	helm repo add tanzu https://vmware-tanzu.github.io/helm-charts
	helm repo update tanzu
	helm pull tanzu/velero --untar --untardir charts
