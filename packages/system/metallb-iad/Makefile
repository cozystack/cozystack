export NAME=metallb-iad
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Same pinning story as packages/system/address-controller.
METALLB_IAD_REPO ?= https://github.com/cozystack/metallb-iad.git
METALLB_IAD_REF ?= 7d0017a0f22b2a4579ae01be8a6e4f3870bce7ef

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(METALLB_IAD_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(METALLB_IAD_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/metallb-iad charts/
	rm -rf tmp-repo
