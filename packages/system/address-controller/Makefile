export NAME=address-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Upstream publishes no chart artifact, so the chart is taken from the
# repository at a release tag. Bump together with the image tag+digest in
# values.yaml: upstream's release job tags the image without the leading `v`
# (git tag v0.1.0 -> image 0.1.0), and `crane digest` resolves the digest.
ADDRESS_CONTROLLER_REPO ?= https://github.com/lllamnyp/address-controller.git
ADDRESS_CONTROLLER_REF ?= v0.1.0

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(ADDRESS_CONTROLLER_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(ADDRESS_CONTROLLER_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/address-controller charts/
	rm -rf tmp-repo
