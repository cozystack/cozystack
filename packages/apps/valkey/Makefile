VALKEY_VERSIONS = $(shell awk -F'"' '{print $$4}' files/versions.yaml)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'valkey' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/valkey/types.go
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

# Build one thin Redis-compat image per supported Valkey version (see
# images/valkey/Dockerfile). Mirrors the per-version image loop used by the
# kubernetes app; each build writes images/valkey-<version>.tag consumed by the
# chart via .Files.Get.
image:
	$(foreach ver,$(VALKEY_VERSIONS), \
		docker buildx build images/valkey \
			--build-arg VALKEY_VERSION=$(ver) \
			--tag $(REGISTRY)/valkey:$(ver)-$(IMAGE_TAG) \
			$(if $(filter 1,$(PUBLISH_VERSIONED)),--tag $(REGISTRY)/valkey:$(ver)) \
			$(call cache-args,valkey,$(ver)-buildcache) \
			--metadata-file images/valkey-$(ver).json \
			$(BUILDX_ARGS) && \
		echo "$(REGISTRY)/valkey:$(ver)-$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/valkey-$(ver).json -o json -r)" \
			> images/valkey-$(ver).tag && \
		rm -f images/valkey-$(ver).json; \
	)
