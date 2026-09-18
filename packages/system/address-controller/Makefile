export NAME=address-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Upstream publishes no chart artifact and cuts no tags yet, so the chart is
# taken from the repository at a pinned commit. Replace ADDRESS_CONTROLLER_REF
# with the tag once upstream starts releasing; the partial clone is what lets a
# bare commit be checked out without fetching the whole history.
ADDRESS_CONTROLLER_REPO ?= https://github.com/lllamnyp/address-controller.git
ADDRESS_CONTROLLER_REF ?= 8a89641b82c62aded86d2b19c2f286c59e019ef4

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(ADDRESS_CONTROLLER_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(ADDRESS_CONTROLLER_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/address-controller charts/
	rm -rf tmp-repo
