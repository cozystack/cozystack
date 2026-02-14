export NAME=kilo
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/cozystack/kilo | awk -F'[/^]' 'END{print $$3}') && \
	digest=$$(skopeo inspect --override-os linux --override-arch amd64 --format '{{.Digest}}' docker://ghcr.io/cozystack/cozystack/kilo:amd64-$${tag}) && \
	REPOSITORY="ghcr.io/cozystack/cozystack/kilo" \
		yq -i '.kilo.image.repository = strenv(REPOSITORY)' values.yaml && \
	TAG="$${tag}@$${digest}" \
		yq -i '.kilo.image.tag = strenv(TAG)' values.yaml
