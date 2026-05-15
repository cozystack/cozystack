include ../../../hack/package.mk

.PHONY: generate update

generate:
	cozyvalues-gen -m 'mongodb' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/mongodb/types.go
	jq '.properties.tls.properties.enabled.type = ["boolean", "null"]' values.schema.json > values.schema.json.tmp && mv values.schema.json.tmp values.schema.json
	jq -e '.properties.tls.properties.enabled.type == ["boolean", "null"]' values.schema.json > /dev/null || (echo "ERROR: jq patch failed" && exit 1)
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

test:
	helm unittest .
