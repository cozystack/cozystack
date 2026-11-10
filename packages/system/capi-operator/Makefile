export NAME=capi-operator
export NAMESPACE=cozy-cluster-api
export REPO_NAME=capi-operator
export REPO_URL=https://kubernetes-sigs.github.io/cluster-api-operator
export CHART_NAME=cluster-api-operator 
export CHART_VERSION=0.30.0

include ../../../hack/package.mk

update: clean capi-operator-update
	rm -rf charts/cluster-api-operator/charts/
	# The upstream chart has no value for --sync-period; values.yaml explains
	# why we set it.
	patch -p1 < patches/sync-period.diff
	patch -p1 < patches/legacy-feature-gates.diff
	patch -p1 < patches/provider-spec-guards.diff
	patch -p1 < patches/fetch-config-precedence.diff
	patch -p1 < patches/profiling-independent.diff
	patch -p1 < patches/fetch-config-types.diff
	patch -p1 < patches/provider-string-types.diff
