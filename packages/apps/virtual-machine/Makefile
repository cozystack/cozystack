include ../../../scripts/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	yq -o json -i '.properties.gpus.items.type = "object" | .properties.gpus.default = []' values.schema.json
	# INSTANCE_TYPES=$$(yq e '.metadata.name' -o=json -r ../../system/kubevirt-instancetypes/templates/instancetypes.yaml | yq 'split(" ") | . + [""]' -o json) \
	#  && yq -i -o json ".properties.instanceType.enum = $${INSTANCE_TYPES}" values.schema.json
	PREFERENCES=$$(yq e '.metadata.name' -o=json -r ../../system/kubevirt-instancetypes/templates/preferences.yaml | yq 'split(" ") | . + [""]' -o json) \
	  && yq -i -o json ".properties.instanceProfile.enum = $${PREFERENCES}" values.schema.json
