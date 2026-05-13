include ../../../hack/common-envs.mk
include ../../../hack/package.mk

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md

test:
	$(MAKE) -C ../../tests/cozy-tls-tests/ test
