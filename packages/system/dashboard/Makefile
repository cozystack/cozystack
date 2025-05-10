export NAME=dashboard
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update: update-chart update-dockerfiles
image: image-dashboard image-kubeapps-apis

update-chart:
	rm -rf charts
	helm repo add bitnami https://charts.bitnami.com/bitnami
	helm repo update bitnami
	helm pull bitnami/kubeapps --untar --untardir charts
	rm -rf charts/kubeapps/charts/postgresql/
	sed -i 's/.cluster.local//g' charts/kubeapps/templates/kubeappsapis/deployment.yaml
	patch --no-backup-if-mismatch charts/kubeapps/templates/frontend/configmap.yaml < patches/logos.patch

update-dockerfiles:
	@echo Update dockerfiles manually
	#tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/vmware-tanzu/kubeapps  | awk -F'[/^]' 'END{print $$3}') && \
	wget https://github.com/vmware-tanzu/kubeapps/raw/$${tag}/cmd/kubeapps-apis/Dockerfile -O images/kubeapps-apis/Dockerfile && \
	patch --no-backup-if-mismatch images/kubeapps-apis/Dockerfile < images/kubeapps-apis/dockerfile.diff && \
	node_image=$$(wget -O- https://github.com/vmware-tanzu/kubeapps/raw/main/dashboard/Dockerfile | awk '/FROM bitnami\/node/ {print $$2}') && \
	sed -i "s|FROM .* AS build|FROM $${node_image} AS build|" images/dashboard/Dockerfile && \
	version=$$(echo "$$tag" | sed 's/^v//') && \
	sed -i "s/ARG VERSION=.*/ARG VERSION=$${version}/" images/dashboard/Dockerfile

image-dashboard: update-version
	docker buildx build images/dashboard \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/dashboard:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/dashboard:latest \
		--cache-to type=inline \
		--metadata-file images/dashboard.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	REGISTRY="$(REGISTRY)" \
		yq -i '.kubeapps.dashboard.image.registry = strenv(REGISTRY)' values.yaml
	REPOSITORY="dashboard" \
		yq -i '.kubeapps.dashboard.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(TAG))" \
		yq -i '.kubeapps.dashboard.image.tag = strenv(TAG)' values.yaml
	DIGEST=$$(yq e '."containerimage.digest"' images/dashboard.json -o json -r) \
		yq -i '.kubeapps.dashboard.image.digest = strenv(DIGEST)' values.yaml
	rm -f images/dashboard.json

image-kubeapps-apis: update-version
	docker buildx build images/kubeapps-apis \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/kubeapps-apis:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubeapps-apis:latest \
		--cache-to type=inline \
		--metadata-file images/kubeapps-apis.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	REGISTRY="$(REGISTRY)" \
		yq -i '.kubeapps.kubeappsapis.image.registry = strenv(REGISTRY)' values.yaml
	REPOSITORY="kubeapps-apis" \
		yq -i '.kubeapps.kubeappsapis.image.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(TAG))" \
		yq -i '.kubeapps.kubeappsapis.image.tag = strenv(TAG)' values.yaml
	DIGEST=$$(yq e '."containerimage.digest"' images/kubeapps-apis.json -o json -r) \
		yq -i '.kubeapps.kubeappsapis.image.digest = strenv(DIGEST)' values.yaml
	rm -f images/kubeapps-apis.json

update-version:
	sed -i "s|\(\"appVersion\":\).*|\1 \"$(TAG)\",|g" ./charts/kubeapps/templates/dashboard/configmap.yaml
