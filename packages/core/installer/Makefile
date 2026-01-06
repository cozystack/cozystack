NAME=installer
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl apply --filename -

diff:
	cozyhr show --namespace $(NAMESPACE) $(NAME) --plain | kubectl diff --filename -

image: pre-checks image-operator image-packages

update-version:
	TAG="$(call settag,$(TAG))" \
		yq --inplace '.cozystackOperator.cozystackVersion = strenv(TAG)' values.yaml

image-operator:
	docker buildx build --file images/cozystack-operator/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-operator:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-operator:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-operator:$(call settag,$(TAG))@$$(yq --exit-status '.["containerimage.digest"]' images/cozystack-operator.json --output-format json --raw-output)" \
		yq --inplace '.cozystackOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-operator.json

image-packages: update-version
	mkdir -p ../../../_out/assets images
	flux push artifact \
		oci://$(REGISTRY)/cozystack-packages:$(call settag,$(TAG)) \
		--path=../../../packages \
		--source=https://github.com/cozystack/cozystack \
		--revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
		2>&1 | tee images/cozystack-packages.log
	REPO="oci://$(REGISTRY)/cozystack-packages" \
	DIGEST=$$(awk --field-separator @ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log) && \
		rm -f images/cozystack-packages.log && \
		test -n "$$DIGEST" && \
		yq --inplace '.cozystackOperator.platformSourceUrl = strenv(REPO)' values.yaml && \
		yq --inplace '.cozystackOperator.platformSourceRef = "digest=" + strenv(DIGEST)' values.yaml
