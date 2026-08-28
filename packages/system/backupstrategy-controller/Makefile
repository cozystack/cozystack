NAME=backupstrategy-controller
NAMESPACE=cozy-backup-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image: image-backupstrategy-controller image-redis-backup

image-backupstrategy-controller:
	docker buildx build -f images/backupstrategy-controller/Dockerfile ../../.. \
		$(call image-tags,backupstrategy-controller,v$(COZYSTACK_VERSION)) \
		$(call cache-args,backupstrategy-controller) \
		--metadata-file images/backupstrategy-controller.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/backupstrategy-controller:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/backupstrategy-controller.json -o json -r)" \
		yq -i '.backupStrategyController.image = strenv(IMAGE)' values.yaml
	rm -f images/backupstrategy-controller.json

image-redis-backup:
	docker buildx build -f images/redis-backup/Dockerfile images/redis-backup \
		$(call image-tags,redis-backup,v$(COZYSTACK_VERSION)) \
		$(call cache-args,redis-backup) \
		--metadata-file images/redis-backup.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/redis-backup:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/redis-backup.json -o json -r)" \
		yq -i '.backupStrategyController.redisBackupClientImage = strenv(IMAGE)' values.yaml
	rm -f images/redis-backup.json

test:
	helm unittest .
