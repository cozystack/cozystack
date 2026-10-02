NAME=talos
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer.yaml)

include ../../../hack/common-envs.mk

ASSETS := ../../../_out/assets

# The installer image and the release assets are built for the architectures
# in PLATFORM. matchbox carries the kernel and initramfs of both whatever
# PLATFORM says: the node it runs on has nothing to do with the architecture
# of the machines it network-boots.
TALOS_ARCHES := $(patsubst linux/%,%,$(subst $(comma), ,$(or $(PLATFORM),$(DEFAULT_PLATFORMS))))
BOOT_ARCHES := amd64 arm64

# assets <file pattern> <arches> — the asset paths, % standing for the arch.
assets = $(foreach a,$(2),$(ASSETS)/$(subst %,$(a),$(1)))

update:
	hack/gen-profiles.sh

image: image-matchbox image-talos

# The installers of every arch go into one OCI layout, where
# hack/installer-index.sh ties them into the index that is pushed.
image-talos: talos-installer
	rm -rf $(ASSETS)/installer-oci
	set -e; \
	for a in $(TALOS_ARCHES); do skopeo copy docker-archive:$(ASSETS)/installer-$$a.tar oci:$(ASSETS)/installer-oci:$$a; done; \
	hack/installer-index.sh $(ASSETS)/installer-oci $(TALOS_ARCHES); \
	SRC=oci:$(ASSETS)/installer-oci:index; \
	$(if $(strip $(OCI_EXPORT_DIR)),mkdir -p "$(OCI_EXPORT_DIR)"; skopeo copy --all "$$SRC" "oci-archive:$(OCI_EXPORT_DIR)/talos.oci.tar:$(IMAGE_TAG)",skopeo copy --all "$$SRC" docker://$(REGISTRY)/talos:$(IMAGE_TAG)); \
	if [ -z "$(OCI_EXPORT_DIR)" ] && [ "$(PUBLISH_VERSIONED)" = "1" ]; then skopeo copy --all "$$SRC" docker://$(REGISTRY)/talos:$(TALOS_VERSION); fi; \
	if [ -z "$(OCI_EXPORT_DIR)" ] && [ "$(PUBLISH_FLOATING)" = "1" ]; then skopeo copy --all "$$SRC" docker://$(REGISTRY)/talos:latest; fi
	rm -rf $(ASSETS)/installer-oci

image-matchbox: $(call assets,kernel-%,$(BOOT_ARCHES)) $(call assets,initramfs-metal-%.xz,$(BOOT_ARCHES))
	docker buildx build -f images/matchbox/Dockerfile ../../.. \
		$(call image-tags,matchbox,$(TALOS_VERSION)) \
		$(call cache-args,matchbox) \
		--metadata-file images/matchbox.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/matchbox:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/matchbox.json -o json -r)" \
		> ../../extra/bootbox/images/matchbox.tag
	rm -f images/matchbox.json

assets: talos-iso talos-nocloud talos-metal talos-kernel talos-initramfs

talos-initramfs: $(call assets,initramfs-metal-%.xz,$(TALOS_ARCHES))
talos-kernel: $(call assets,kernel-%,$(TALOS_ARCHES))
talos-installer: $(call assets,installer-%.tar,$(TALOS_ARCHES))
talos-iso: $(call assets,metal-%.iso,$(TALOS_ARCHES))
talos-nocloud: $(call assets,nocloud-%.raw.xz,$(TALOS_ARCHES))
talos-metal: $(call assets,metal-%.raw.xz,$(TALOS_ARCHES))

.PHONY: update image image-talos image-matchbox assets talos-initramfs talos-kernel talos-installer talos-iso talos-nocloud talos-metal

# imager <profile> — run the imager on the profile of that name for the arch
# in $*. amd64 profiles carry no arch suffix (see hack/gen-profiles.sh).
define imager
	mkdir -p $(ASSETS)
	docker run --rm -i -v /dev:/dev --privileged "ghcr.io/siderolabs/imager:$(TALOS_VERSION)" --tar-to-stdout - \
		< images/talos/profiles/$(1)$(if $(filter-out amd64,$*),-$*).yaml | \
		tar -C $(ASSETS) -xzf-
endef

$(ASSETS)/initramfs-metal-%.xz: ; $(call imager,initramfs)
$(ASSETS)/kernel-%: ; $(call imager,kernel)
$(ASSETS)/installer-%.tar: ; $(call imager,installer)
$(ASSETS)/metal-%.iso: ; $(call imager,iso)
$(ASSETS)/nocloud-%.raw.xz: ; $(call imager,nocloud)
$(ASSETS)/metal-%.raw.xz: ; $(call imager,metal)
