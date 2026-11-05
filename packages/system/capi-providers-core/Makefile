export NAME=capi-providers-core
export NAMESPACE=cozy-cluster-api

CAPI_VERSION=v1.13.7
CAPI_URL=https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CAPI_VERSION)
# SHA-256 of the two release assets at CAPI_VERSION, as downloaded. A release tag
# can be moved; these cannot. Bumping the version means recording the new sums
# here. files/core-components.yaml carries patches/ on top, so its own sum differs.
CAPI_SHA256_COMPONENTS=268007c14fd9c6e5e790dc5274ab2befd2bab02761261d2860dc47756a7a20f2
CAPI_SHA256_METADATA=20ffd682bfc50ab5193aa14ba55222ba390f9204ccf6a0dd2d52c4a3c4fb004f

include ../../../hack/package.mk

# Refresh the vendored core manifests. Bump CAPI_VERSION and the two sums, run
# this, and update the version in templates/{configmaps,providers}.yaml to match.
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
