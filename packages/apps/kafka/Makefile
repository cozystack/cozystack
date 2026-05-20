include ../../../hack/package.mk
PRESET_ENUM := ["nano","micro","small","medium","large","xlarge","2xlarge"]

generate:
	cozyvalues-gen -m 'kafka' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kafka/types.go
	sed -i 's/Enabled bool `json:"enabled,omitempty"`/Enabled *bool `json:"enabled,omitempty"`/' ../../../api/apps/v1alpha1/kafka/types.go
	grep -q 'Enabled \*bool `json:"enabled,omitempty"`' ../../../api/apps/v1alpha1/kafka/types.go || (echo "ERROR: failed to patch TLS.Enabled to *bool in types.go" && exit 1)
	jq '.properties.tls.properties.enabled.type = ["boolean", "null"]' values.schema.json > values.schema.json.tmp && mv values.schema.json.tmp values.schema.json
	jq -e '.properties.tls.properties.enabled.type == ["boolean", "null"]' values.schema.json > /dev/null || (echo "ERROR: jq patch failed" && exit 1)
	../../../hack/update-crd.sh

test:
	helm unittest .
