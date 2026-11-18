SHADOWBOX_VERSION = $(shell awk -F= '$$1 == "ARG VERSION" {print $$2; exit}' images/shadowbox/Dockerfile)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test image
test:
	helm unittest .

generate:
	cozyvalues-gen -m 'vpn' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vpn/types.go
	../../../hack/update-crd.sh

image:
	docker buildx build images/shadowbox \
		$(call image-tags,shadowbox,$(SHADOWBOX_VERSION)) \
		$(call cache-args,shadowbox) \
		--metadata-file images/shadowbox.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/shadowbox:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/shadowbox.json -o json -r)" \
		> images/shadowbox.tag
	rm -f images/shadowbox.json
