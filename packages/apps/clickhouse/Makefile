CLICKHOUSE_BACKUP_TAG = $(shell awk '$$0 ~ /^version:/ {print $$2}' Chart.yaml)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'clickhouse' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/clickhouse/types.go
	../../../hack/update-crd.sh

.PHONY: test
test:
	helm unittest .

image:
	docker buildx build images/clickhouse-backup \
		$(call image-tags,clickhouse-backup,$(CLICKHOUSE_BACKUP_TAG)) \
		$(call cache-args,clickhouse-backup) \
		--metadata-file images/clickhouse-backup.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/clickhouse-backup:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/clickhouse-backup.json -o json -r)" \
		> images/clickhouse-backup.tag
	rm -f images/clickhouse-backup.json
	docker buildx build images/altinity-clickhouse-backup \
		$(call image-tags,altinity-clickhouse-backup,$(CLICKHOUSE_BACKUP_TAG)) \
		$(call cache-args,altinity-clickhouse-backup) \
		--metadata-file images/altinity-clickhouse-backup.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/altinity-clickhouse-backup:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/altinity-clickhouse-backup.json -o json -r)" \
		> images/altinity-clickhouse-backup.tag
	rm -f images/altinity-clickhouse-backup.json
