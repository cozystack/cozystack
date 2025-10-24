REDIS_OPERATOR_TAG=$(shell grep -F 'ARG VERSION=' images/redis-operator/Dockerfile | cut -f2 -d=)
export NAME=redis-operator
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add redis-operator https://spotahome.github.io/redis-operator
	helm repo update redis-operator
	helm pull redis-operator/redis-operator --untar --untardir charts
	sed -i '/{{/d' charts/redis-operator/crds/databases.spotahome.com_redisfailovers.yaml

image:
	docker buildx build images/redis-operator \
		--tag $(REGISTRY)/redis-operator:$(REDIS_OPERATOR_TAG) \
		--cache-from type=registry,ref=$(REGISTRY)/redis-operator:latest \
		--cache-to type=inline \
		--metadata-file images/redis-operator.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/redis-operator" \
		yq -i '.redis-operator.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(REDIS_OPERATOR_TAG)@$$(yq e '."containerimage.digest"' images/redis-operator.json -o json -r) \
		yq -i '.redis-operator.image.tag = strenv(TAG)' values.yaml
	rm -f images/redis-operator.json
