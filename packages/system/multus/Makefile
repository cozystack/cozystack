export NAME=multus
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf templates
	mkdir templates
	$(eval RELEASE := $(shell curl -s https://api.github.com/repos/k8snetworkplumbingwg/multus-cni/releases/latest?per_page=1 | jq -r '.name'))
	wget -q https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/refs/tags/$(RELEASE)/deployments/multus-daemonset-thick.yml -O templates/multus-daemonset-thick.yml
	sed -i '/multus-cni/s/snapshot-thick/$(RELEASE)-thick/' templates/multus-daemonset-thick.yml && \
	patch --no-backup-if-mismatch -p4 < patches/customize-deployment.patch
	$(SED_INPLACE) "/ARG VERSION/ s|=.*|=$(RELEASE)|g" images/multus-cni/Dockerfile

image:
	docker buildx build images/multus-cni \
		$(call image-tags,multus-cni,v$(COZYSTACK_VERSION)) \
		$(call cache-args,multus-cni) \
		--metadata-file images/multus-cni.json \
		$(BUILDX_ARGS)
	DIGEST=$$(yq e '."containerimage.digest"' images/multus-cni.json -o json -r) && \
		sed -i "s|image: .*multus-cni.*|image: $(REGISTRY)/multus-cni:$(IMAGE_TAG)@$${DIGEST}|g" templates/multus-daemonset-thick.yml
	rm -f images/multus-cni.json
