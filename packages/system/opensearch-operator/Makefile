# Single source for the version: the image's ARG VERSION. The chart and the
# binary have to move together, because the chart ships the CRDs the binary is
# compiled against.
OPENSEARCH_OPERATOR_TAG=$(shell grep -F 'ARG VERSION=' images/opensearch-operator/Dockerfile | cut -f2 -d=)

export NAME=opensearch-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add opensearch-operator https://opensearch-project.github.io/opensearch-k8s-operator/
	helm repo update opensearch-operator
	helm pull opensearch-operator/opensearch-operator --version $(patsubst v%,%,$(OPENSEARCH_OPERATOR_TAG)) --untar --untardir charts
	patch --no-backup-if-mismatch -p1 < patches/leaderElection.diff

image:
	docker buildx build images/opensearch-operator \
		$(call image-tags,opensearch-operator,$(OPENSEARCH_OPERATOR_TAG)) \
		$(call cache-args,opensearch-operator) \
		--metadata-file images/opensearch-operator.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/opensearch-operator" \
		yq -i '.opensearch-operator.manager.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/opensearch-operator.json -o json -r) \
		yq -i '.opensearch-operator.manager.image.tag = strenv(TAG)' values.yaml
	rm -f images/opensearch-operator.json

test:
	helm unittest .
