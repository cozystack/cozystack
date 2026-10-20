export NAME=gpu-node-agent
export NAMESPACE=cozy-gpu-operator

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test
test:
	helm unittest .

image:
	docker buildx build -f images/gpu-node-agent/Dockerfile ../../../ \
		$(call image-tags,gpu-node-agent,v$(COZYSTACK_VERSION)) \
		$(call cache-args,gpu-node-agent) \
		--metadata-file images/gpu-node-agent.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/gpu-node-agent:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/gpu-node-agent.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/gpu-node-agent.json
