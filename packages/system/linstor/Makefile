export NAME=linstor
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

LINSTOR_VERSION ?= 1.32.3
LINSTOR_CSI_VERSION ?= v1.10.5

image: image-piraeus-server image-linstor-csi

image-piraeus-server:
	docker buildx build images/piraeus-server \
		--build-arg LINSTOR_VERSION=$(LINSTOR_VERSION) \
		--build-arg K8S_AWAIT_ELECTION_VERSION=v0.4.2 \
		--tag $(REGISTRY)/piraeus-server:$(call settag,$(LINSTOR_VERSION)) \
		--tag $(REGISTRY)/piraeus-server:$(call settag,$(LINSTOR_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/piraeus-server:latest \
		--cache-to type=inline \
		--metadata-file images/piraeus-server.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/piraeus-server" \
		yq -i '.piraeusServer.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(LINSTOR_VERSION))@$$(yq e '."containerimage.digest"' images/piraeus-server.json -o json -r)" \
		yq -i '.piraeusServer.image.tag = strenv(TAG)' values.yaml
	rm -f images/piraeus-server.json

image-linstor-csi:
	docker buildx build images/linstor-csi \
		--build-arg VERSION=$(LINSTOR_CSI_VERSION) \
		--tag $(REGISTRY)/linstor-csi:$(call settag,$(LINSTOR_CSI_VERSION)) \
		--tag $(REGISTRY)/linstor-csi:$(call settag,$(LINSTOR_CSI_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/linstor-csi:latest \
		--cache-to type=inline \
		--metadata-file images/linstor-csi.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/linstor-csi" \
		yq -i '.linstorCSI.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(LINSTOR_CSI_VERSION))@$$(yq e '."containerimage.digest"' images/linstor-csi.json -o json -r)" \
		yq -i '.linstorCSI.image.tag = strenv(TAG)' values.yaml
	rm -f images/linstor-csi.json
