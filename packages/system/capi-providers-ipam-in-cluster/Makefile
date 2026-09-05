export NAME=capi-providers-ipam-in-cluster
export NAMESPACE=cozy-cluster-api

CAIP_VERSION=v1.0.3
CAIP_URL=https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster/releases/download/$(CAIP_VERSION)

include ../../../hack/package.mk

# See the note in capi-providers-infraprovider-proxmox/Makefile: gzip -n keeps
# the committed artifact reproducible.
.PHONY: components
components:
	curl -fL -o files/ipam-components.yaml $(CAIP_URL)/ipam-components.yaml
	curl -fL -o files/metadata.yaml $(CAIP_URL)/metadata.yaml
	gzip -9 -n -c files/ipam-components.yaml > files/components.gz
