export NAME=metrics-server
export NAMESPACE=cozy-monitoring

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
	helm repo update metrics-server
	helm pull metrics-server/metrics-server --untar --untardir charts
