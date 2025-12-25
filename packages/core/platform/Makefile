NAME=platform
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-
	kubectl delete helmreleases.helm.toolkit.fluxcd.io -l cozystack.io/marked-for-deletion=true -A

reconcile: apply

namespaces-show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml

namespaces-apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml | kubectl apply -f-

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-

image: image-assets
image-assets:
	docker buildx build -f images/cozystack-assets/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-assets:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-assets:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-assets.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-assets:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-assets.json -o json -r)" \
		yq -i '.assets.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-assets.json
