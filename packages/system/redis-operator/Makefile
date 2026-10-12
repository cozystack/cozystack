REDIS_OPERATOR_TAG=$(shell grep -F 'ARG VERSION=' images/redis-operator/Dockerfile | cut -f2 -d=)
export NAME=redis-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm pull oci://ghcr.io/freshworks-oss/charts/redis-operator --version $(patsubst v%,%,$(REDIS_OPERATOR_TAG)) --untar --untardir charts
	# Re-apply the chart half of the downstream TLS support. The other half is a
	# source patch on the image (images/redis-operator/patches/tls.diff) and the
	# two only work together: the binary reads spec.tls, the CRD is what lets it
	# through admission, and the ClusterRole is what lets it publish the CA-only
	# Secret. Drop one and TLS fails silently rather than loudly.
	#
	# Both are carried as patches because rm -rf charts above discards any
	# hand-edit, and the fix belongs upstream (freshworks-oss/redis-operator#85)
	# rather than here. Retire these when a release carrying it lands.
	#
	# --forward guards a re-run by hand over a tree that already carries the
	# change, where patch(1) defaults diverge by implementation: GNU 2.7.6
	# refuses and exits 1, Apple 2.0-12u11 takes its "Assume -R? [y]" default and
	# strips the hunk back out with exit 0, and GNU under --batch does the same.
	# --forward refuses in all three, leaving a .rej and a non-zero exit.
	patch --forward --no-backup-if-mismatch -p1 < patches/crd-tls.diff
	patch --forward --no-backup-if-mismatch -p1 < patches/rbac-tls.diff

image:
	docker buildx build images/redis-operator \
		$(call image-tags,redis-operator,$(REDIS_OPERATOR_TAG)) \
		$(call cache-args,redis-operator) \
		--metadata-file images/redis-operator.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/redis-operator" \
		yq -i '.redis-operator.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/redis-operator.json -o json -r) \
		yq -i '.redis-operator.image.tag = strenv(TAG)' values.yaml
	rm -f images/redis-operator.json
