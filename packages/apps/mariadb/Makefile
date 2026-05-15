MARIADB_BACKUP_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

generate:
	@rm -f values.schema.json.tmp
	cozyvalues-gen -m 'mariadb' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/mariadb/types.go
	# Patch tls.enabled to be nullable (cozyvalues-gen emits "type": "boolean" only;
	# we need ["boolean","null"] for the tri-state).
	jq '.properties.tls.properties.enabled.type = ["boolean", "null"]' values.schema.json > values.schema.json.tmp && mv values.schema.json.tmp values.schema.json || { rm -f values.schema.json.tmp; exit 1; }
	jq -e '.properties.tls.properties.enabled.type == ["boolean", "null"]' values.schema.json > /dev/null || (echo "ERROR: jq patch for tls.enabled.type failed — cozyvalues-gen output may have drifted" && exit 1)
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

.PHONY: test
test:
	helm unittest .

image:
	docker buildx build images/mariadb-backup \
		$(call image-tags,mariadb-backup,$(MARIADB_BACKUP_TAG)) \
		$(call cache-args,mariadb-backup) \
		--metadata-file images/mariadb-backup.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/mariadb-backup:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/mariadb-backup.json -o json -r)" \
		> images/mariadb-backup.tag
	rm -f images/mariadb-backup.json

test:
	helm unittest .
