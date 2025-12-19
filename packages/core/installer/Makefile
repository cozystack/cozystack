NAME=installer
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer.yaml)

include ../../../scripts/common-envs.mk

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

update:
	hack/gen-profiles.sh

image: pre-checks image-matchbox image-cozystack image-talos image-packages

image-cozystack: update-version
	docker buildx build -f images/cozystack/Dockerfile ../../.. \
		--tag $(REGISTRY)/installer:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/installer:latest \
		--cache-to type=inline \
		--metadata-file images/installer.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/installer:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/installer.json -o json -r)" \
		yq -i '.cozystack.image = strenv(IMAGE)' values.yaml
	rm -f images/installer.json

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

talos-initramfs talos-kernel talos-installer talos-iso talos-nocloud talos-metal:
	mkdir -p ../../../_out/assets
	cat images/talos/profiles/$(subst talos-,,$@).yaml | \
		docker run --rm -i -v /dev:/dev --privileged "ghcr.io/siderolabs/imager:$(TALOS_VERSION)" --tar-to-stdout - | \
		tar -C ../../../_out/assets -xzf-

update-version:
	TAG="$(call settag,$(TAG))" \
			yq -i '.cozystackOperator.cozystackVersion = strenv(TAG)' values.yaml

image-packages: update-version
	mkdir -p ../../../_out/assets images
	flux push artifact \
		oci://$(REGISTRY)/platform-packages:$(call settag,$(TAG)) \
		--path=../../../packages \
		--source=https://github.com/cozystack/cozystack \
		--revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
		2>&1 | tee images/cozystack-packages.log
	export REPO="oci://$(REGISTRY)/platform-packages"; \
	export DIGEST=$$(awk -F@ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log; rm -f images/cozystack-packages.log); \
		test -n "$$DIGEST" && yq -i '.cozystackOperator.platformSource = (strenv(REPO) + "@" + strenv(DIGEST))' values.yaml
