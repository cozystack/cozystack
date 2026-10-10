export NAME=address-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Upstream publishes no chart artifact, so the chart is taken from the
# repository at a pinned commit: the same commit the image in values.yaml was
# built from, so chart and controller cannot disagree. Bump the two together;
# upstream's release job tags each main build as main-<short sha>, and the
# digest is in that job's log or from `crane digest`.
ADDRESS_CONTROLLER_REPO ?= https://github.com/cozystack/address-controller.git
ADDRESS_CONTROLLER_REF ?= ee38d0a14d212bf895ca342dc3bcb8aad89ac89a

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(ADDRESS_CONTROLLER_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(ADDRESS_CONTROLLER_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/address-controller charts/
	rm -rf tmp-repo
