KUBEOVN_TAG=$(shell awk '$$1 == "version:" {print $$2}' charts/kube-ovn/Chart.yaml)

export NAME=kubeovn
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts && mkdir -p charts/kube-ovn
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/kubeovn/kube-ovn | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/kubeovn/kube-ovn/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 1 kube-ovn-$${tag#*v}/charts
	patch --no-backup-if-mismatch -p4 < patches/cozyconfig.diff
	patch --no-backup-if-mismatch -p4 < patches/mtu.diff
	version=$$(awk '$$1 == "version:" {print $$2}' charts/kube-ovn/Chart.yaml) && \
	sed -i "s/ARG VERSION=.*/ARG VERSION=$${version}/" images/kubeovn/Dockerfile

image:
	docker buildx build images/kubeovn \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/kubeovn:$(call settag,$(KUBEOVN_TAG)) \
		--tag $(REGISTRY)/kubeovn:$(call settag,$(KUBEOVN_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/kubeovn:latest \
		--cache-to type=inline \
		--metadata-file images/kubeovn.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	REGISTRY="$(REGISTRY)" \
		yq -i '.global.registry.address = strenv(REGISTRY)' values.yaml
	REPOSITORY="kubeovn" \
		yq -i '.global.images.kubeovn.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(call settag,$(KUBEOVN_TAG))@$$(yq e '."containerimage.digest"' images/kubeovn.json -o json -r)" \
		yq -i '.global.images.kubeovn.tag = strenv(TAG)' values.yaml
	rm -f images/kubeovn.json
