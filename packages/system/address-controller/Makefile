export NAME=address-controller
export NAMESPACE=cozy-$(NAME)

# The chart is vendored from lllamnyp/address-controller at a pinned commit:
# the repository carries no release tags yet, so a bump is a change of this
# variable followed by `make update`.
ADDRESS_CONTROLLER_COMMIT=8a89641b82c62aded86d2b19c2f286c59e019ef4

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	curl -sSL https://github.com/lllamnyp/address-controller/archive/$(ADDRESS_CONTROLLER_COMMIT).tar.gz | \
	tar xzvf - -C charts --strip 2 address-controller-$(ADDRESS_CONTROLLER_COMMIT)/chart

test:
	helm unittest .
