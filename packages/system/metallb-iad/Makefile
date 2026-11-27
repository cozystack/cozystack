export NAME=metallb-iad
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Same pinning story as packages/system/address-controller.
METALLB_IAD_REPO ?= https://github.com/cozystack/metallb-iad.git
METALLB_IAD_REF ?= b4d2fdc4b879b96bfc423392e8aea0ee2cb5f029

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(METALLB_IAD_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(METALLB_IAD_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/metallb-iad charts/
	rm -rf tmp-repo
