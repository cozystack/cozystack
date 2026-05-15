include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'postgresql' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/postgresql/types.go
	# Patch tls.enabled to be nullable (cozyvalues-gen emits "type": "boolean" only;
	# we need ["boolean","null"] for the tri-state).
	jq '.properties.tls.properties.enabled.type = ["boolean", "null"]' values.schema.json > values.schema.json.tmp && mv values.schema.json.tmp values.schema.json
	jq -e '.properties.tls.properties.enabled.type == ["boolean", "null"]' values.schema.json > /dev/null || (echo "ERROR: jq patch for tls.enabled.type failed — cozyvalues-gen output may have drifted" && exit 1)
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

test:
	helm unittest .
