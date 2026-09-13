export NAME=metallb-iad
export NAMESPACE=cozy-$(NAME)

# The chart is vendored from lllamnyp/metallb-iad at a pinned commit: the
# repository carries no release tags yet, so a bump is a change of this
# variable followed by `make update`.
METALLB_IAD_COMMIT=e27c972b7a52946190d6a94e93f0e22deaab5119

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	curl -sSL https://github.com/lllamnyp/metallb-iad/archive/$(METALLB_IAD_COMMIT).tar.gz | \
	tar xzvf - -C charts --strip 2 metallb-iad-$(METALLB_IAD_COMMIT)/chart

test:
	helm unittest .
