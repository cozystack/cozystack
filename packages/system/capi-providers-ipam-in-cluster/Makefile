export NAME=capi-providers-ipam-in-cluster
export NAMESPACE=cozy-cluster-api

CAIP_VERSION=v1.0.3
CAIP_URL=https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster/releases/download/$(CAIP_VERSION)
# SHA-256 of the two release assets at CAIP_VERSION. A release tag can be moved;
# these cannot. Bumping the version means recording the new sums here.
CAIP_SHA256_COMPONENTS=e3247a458e66e1eeca8bda05a6dfaa882dae29785072f41e7cf071ff8c70f695
CAIP_SHA256_METADATA=dc1ebd036645e0e87e99dab275c312cf8f8f38aa68a12bc01ad2b1dd1d6bf20e

include ../../../hack/package.mk

# See the note in capi-providers-infraprovider-proxmox/Makefile: gzip -n keeps
# the committed artifact reproducible.
.PHONY: components
components:
	curl -fL -o files/ipam-components.yaml $(CAIP_URL)/ipam-components.yaml
	curl -fL -o files/metadata.yaml $(CAIP_URL)/metadata.yaml
	printf '%s  files/ipam-components.yaml\n%s  files/metadata.yaml\n' $(CAIP_SHA256_COMPONENTS) $(CAIP_SHA256_METADATA) | sha256sum -c -
	gzip -9 -n -c files/ipam-components.yaml > files/components.gz
