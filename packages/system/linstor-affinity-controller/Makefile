export NAME=linstor-affinity-controller
export NAMESPACE=cozy-linstor

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/linstor-affinity-controller --untar --untardir charts
