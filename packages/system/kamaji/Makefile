export NAME=kamaji
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/clastix/kamaji | grep -E 'refs/tags/[0-9]+\.[0-9]+\.[0-9]+-edge$$' | awk -F/ 'END{print $$NF}') && \
	curl -sSL https://github.com/clastix/kamaji/archive/refs/tags/$${tag}.tar.gz | \
	tar -xzvf - --strip 1 kamaji-$${tag}/charts && \
	sed -i "/ARG VERSION/ s|=.*|=$${tag}|g" images/kamaji/Dockerfile
	# Re-apply Cozystack-side patches over the freshly extracted chart so
	# `make update` stays reproducible. Mirrors the pattern used by
	# packages/system/fluxcd-operator and packages/system/ingress-nginx.
	patch --no-backup-if-mismatch -p1 < patches/1.diff

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
