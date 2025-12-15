NAME=backupstrategy-controller
NAMESPACE=cozy-backup-controller

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-backupstrategy-controller

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
