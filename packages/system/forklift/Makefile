export NAME=forklift
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .
