MARIADB_BACKUP_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

image:
	docker buildx build images/mariadb-backup \
		--tag $(REGISTRY)/mariadb-backup:$(call settag,$(MARIADB_BACKUP_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/mariadb-backup:latest \
		--cache-to type=inline \
		--metadata-file images/mariadb-backup.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/mariadb-backup:$(call settag,$(MARIADB_BACKUP_TAG))@$$(yq e '."containerimage.digest"' images/mariadb-backup.json -o json -r)" \
		> images/mariadb-backup.tag
	rm -f images/mariadb-backup.json
