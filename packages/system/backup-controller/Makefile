NAME=backup-controller
NAMESPACE=cozy-backup-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

image: image-backup-controller

image-backup-controller:
	docker buildx build -f images/backup-controller/Dockerfile ../../.. \
		$(call image-tags,backup-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,backup-controller) \
		--metadata-file images/backup-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/backup-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/backup-controller.json -o json -r)" \
		yq -i '.backupController.image = strenv(IMAGE)' values.yaml
	rm -f images/backup-controller.json
