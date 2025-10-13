export NAME=kamaji
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/clastix/kamaji | grep refs/tags/edge- | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/clastix/kamaji/archive/refs/tags/$${tag}.tar.gz | \
	tar -xzvf - --strip 1 kamaji-$${tag}/charts && \
	sed -i "/ARG VERSION/ s|=.*|=$${tag}|g" images/kamaji/Dockerfile

image:
	docker buildx build images/kamaji \
		--tag $(REGISTRY)/kamaji:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kamaji:latest \
		--cache-to type=inline \
		--metadata-file images/kamaji.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/kamaji" \
		yq -i '.kamaji.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(TAG)@$$(yq e '."containerimage.digest"' images/kamaji.json -o json -r) \
		yq -i '.kamaji.image.tag = strenv(TAG)' values.yaml
	yq -i '.kamaji.extraArgs[0] = "--migrate-image=" + .kamaji.image.repository + ":" + .kamaji.image.tag' values.yaml
	rm -f images/kamaji.json
