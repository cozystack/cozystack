export NAME=kilo
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	sha=$$(git ls-remote https://github.com/squat/kilo refs/heads/main | awk '{print $$1}') && \
	sed -i "/ARG VERSION/ s|=.*|=$${sha}|g" images/kilo/Dockerfile

image:
	docker buildx build images/kilo \
		$(call image-tags,kilo,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(REGISTRY)/kilo:latest \
		--cache-to type=inline \
		--metadata-file images/kilo.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/kilo" \
		yq -i '.kilo.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kilo.json -o json -r) \
		yq -i '.kilo.image.tag = strenv(TAG)' values.yaml
	rm -f images/kilo.json
