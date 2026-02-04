NAME=backup-controller
NAMESPACE=cozy-backup-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image: image-backup-controller image-backupstrategy-controller

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

image-backupstrategy-controller:
	docker buildx build -f images/backupstrategy-controller/Dockerfile ../../.. \
		--tag $(REGISTRY)/backupstrategy-controller:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/backupstrategy-controller:latest \
		--cache-to type=inline \
		--metadata-file images/backupstrategy-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/backupstrategy-controller:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/backupstrategy-controller.json -o json -r)" \
		yq -i '.backupStrategyController.image = strenv(IMAGE)' values.yaml
	rm -f images/backupstrategy-controller.json
