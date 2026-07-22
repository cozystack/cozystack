export NAME=talos-log-collector
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

update:
	ver=$$(curl -sSL "https://api.github.com/repos/vectordotdev/vector/releases" | jq -r '[.[] | select(.prerelease == false) | .tag_name | select(startswith("v0."))][0]' | sed 's/^v//') && \
	$(SED_INPLACE) "/ARG VERSION/ s|=.*|=$${ver}-alpine|g" images/vector/Dockerfile && \
	VERSION="$${ver}" yq -i '.appVersion = strenv(VERSION)' Chart.yaml

image:
	docker buildx build images/vector \
		$(call image-tags,vector,v$(COZYSTACK_VERSION)) \
		$(call cache-args,vector) \
		--metadata-file images/vector.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/vector" \
		yq -i '.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/vector.json -o json -r) \
		yq -i '.image.tag = strenv(TAG)' values.yaml
	rm -f images/vector.json

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
