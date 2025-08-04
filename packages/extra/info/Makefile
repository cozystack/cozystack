NAME=etcd

include ../../../scripts/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	yq -o json -i '.properties = {}' values.schema.json