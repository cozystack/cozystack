CLICKHOUSE_BACKUP_TAG = $(shell awk '$$0 ~ /^version:/ {print $$2}' Chart.yaml)
PRESET_ENUM := ["nano","micro","small","medium","large","xlarge","2xlarge"]

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

generate:
	readme-generator-for-helm -v values.yaml -s values.schema.json -r README.md
	yq -i -o json --indent 4 '.properties.resourcesPreset.enum = $(PRESET_ENUM)' values.schema.json
	yq -i -o json --indent 4 '.properties.clickhouseKeeper.resourcesPreset.enum = $(PRESET_ENUM)' values.schema.json

image:
	docker buildx build images/clickhouse-backup \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/clickhouse-backup:$(call settag,$(CLICKHOUSE_BACKUP_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/clickhouse-backup:latest \
		--cache-to type=inline \
		--metadata-file images/clickhouse-backup.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/clickhouse-backup:$(call settag,$(CLICKHOUSE_BACKUP_TAG))@$$(yq e '."containerimage.digest"' images/clickhouse-backup.json -o json -r)" \
		> images/clickhouse-backup.tag
	rm -f images/clickhouse-backup.json
