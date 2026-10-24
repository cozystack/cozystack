export NAME=linstor
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

LINSTOR_VERSION ?= 1.33.3
# The golden-image clone guard in packages/apps/vm-disk/templates/dv.yaml
# reads allowRemoteVolumeAccess and layerList the way this release does and
# has to be re-checked against the new one.
LINSTOR_CSI_VERSION ?= v1.11.2

# blockstor's CRDs are copied unmodified from the release its images are pinned
# to, so the schema and the binaries that read it always come from one tag.
# Bump the three blockstor image tags in values.yaml first, then run this.
BLOCKSTOR_VERSION ?= $(shell yq e '.blockstorController.image.tag' values.yaml | cut -d@ -f1)

update-blockstor-crds:
	tmpdir=$$(mktemp -d) && \
	{ curl --fail --silent --show-error --location -o "$$tmpdir/source.tar.gz" \
		"https://github.com/cozystack/blockstor/archive/refs/tags/v$(BLOCKSTOR_VERSION).tar.gz" && \
		mkdir "$$tmpdir/src" && tar -xzf "$$tmpdir/source.tar.gz" --strip-components=1 -C "$$tmpdir/src" && \
		ls "$$tmpdir"/src/config/crd/bases/blockstor.org_*.yaml >/dev/null || { rm -rf "$$tmpdir"; exit 1; }; } && \
	rm -f definitions/blockstor.org_*.yaml && \
	cp "$$tmpdir"/src/config/crd/bases/blockstor.org_*.yaml definitions/ && \
	rm -rf "$$tmpdir"

test:
	helm unittest .
	python3 -m unittest discover -s tests -p 'test_*.py' -v

image: image-piraeus-server image-linstor-csi

image-piraeus-server:
	docker buildx build images/piraeus-server \
		--build-arg LINSTOR_VERSION=$(LINSTOR_VERSION) \
		--build-arg K8S_AWAIT_ELECTION_VERSION=v0.4.2 \
		$(call image-tags,piraeus-server,$(LINSTOR_VERSION)) \
		$(call cache-args,piraeus-server) \
		--metadata-file images/piraeus-server.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/piraeus-server" \
		yq -i '.piraeusServer.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/piraeus-server.json -o json -r)" \
		yq -i '.piraeusServer.image.tag = strenv(TAG)' values.yaml
	rm -f images/piraeus-server.json

image-linstor-csi:
	docker buildx build images/linstor-csi \
		--build-arg VERSION=$(LINSTOR_CSI_VERSION) \
		$(call image-tags,linstor-csi,$(LINSTOR_CSI_VERSION)) \
		$(call cache-args,linstor-csi) \
		--metadata-file images/linstor-csi.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/linstor-csi" \
		yq -i '.linstorCSI.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/linstor-csi.json -o json -r)" \
		yq -i '.linstorCSI.image.tag = strenv(TAG)' values.yaml
	rm -f images/linstor-csi.json
