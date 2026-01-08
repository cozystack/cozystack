NAME=installer
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

image: pre-checks image-cozystack

image-cozystack:
	docker buildx build -f images/cozystack/Dockerfile ../../.. \
		--tag $(REGISTRY)/installer:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/installer:latest \
		--cache-to type=inline \
		--metadata-file images/installer.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/installer:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/installer.json -o json -r)" \
		yq -i '.cozystack.image = strenv(IMAGE)' values.yaml
	rm -f images/installer.json

update-version:
	TAG="$(call settag,$(TAG))" \
			  yq -i '.cozystackOperator.cozystackVersion = strenv(TAG)' values.yaml

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


image-packages: update-version
	mkdir -p ../../../_out/assets images
	flux push artifact \
		 oci://$(REGISTRY)/platform-packages:$(call settag,$(TAG)) \
		 --path=../../../packages \
		 --source=https://github.com/cozystack/cozystack \
		 --revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
		 2>&1 | tee images/cozystack-packages.log
	export REPO="oci://$(REGISTRY)/platform-packages"; \
	export DIGEST=$$(awk -F@ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log; rm -f images/cozystack-packages.log); \
		 test -n "$$DIGEST" && yq -i '.cozystackOperator.platformSource = (strenv(REPO) + "@" + strenv(DIGEST))' values.yaml
