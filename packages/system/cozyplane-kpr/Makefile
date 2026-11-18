export NAME=cozyplane-kpr
export NAMESPACE=cozy-cozyplane

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# The lllamnyp/cozyplane ref charts/cozyplane-kpr is vendored from, verbatim. Keep the
# three cozyplane packages on the same ref; Cozystack's settings live in this
# wrapper, never in the vendored chart.
COZYPLANE_REF=a0f32e272640c6ac99d863622570bdf9677bf7ab

test:
	helm unittest .

update:
	tmp=$$(mktemp -d) && \
	curl -fsSL https://github.com/lllamnyp/cozyplane/archive/$(COZYPLANE_REF).tar.gz | \
	  tar -xzf - -C "$$tmp" --strip-components=3 cozyplane-$(COZYPLANE_REF)/chart/cozyplane-kpr && \
	rm -rf charts/cozyplane-kpr && mkdir -p charts && mv "$$tmp" charts/cozyplane-kpr
