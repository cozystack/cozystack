export NAME=address-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Upstream publishes no chart artifact, so the chart is taken from the
# repository at a pinned commit. Bump together with the image in values.yaml,
# which must carry the same controller code: upstream's release job tags each
# main build as main-<short sha>, and the digest is in that job's log or from
# `crane digest`.
ADDRESS_CONTROLLER_REPO ?= https://github.com/cozystack/address-controller.git
ADDRESS_CONTROLLER_REF ?= 0ed4cc53de935ba108c012e43008fc456b806f79

test:
	helm unittest .

update:
	rm -rf charts tmp-repo
	git clone --filter=tree:0 --no-checkout $(ADDRESS_CONTROLLER_REPO) tmp-repo
	git -C tmp-repo checkout --quiet $(ADDRESS_CONTROLLER_REF)
	mkdir -p charts
	cp -r tmp-repo/chart/address-controller charts/
	rm -rf tmp-repo
