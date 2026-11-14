export NAME=capi-providers-core
export NAMESPACE=cozy-cluster-api

CAPI_VERSION=v1.14.4
CAPI_URL=https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CAPI_VERSION)
# SHA-256 of the two release assets at CAPI_VERSION, as downloaded. A release tag
# can be moved; these cannot. Bumping the version means recording the new sums
# here. files/core-components.yaml carries patches/ on top, so its own sum differs.
CAPI_SHA256_COMPONENTS=0030264eb0a0d90ebacc9da8f6bbb717bee32e45ffadc4a74784fb5ad7b9bb0b
CAPI_SHA256_METADATA=c470906f551ac3e3e9aedc9a3e733b5a5994428693df43e95f65ea67c035f9fe

include ../../../hack/package.mk

# Keep Chart.yaml appVersion equal to CAPI_VERSION: it selects the provider
# version and ConfigMap that the operator installs.
# patches/ is applied on top of the verified download. See the note in
# capi-providers-infraprovider-proxmox/Makefile on gzip -n.
.PHONY: components
components:
	@command -v patch >/dev/null || { echo "patch(1) required"; exit 1; }
	rm -rf .components-tmp
	mkdir .components-tmp
	curl -fL -o .components-tmp/core-components.yaml $(CAPI_URL)/core-components.yaml
	curl -fL -o .components-tmp/metadata.yaml $(CAPI_URL)/metadata.yaml
	printf '%s  .components-tmp/core-components.yaml\n%s  .components-tmp/metadata.yaml\n' $(CAPI_SHA256_COMPONENTS) $(CAPI_SHA256_METADATA) | sha256sum -c -
	for p in patches/*.diff; do \
		[ -e "$$p" ] || continue; \
		(cd .components-tmp && patch core-components.yaml < ../$$p) || exit 1; \
	done
	mv .components-tmp/core-components.yaml files/core-components.yaml
	mv .components-tmp/metadata.yaml files/metadata.yaml
	gzip -9 -n -c files/core-components.yaml > files/components.gz
	rm -rf .components-tmp
