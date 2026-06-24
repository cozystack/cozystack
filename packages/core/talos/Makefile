NAME=talos
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer.yaml)

include ../../../hack/common-envs.mk

update:
	hack/gen-profiles.sh

image: image-matchbox image-talos

image-talos:
	test -f ../../../_out/assets/installer-amd64.tar || make talos-installer
	set -e; \
	SRC=docker-archive:../../../_out/assets/installer-amd64.tar; \
	skopeo copy "$$SRC" docker://$(REGISTRY)/talos:$(IMAGE_TAG); \
	if [ "$(PUBLISH_VERSIONED)" = "1" ]; then skopeo copy "$$SRC" docker://$(REGISTRY)/talos:$(TALOS_VERSION); fi; \
	if [ "$(PUBLISH_FLOATING)" = "1" ]; then skopeo copy "$$SRC" docker://$(REGISTRY)/talos:latest; fi

image-matchbox:
	test -f ../../../_out/assets/kernel-amd64 || make talos-kernel
	test -f ../../../_out/assets/initramfs-metal-amd64.xz || make talos-initramfs
	docker buildx build -f images/matchbox/Dockerfile ../../.. \
		$(call image-tags,matchbox,$(TALOS_VERSION)) \
		$(call cache-args,matchbox) \
		--metadata-file images/matchbox.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/matchbox:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/matchbox.json -o json -r)" \
		> ../../extra/bootbox/images/matchbox.tag
	rm -f images/matchbox.json

assets: talos-iso talos-nocloud talos-metal talos-openstack talos-kernel talos-initramfs

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

talos-openstack:
	test -f ../../../_out/assets/openstack-amd64.raw.xz || $(MAKE) build-talos-openstack

build-talos-initramfs build-talos-kernel build-talos-installer build-talos-iso build-talos-nocloud build-talos-metal build-talos-openstack:
	mkdir -p ../../../_out/assets
	cat images/talos/profiles/$(subst build-talos-,,$@).yaml | \
		docker run --rm -i -v /dev:/dev --privileged "ghcr.io/siderolabs/imager:$(TALOS_VERSION)" --tar-to-stdout - | \
		tar -C ../../../_out/assets -xzf-

