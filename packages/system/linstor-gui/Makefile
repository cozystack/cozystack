export NAME=linstor-gui
export NAMESPACE=cozy-linstor

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

LINSTOR_GUI_VERSION ?= 2.3.0

.PHONY: image image-linstor-gui test

image: image-linstor-gui

image-linstor-gui:
	docker buildx build images/linstor-gui \
		--build-arg LINSTOR_GUI_VERSION=$(LINSTOR_GUI_VERSION) \
		--tag $(REGISTRY)/linstor-gui:$(call settag,$(LINSTOR_GUI_VERSION)) \
		--tag $(REGISTRY)/linstor-gui:$(call settag,$(LINSTOR_GUI_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/linstor-gui:latest \
		--cache-to type=inline \
		--metadata-file images/linstor-gui.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/linstor-gui" \
		yq -i '.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(LINSTOR_GUI_VERSION))@$$(yq e '."containerimage.digest"' images/linstor-gui.json -o json -r)" \
		yq -i '.image.tag = strenv(TAG)' values.yaml
	rm -f images/linstor-gui.json

test:
	helm unittest .
