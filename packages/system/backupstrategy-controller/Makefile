NAME=backupstrategy-controller
NAMESPACE=cozy-backup-controller

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# PHONY so a stray file/dir named `image` cannot mark the aggregate up to date
# and skip both builds.
.PHONY: image image-backupstrategy-controller image-redis-backup

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

# Depends on image-backupstrategy-controller so the two recipes never rewrite
# values.yaml concurrently under `make -j image` — each stamps a digest with
# `yq -i`, and a concurrent write would drop one of them.
image-redis-backup: image-backupstrategy-controller
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
