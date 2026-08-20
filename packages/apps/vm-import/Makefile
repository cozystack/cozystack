include ../../../hack/package.mk

.PHONY: generate test

test:
	helm unittest .

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	yq -i -o json '.required = ["sourceUrl", "sourceSecretName"]' values.schema.json
	../../../hack/update-crd.sh
