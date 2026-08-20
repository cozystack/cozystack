include ../../../hack/package.mk

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'vminstance' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/vminstance/types.go
	# Immutable: fullnameOverride drives the rendered VirtualMachine's metadata.name,
	# so editing it on a live VMInstance makes Helm prune the old VirtualMachine and
	# recreate it under the new name — a disruptive re-roll of a running VM. The
	# transition rule leaves absent -> set alone, which is how the adoption
	# controller stamps it at creation.
	yq -i -o json '.properties.fullnameOverride = {"description": "Override the VirtualMachine resource name. Used by vm-adoption-controller to match imported VM names.", "type": "string", "x-kubernetes-validations": [{"rule": "self == oldSelf", "message": "fullnameOverride is immutable"}]}' values.schema.json
	../../../hack/update-crd.sh
