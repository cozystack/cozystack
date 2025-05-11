MARIADB_BACKUP_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

generate:
	readme-generator -v values.yaml -s values.schema.json -r README.md

image:
	docker buildx build images/mariadb-backup \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/mariadb-backup:$(call settag,$(MARIADB_BACKUP_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/mariadb-backup:latest \
		--cache-to type=inline \
		--metadata-file images/mariadb-backup.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/mariadb-backup:$(call settag,$(MARIADB_BACKUP_TAG))@$$(yq e '."containerimage.digest"' images/mariadb-backup.json -o json -r)" \
		> images/mariadb-backup.tag
	rm -f images/mariadb-backup.json
