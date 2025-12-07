NAME=backup-controller
NAMESPACE=cozy-backup-controller

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-backup-controller

image-backup-controller:
	docker buildx build -f images/backup-controller/Dockerfile ../../.. \
		--tag $(REGISTRY)/backup-controller:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/backup-controller:latest \
		--cache-to type=inline \
		--metadata-file images/backup-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/backup-controller:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/backup-controller.json -o json -r)" \
		yq -i '.backupController.image = strenv(IMAGE)' values.yaml
	rm -f images/backup-controller.json
