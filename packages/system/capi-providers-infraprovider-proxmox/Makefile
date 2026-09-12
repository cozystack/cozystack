export NAME=capi-providers-infraprovider-proxmox
export NAMESPACE=cozy-cluster-api

CAPMOX_VERSION=v0.7.7
CAPMOX_URL=https://github.com/ionos-cloud/cluster-api-provider-proxmox/releases/download/$(CAPMOX_VERSION)

include ../../../hack/package.mk

# Refresh the vendored provider manifests. Bump CAPMOX_VERSION, run this, and
# update the version in templates/{configmaps,providers}.yaml to match.
#
# gzip -n matters: without it the header carries a timestamp, so every
# regeneration produces a different blob for identical input and the committed
# artifact looks changed when it is not.
.PHONY: components
components:
	curl -fL -o files/infrastructure-components.yaml $(CAPMOX_URL)/infrastructure-components.yaml
	curl -fL -o files/metadata.yaml $(CAPMOX_URL)/metadata.yaml
	gzip -9 -n -c files/infrastructure-components.yaml > files/components.gz
