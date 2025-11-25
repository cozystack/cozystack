NAME=cozystack-operator
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk

image: pre-checks image-operator image-packages update-version

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

image-operator:
	docker buildx build -f images/cozystack-operator/Dockerfile ../../.. \
		--tag $(REGISTRY)/cozystack-operator:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-operator:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-operator:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-operator.json -o json -r)" \
		yq -i '.cozystackOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-operator.json

update-version:
	TAG="$(call settag,$(TAG))" \
		yq -i '.cozystackOperator.cozystackVersion = strenv(TAG)' values.yaml

image-packages:
	mkdir -p ../../../_out/assets images
	flux push artifact \
		oci://$(REGISTRY)/platform-packages:$(call settag,$(TAG)) \
		--path=../../../packages \
		--source=https://github.com/cozystack/cozystack \
		--revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
	2>&1 | tee images/cozystack-packages.log
	REPO="oci://$(REGISTRY)/platform-packages" \
		yq -i '.cozystackOperator.packagesRepository = strenv(REPO)' values.yaml
	export DIGEST=$$(awk -F@ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log; rm -f images/cozystack-packages.log); \
		test -n "$$DIGEST" && yq -i '.cozystackOperator.packagesDigest = strenv(DIGEST)' values.yaml
