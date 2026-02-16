NAME=installer
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl apply -f -

diff:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

image: pre-checks image-operator image-packages chart

image-operator:
	docker buildx build -f images/cozystack-operator/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-operator:$(call settag,$(TAG)) \
		--build-arg VERSION=$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-operator:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-operator:$(call settag,$(TAG))@$$(yq -e '.["containerimage.digest"]' images/cozystack-operator.json -o json -r)" \
		yq -i '.cozystackOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-operator.json

image-packages:
	mkdir -p ../../../_out/assets images
	flux push artifact \
		oci://$(REGISTRY)/cozystack-packages:$(call settag,$(TAG)) \
		--path=../../../packages \
		--source=https://github.com/cozystack/cozystack \
		--revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
		2>&1 | tee images/cozystack-packages.log
	export REPO="oci://$(REGISTRY)/cozystack-packages" \
	export DIGEST=$$(awk -F @ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log) && \
		rm -f images/cozystack-packages.log && \
		test -n "$$DIGEST" && \
		yq -i '.cozystackOperator.platformSourceUrl = strenv(REPO)' values.yaml && \
		yq -i '.cozystackOperator.platformSourceRef = "digest=" + strenv(DIGEST)' values.yaml

chart:
	set -e; \
	PKG=$$(helm package . --version $(COZYSTACK_VERSION) | awk '{print $$NF}'); \
	trap 'rm -f "$$PKG"' EXIT; \
	if [ "$(PUSH)" = "1" ]; then helm push "$$PKG" oci://$(REGISTRY); fi
