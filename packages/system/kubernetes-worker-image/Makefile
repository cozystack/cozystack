export NAME=kubernetes-worker-image
export NAMESPACE=cozy-system

include ../../../hack/package.mk

.PHONY: test

test:
	helm unittest .
