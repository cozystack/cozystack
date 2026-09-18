export NAME=metallb-iad
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Same pinning story as packages/system/address-controller.
METALLB_IAD_REPO ?= https://github.com/cozystack/metallb-iad.git
METALLB_IAD_REF ?= 2f983b67ed6ab86d745922a6bff9d89f18fc681d

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(METALLB_IAD_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(METALLB_IAD_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/metallb-iad charts/
	rm -rf tmp-repo
