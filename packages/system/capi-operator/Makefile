export NAME=capi-operator
export NAMESPACE=cozy-cluster-api
export REPO_NAME=capi-operator
export REPO_URL=https://kubernetes-sigs.github.io/cluster-api-operator
export CHART_NAME=cluster-api-operator 
export CHART_VERSION=0.30.0

CAPI_BRIDGE_VERSION=v1.13.8
CAPI_BRIDGE_URL=https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CAPI_BRIDGE_VERSION)
CAPI_BRIDGE_CORE_SHA256_COMPONENTS=f28647b6c20956d56fd8a678bf477821036bea71f1a1318d9cacdc413c779957
CAPI_BRIDGE_BOOTSTRAP_SHA256_COMPONENTS=775ebb39516fcf17eccaa6f2d157fa763f90d7349f274520c5320cb693d88726
CAPI_BRIDGE_SHA256_METADATA=20ffd682bfc50ab5193aa14ba55222ba390f9204ccf6a0dd2d52c4a3c4fb004f

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

.PHONY: bridge-components
bridge-components:
	mkdir .bridge-tmp
	curl --fail --location --output .bridge-tmp/core-components.yaml $(CAPI_BRIDGE_URL)/core-components.yaml
	curl --fail --location --output .bridge-tmp/bootstrap-components.yaml $(CAPI_BRIDGE_URL)/bootstrap-components.yaml
	curl --fail --location --output .bridge-tmp/metadata.yaml $(CAPI_BRIDGE_URL)/metadata.yaml
	printf '%s  .bridge-tmp/core-components.yaml\n%s  .bridge-tmp/bootstrap-components.yaml\n%s  .bridge-tmp/metadata.yaml\n' $(CAPI_BRIDGE_CORE_SHA256_COMPONENTS) $(CAPI_BRIDGE_BOOTSTRAP_SHA256_COMPONENTS) $(CAPI_BRIDGE_SHA256_METADATA) | sha256sum --check -
	patch --batch .bridge-tmp/core-components.yaml < ../capi-providers-core/patches/0001-add-startup-probe.diff
	mv .bridge-tmp/metadata.yaml files/bridge-metadata.yaml
	gzip --best --no-name --stdout .bridge-tmp/core-components.yaml > files/bridge-core-components.gz
	gzip --best --no-name --stdout .bridge-tmp/bootstrap-components.yaml > files/bridge-bootstrap-components.gz
	rm .bridge-tmp/core-components.yaml .bridge-tmp/bootstrap-components.yaml
	printf '%s\n' $(CAPI_BRIDGE_VERSION) | tee files/bridge-version ../capi-providers-core/files/bridge-version >/dev/null
	if [ -f .bridge-tmp/core-components.yaml.orig ]; then rm .bridge-tmp/core-components.yaml.orig; fi
	rmdir .bridge-tmp
