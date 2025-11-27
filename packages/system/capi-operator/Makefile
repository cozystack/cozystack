export NAME=capi-operator
export NAMESPACE=cozy-cluster-api
export REPO_NAME=capi-operator
export REPO_URL=https://kubernetes-sigs.github.io/cluster-api-operator
export CHART_NAME=cluster-api-operator 
export CHART_VERSION=^0.19

include ../../../hack/package.mk

update: clean capi-operator-update
	rm -rf charts/cluster-api-operator/charts/
