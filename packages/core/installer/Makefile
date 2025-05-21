NAME=installer
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer-amd64.yaml)

include ../../../scripts/common-envs.mk

ARCH := $(shell echo "$(PLATFORM)" | sed 's:linux/::g;s:,: :g')

pre-checks:
	../../../hack/pre-checks.sh

show:
	helm template -n $(NAMESPACE) $(NAME) .

apply:
	helm template -n $(NAMESPACE) $(NAME) . | kubectl apply -f -

diff:
	helm template -n $(NAMESPACE) $(NAME) . | kubectl diff -f -

update: update-amd64 update-arm64

update-%:
	hack/gen-profiles.sh $*

image: pre-checks image-matchbox image-cozystack image-talos

image-cozystack:
	make -C ../../.. repos
	docker buildx build -f images/cozystack/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/installer:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/installer:latest \
		--cache-to type=inline \
		--metadata-file images/installer.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/installer:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/installer.json -o json -r)" \
		yq -i '.cozystack.image = strenv(IMAGE)' values.yaml
	rm -f images/installer.json

image-talos:
	# assets for different architectures may be built on a single host
	for arch in amd64 arm64; do \
		test -f ../../../_out/assets/installer-$${arch}.tar || make talos-installer-$${arch}; \
		skopeo copy --override-os=linux --override-arch=$${arch} docker-archive:../../../_out/assets/installer-$${arch}.tar \
			docker://$(REGISTRY)/talos-$${arch}:$(call settag,$(TALOS_VERSION)); \
	done
	# assemble multiarch image from two parts
	docker buildx imagetools create \
		--builder=$(BUILDER) \
		--tag $(REGISTRY)/talos:$(call settag,$(TALOS_VERSION)) \
		$(REGISTRY)/talos-amd64:$(call settag,$(TALOS_VERSION)) \
		$(REGISTRY)/talos-arm64:$(call settag,$(TALOS_VERSION))

image-matchbox:
	for arch in $(ARCH); do \
		test -f ../../../_out/assets/kernel-$${arch} || make talos-kernel-$${arch}; \
		test -f ../../../_out/assets/initramfs-metal-$${arch}.xz || make talos-initramfs-$${arch}; \
	done
	docker buildx build -f images/matchbox/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/matchbox:$(call settag,$(TAG)) \
		--tag $(REGISTRY)/matchbox:$(call settag,$(TALOS_VERSION)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/matchbox:latest \
		--cache-to type=inline \
		--metadata-file images/matchbox.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/matchbox:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/matchbox.json -o json -r)" \
		> ../../extra/bootbox/images/matchbox.tag
	rm -f images/matchbox.json

assets: assets-amd64 assets-arm64

assets-amd64 assets-arm64: assets-%: talos-iso-% talos-nocloud-% talos-metal-% talos-kernel-% talos-initramfs-%

talos-%:
	mkdir -p ../../../_out/assets
	@echo Building $@...
	cat images/talos/profiles/$(subst talos-,,$@).yaml | \
		docker run --rm -i -v /dev:/dev --privileged "ghcr.io/siderolabs/imager:$(TALOS_VERSION)" --tar-to-stdout - | \
		tar -C ../../../_out/assets -xzf-
