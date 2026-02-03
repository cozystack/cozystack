NAME=platform
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk

show:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl apply --filename -
	kubectl delete helmreleases.helm.toolkit.fluxcd.io --selector cozystack.io/marked-for-deletion=true --all-namespaces

reconcile: apply

diff:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl diff --filename -

image: image-migrations

image-migrations:
	docker buildx build --file images/migrations/Dockerfile . \
		--tag $(REGISTRY)/platform-migrations:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/platform-migrations:latest \
		--cache-to type=inline \
		--metadata-file images/migrations.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/platform-migrations:$(call settag,$(TAG))@$$(yq --exit-status '.["containerimage.digest"]' images/migrations.json --output-format json --raw-output)" \
		yq --inplace '.migrations.image = strenv(IMAGE)' values.yaml
	rm -f images/migrations.json
