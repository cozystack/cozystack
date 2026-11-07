export NAME=capi-providers-infraprovider-proxmox
export NAMESPACE=cozy-cluster-api

CAPMOX_VERSION=v0.9.2
CAPMOX_URL=https://github.com/ionos-cloud/cluster-api-provider-proxmox/releases/download/$(CAPMOX_VERSION)
# SHA-256 of the two release assets at CAPMOX_VERSION. A release tag can be moved;
# these cannot. Bumping the version means recording the new sums here.
CAPMOX_SHA256_COMPONENTS=6817322435b42cfda07a8f94006bacb709afb83445ce24545af253c7904755d8
CAPMOX_SHA256_METADATA=df2916a79503ac053f6a9866a8de025e780e24d6d472f00f59f84898b5a1c572

include ../../../hack/package.mk

# Refresh the vendored provider manifests. Bump CAPMOX_VERSION and the two sums,
# run this, and update the version in templates/{configmaps,providers}.yaml to
# match.
#
# gzip -n matters: without it the header carries a timestamp, so every
# regeneration produces a different blob for identical input and the committed
# artifact looks changed when it is not.
.PHONY: components
components:
	curl -fL -o files/infrastructure-components.yaml $(CAPMOX_URL)/infrastructure-components.yaml
	curl -fL -o files/metadata.yaml $(CAPMOX_URL)/metadata.yaml
	printf '%s  files/infrastructure-components.yaml\n%s  files/metadata.yaml\n' $(CAPMOX_SHA256_COMPONENTS) $(CAPMOX_SHA256_METADATA) | sha256sum -c -
	gzip -9 -n -c files/infrastructure-components.yaml > files/components.gz
