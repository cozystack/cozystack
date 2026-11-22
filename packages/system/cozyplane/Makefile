export NAME=cozyplane
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# The lllamnyp/cozyplane ref charts/cozyplane is vendored from, verbatim. Keep the
# three cozyplane packages on the same ref; Cozystack's settings live in this
# wrapper, never in the vendored chart.
COZYPLANE_REF=b99171ea7c33319dad4f2b401898c1ddb089d85b

test:
	helm unittest .

update:
	mkdir -p charts && tmp=$$(mktemp -d charts/.update.XXXXXX) && \
	{ curl -fsSL https://github.com/lllamnyp/cozyplane/archive/$(COZYPLANE_REF).tar.gz | \
	  tar -xzf - -C "$$tmp" --strip-components=3 cozyplane-$(COZYPLANE_REF)/chart/cozyplane || \
	  { rm -rf "$$tmp"; exit 1; }; } && \
	rm -rf charts/cozyplane && mv "$$tmp" charts/cozyplane
