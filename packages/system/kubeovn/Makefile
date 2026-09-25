KUBEOVN_TAG=$(shell awk '$$1 == "appVersion:" {print $$2}' charts/kube-ovn/Chart.yaml)

export NAME=kubeovn
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/kubeovn/kube-ovn | awk -F'[/^]' '{print $$3}' | grep '^v1\.15\.' | tail -n1) && \
	curl -sSL https://github.com/kubeovn/kube-ovn/archive/refs/tags/$${tag}.tar.gz | \
	tar -xzf - --strip 1 kube-ovn-$${tag#*v}/charts/kube-ovn
	# Teach the CNI DaemonSet to honour an explicit MTU: upstream derives it from
	# the node interface, which is wrong when the underlay is encapsulated.
	patch --no-backup-if-mismatch -p4 < patches/mtu.diff
	version=$$(awk '$$1 == "appVersion:" {print $$2}' charts/kube-ovn/Chart.yaml) && \
	$(SED_INPLACE) -e "s/ARG VERSION=.*/ARG VERSION=$${version}/" images/kubeovn/Dockerfile && \
	$(SED_INPLACE) -e "s/ARG TAG=.*/ARG TAG=v$${version}/" images/kubeovn/Dockerfile

image:
	docker buildx build images/kubeovn \
		$(call image-tags,kubeovn,v$(KUBEOVN_TAG)) \
		$(call cache-args,kubeovn) \
		--metadata-file images/kubeovn.json \
		$(BUILDX_ARGS)
	ADDRESS="$(REGISTRY)" \
		yq -i '.global.registry.address = strenv(ADDRESS)' values.yaml
	REPOSITORY="kubeovn" \
		yq -i '.global.images.kubeovn.repository = strenv(REPOSITORY)' values.yaml
	TAG="$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubeovn.json -o json -r)" \
		yq -i '.global.images.kubeovn.tag = strenv(TAG)' values.yaml
	rm -f images/kubeovn.json
