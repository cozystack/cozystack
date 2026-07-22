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
	# Vendor the v1alpha2 CRDs into the build context so migration 50 can ensure
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
	# Stamp BOTH consumers of this image. backupstrategy-controller renders it
	# into the Altinity strategy Pod as a curl+jq runner — a WORKAROUND, not a
	# design: that Pod needs only curl/jq/sleep/date but gets 349 MB including
	# kubectl, helm and etcd-migrate, and this image's ENTRYPOINT is the
	# migration runner itself. It should get a dedicated ~23 MB image owned by
	# its own package, which would delete this cross-package stamp; see the TODO
	# in ../../system/backupstrategy-controller/values.yaml.
	#
	# Until then the two pins MUST move together. The second copy had no
	# producer, so it silently froze at v1.4.0-rc.2 while this one advanced, and
	# a promotion then tried to retag two different digests to the same stable
	# tag — which the write-once guard in hack/promote-retag.sh turns into a
	# failed release. hack/image-pin-consistency.bats keeps them in lockstep.
	IMAGE="$(REGISTRY)/platform-migrations:$(IMAGE_TAG)@$$(yq -e -o json -r '.["containerimage.digest"]' images/migrations.json)" \
		yq --inplace '.migrations.image = strenv(IMAGE)' values.yaml
	IMAGE="$(REGISTRY)/platform-migrations:$(IMAGE_TAG)@$$(yq -e -o json -r '.["containerimage.digest"]' images/migrations.json)" \
		yq --inplace '.backupStrategyController.chBackupClientImage = strenv(IMAGE)' \
		../../system/backupstrategy-controller/values.yaml
	rm -f images/migrations.json
