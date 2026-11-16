NAME=observability-proxy
NAMESPACE=cozy-observability-proxy

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-observability-proxy

image-observability-proxy:
	docker buildx build -f images/observability-proxy/Dockerfile ../../.. \
		$(call image-tags,observability-proxy,v$(COZYSTACK_VERSION)) \
		$(call cache-args,observability-proxy) \
		--metadata-file images/observability-proxy.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/observability-proxy:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/observability-proxy.json -o json -r)" \
		yq -i '.observabilityProxy.image = strenv(IMAGE)' values.yaml
	rm -f images/observability-proxy.json
