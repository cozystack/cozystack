NAME=talos
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer.yaml)

include ../../../hack/common-envs.mk

update:
	hack/gen-profiles.sh

image: image-matchbox image-talos

image-talos:
	test -f ../../../_out/assets/installer-amd64.tar || make talos-installer
	skopeo copy docker-archive:../../../_out/assets/installer-amd64.tar docker://$(REGISTRY)/talos:$(call settag,$(TALOS_VERSION))

image-matchbox:
	test -f ../../../_out/assets/kernel-amd64 || make talos-kernel
	test -f ../../../_out/assets/initramfs-metal-amd64.xz || make talos-initramfs
	docker buildx build -f images/matchbox/Dockerfile ../../.. \
		--tag $(REGISTRY)/matchbox:$(call settag,$(TAG)) \
		--tag $(REGISTRY)/matchbox:$(call settag,$(TALOS_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/matchbox:latest \
		--cache-to type=inline \
		--metadata-file images/matchbox.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/matchbox:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/matchbox.json -o json -r)" \
		> ../../extra/bootbox/images/matchbox.tag
	rm -f images/matchbox.json

assets: talos-iso talos-nocloud talos-metal talos-kernel talos-initramfs

talos-initramfs:
	test -f ../../../_out/assets/initramfs-metal-amd64.xz || $(MAKE) build-talos-initramfs

talos-kernel:
	test -f ../../../_out/assets/kernel-amd64 || $(MAKE) build-talos-kernel

talos-installer:
	test -f ../../../_out/assets/installer-amd64.tar || $(MAKE) build-talos-installer

talos-iso:
	test -f ../../../_out/assets/metal-amd64.iso || $(MAKE) build-talos-iso

talos-nocloud:
	test -f ../../../_out/assets/nocloud-amd64.raw.xz || $(MAKE) build-talos-nocloud

talos-metal:
	test -f ../../../_out/assets/metal-amd64.raw.xz || $(MAKE) build-talos-metal

build-talos-initramfs build-talos-kernel build-talos-installer build-talos-iso build-talos-nocloud build-talos-metal:
	mkdir -p ../../../_out/assets
	cat images/talos/profiles/$(subst build-talos-,,$@).yaml | \
		docker run --rm -i -v /dev:/dev --privileged "ghcr.io/siderolabs/imager:$(TALOS_VERSION)" --tar-to-stdout - | \
		tar -C ../../../_out/assets -xzf-

