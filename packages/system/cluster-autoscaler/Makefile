export NAME=cluster-autoscaler
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add autoscaler https://kubernetes.github.io/autoscaler
	helm repo update autoscaler
	helm pull autoscaler/cluster-autoscaler --untar --untardir charts
