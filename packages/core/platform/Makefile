NAME=cozystack-platform
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk

show:
	cozyhr show --namespace $(NAMESPACE) $(NAME)

apply:
	cozyhr apply --namespace $(NAMESPACE) $(NAME)

reconcile: apply

diff:
	cozyhr diff --namespace $(NAMESPACE) $(NAME)

image: image-migrations

image-migrations:
	docker buildx build images/migrations \
		--tag $(REGISTRY)/platform-migrations:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/platform-migrations:latest \
		--cache-to type=inline \
		--metadata-file images/migrations.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/platform-migrations:$(call settag,$(TAG))@$$(yq -e -o json -r '.["containerimage.digest"]' images/migrations.json)" \
		yq --inplace '.migrations.image = strenv(IMAGE)' values.yaml
	rm -f images/migrations.json
