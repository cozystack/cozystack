NAME=db-autoscaler
NAMESPACE=cozy-db-autoscaler

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-db-autoscaler

image-db-autoscaler:
	docker buildx build -f images/db-autoscaler/Dockerfile ../../.. \
		$(call image-tags,db-autoscaler,v$(COZYSTACK_VERSION)) \
		$(call cache-args,db-autoscaler) \
		--metadata-file images/db-autoscaler.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/db-autoscaler:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/db-autoscaler.json -o json -r)" \
		yq -i '.dbAutoscaler.image = strenv(IMAGE)' values.yaml
	rm -f images/db-autoscaler.json
