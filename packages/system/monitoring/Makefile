NAME=monitoring
NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .
