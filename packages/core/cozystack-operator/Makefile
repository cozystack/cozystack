NAME=cozystack-operator
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk

image: pre-checks image-cozystack-operator

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

image-cozystack-operator:
	docker buildx build -f images/cozystack-operator/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-operator:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-operator:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-operator:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-operator.json -o json -r)" \
		yq -i '.cozystackOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-operator.json

