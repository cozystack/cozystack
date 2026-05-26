include ../../../hack/package.mk

generate:
	cozyvalues-gen -m 'rabbitmq' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/rabbitmq/types.go
	# Patch tls object to reject unknown keys (cozyvalues-gen does not emit
	# additionalProperties: false for struct types).
	jq '.properties.tls.additionalProperties = false' values.schema.json > values.schema.json.tmp && mv values.schema.json.tmp values.schema.json
	jq -e '.properties.tls.additionalProperties == false' values.schema.json > /dev/null || (echo "ERROR: jq patch for tls.additionalProperties failed — cozyvalues-gen output may have drifted" && exit 1)
	../../../hack/update-crd.sh

update:
	hack/update-versions.sh
	make generate

test:
	helm unittest .
