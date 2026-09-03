export NAME=kubeovn-webhook
export NAMESPACE=cozy-kubeovn

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/kubeovn-webhook \
		$(call image-tags,kubeovn-webhook,v$(COZYSTACK_VERSION)) \
		$(call cache-args,kubeovn-webhook) \
		--metadata-file images/kubeovn-webhook.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/kubeovn-webhook:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubeovn-webhook.json -o json -r)" \
		yq -i '.image = strenv(IMAGE)' values.yaml
	rm -f images/kubeovn-webhook.json

# hack/helm-unit-tests.sh discovers suites by probing `make -n test` per package
# and SKIPS any package that has no such target. This package carried two
# suites under tests/ that consequently never ran in CI -- including the port
# agreement guard, whose whole purpose is to catch a drift that fails open.
# --with-subchart=false matches the other packages: run only our own suites.
#
# Declared phony because nothing else declares it: hack/package.mk writes its
# .PHONY as a variable assignment rather than a target, so it declares no
# target at all, and it would not list `test` in any case. Without this line a
# path named `test` next to the Makefile would make the target up to date and
# skip the recipe -- the same fail-open shape the port guard below exists for,
# and nothing reports it.
.PHONY: test
test:
	helm unittest --with-subchart=false .
