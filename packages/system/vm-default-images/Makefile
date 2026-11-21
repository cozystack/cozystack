export NAME=vm-default-images
export NAMESPACE=cozy-system

include ../../../hack/package.mk

.PHONY: test

test:
	helm unittest .
