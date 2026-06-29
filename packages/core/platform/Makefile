NAME=cozystack-platform
NAMESPACE=cozy-system

include ../../../hack/common-envs.mk

test:
	helm unittest .

show:
	cozyhr show --namespace $(NAMESPACE) $(NAME)

apply:
	cozyhr apply --namespace $(NAMESPACE) $(NAME)

reconcile: apply

diff:
	cozyhr diff --namespace $(NAMESPACE) $(NAME)

# Strip the platform.cozystack.io/no-delete label from every guarded object so
# `helm uninstall cozy-platform` (and ad-hoc deletes of protected resources)
# is no longer denied by the deletion-protection VAP. After this runs, the
# cluster has no guardrail until the next `helm upgrade` re-stamps the label.
# Intended for teardown / disaster-recovery, not routine use.
unprotect:
	hack/unprotect.sh

image: image-migrations

image-migrations:
	# Vendor the v1alpha2 CRDs into the build context so migration 47 can ensure
	# they exist before adoption (the etcd-operator-crds HelmRelease only installs
	# AFTER this pre-upgrade hook). Copied from the committed source of truth —
	# no build-time network fetch, stays in lockstep with the chart. Removed again
	# after the build; the leading rm reclaims a stale copy from a failed build.
	rm -rf images/migrations/etcd-operator-crds
	mkdir -p images/migrations/etcd-operator-crds
	cp ../../system/etcd-operator-crds/templates/*.yaml images/migrations/etcd-operator-crds/
	docker buildx build images/migrations \
		$(call image-tags,platform-migrations,v$(COZYSTACK_VERSION)) \
		$(call cache-args,platform-migrations) \
		--metadata-file images/migrations.json \
		$(BUILDX_ARGS)
	rm -rf images/migrations/etcd-operator-crds
	IMAGE="$(REGISTRY)/platform-migrations:$(IMAGE_TAG)@$$(yq -e -o json -r '.["containerimage.digest"]' images/migrations.json)" \
		yq --inplace '.migrations.image = strenv(IMAGE)' values.yaml
	rm -f images/migrations.json
