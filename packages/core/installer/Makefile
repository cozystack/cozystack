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

test:
	helm unittest .

image: pre-checks image-operator image-packages chart

image-operator:
	docker buildx build -f images/cozystack-operator/Dockerfile ../../.. \
		$(call image-tags,cozystack-operator,v$(COZYSTACK_VERSION)) \
		--build-arg VERSION=v$(COZYSTACK_VERSION) \
		$(call cache-args,cozystack-operator) \
		--metadata-file images/cozystack-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cozystack-operator:$(IMAGE_TAG)@$$(yq -e '.["containerimage.digest"]' images/cozystack-operator.json -o json -r)" \
		yq -i '.cozystackOperator.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-operator.json

# flux push artifact REPLACES its built-in ignore list when --ignore-paths is
# given, so the defaults are repeated here. node_modules/ is the addition: a
# developer who runs `pnpm install` under images/console leaves the console's
# dependency tree in packages/, and one file above Helm's 5MB per-file limit
# makes the dashboard chart unloadable on every cluster that pulls the bundle.
# Held by hack/check-packages-artifact-ignores.bats.
PACKAGES_IGNORE_PATHS := .git/,.gitignore,.gitmodules,.gitattributes,*.jpg,*.jpeg,*.gif,*.png,*.wmv,*.flv,*.tar.gz,*.zip,node_modules/

image-packages:
	mkdir -p ../../../_out/assets images
	echo $$(git rev-parse HEAD; git diff HEAD; git ls-files --others --exclude-standard) | shasum -a 256 > ../platform/.build-revision
	flux push artifact \
		oci://$(REGISTRY)/cozystack-packages:$(IMAGE_TAG) \
		--path=../../../packages \
		--ignore-paths='$(PACKAGES_IGNORE_PATHS)' \
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
