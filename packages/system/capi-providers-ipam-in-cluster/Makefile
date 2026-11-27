export NAME=capi-providers-ipam-in-cluster
export NAMESPACE=cozy-cluster-api

CAIP_VERSION=v1.1.1
CAIP_URL=https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster/releases/download/$(CAIP_VERSION)
# SHA-256 of the two release assets at CAIP_VERSION. A release tag can be moved;
# these cannot. Bumping the version means recording the new sums here.
CAIP_SHA256_COMPONENTS=8a0656a39d9e85ee07219702254669b7e543d09957e86315f3d20b4bf6aebfa5
CAIP_SHA256_METADATA=eb280ab315f478324973a7b65739a655d57e3c22a1356efd9f5127534d3502b7

include ../../../hack/package.mk

# See the note in capi-providers-infraprovider-proxmox/Makefile: gzip -n keeps
# the committed artifact reproducible.
.PHONY: components
components:
	curl -fL -o files/ipam-components.yaml $(CAIP_URL)/ipam-components.yaml
	curl -fL -o files/metadata.yaml $(CAIP_URL)/metadata.yaml
	printf '%s  files/ipam-components.yaml\n%s  files/metadata.yaml\n' $(CAIP_SHA256_COMPONENTS) $(CAIP_SHA256_METADATA) | sha256sum -c -
	gzip -9 -n -c files/ipam-components.yaml > files/components.gz
