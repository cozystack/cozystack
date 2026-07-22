REDIS_OPERATOR_TAG=$(shell grep -F 'ARG VERSION=' images/redis-operator/Dockerfile | cut -f2 -d=)
export NAME=redis-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm pull oci://ghcr.io/freshworks-oss/charts/redis-operator --version $(patsubst v%,%,$(REDIS_OPERATOR_TAG)) --untar --untardir charts

image:
	docker buildx build images/redis-operator \
		$(call image-tags,redis-operator,$(REDIS_OPERATOR_TAG)) \
		$(call cache-args,redis-operator) \
		--metadata-file images/redis-operator.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/redis-operator" \
		yq -i '.redis-operator.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/redis-operator.json -o json -r) \
		yq -i '.redis-operator.image.tag = strenv(TAG)' values.yaml
	rm -f images/redis-operator.json
