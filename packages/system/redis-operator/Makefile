REDIS_OPERATOR_TAG=$(shell grep -F 'ARG VERSION=' images/redis-operator/Dockerfile | cut -f2 -d=)
export NAME=redis-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# The chart is vendored from the cozystack fork, the same source the operator
# image is built from, and pinned to the tag in images/redis-operator/Dockerfile.
# Upstream spotahome must not be used here: the RedisFailover CRD shipped in
# crds/ carries the fork's spec.tls fields, so re-vendoring from spotahome would
# drop them and every RedisFailover the redis chart renders would be rejected.
# The fork keeps the chart directory as charts/redisoperator while the chart
# itself is named redis-operator, hence the rename after extraction.
#
# Note on the ClusterRole this vendors: it grants create/get/patch/update on
# Secrets and full verbs on cert-manager Certificates cluster-wide, which is
# wider than this platform needs — the operator only reads the TLS Secret and
# publishes the CA-only one, and it never takes the cert-manager path because
# the chart always supplies spec.tls.certificateSecret. It is not narrowed here
# because everything under charts/ is overwritten by the update target above;
# the fix belongs in the fork, either as a namespace-scoped Role or behind a
# chart value, and a downstream edit would be silently reverted on the next
# `make update`.
update:
	rm -rf charts
	mkdir -p charts
	cd charts && \
	tag=$(REDIS_OPERATOR_TAG) && \
	curl -sSL https://github.com/cozystack/redis-operator/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 2 redis-operator-$${tag#v}/charts/redisoperator && \
	mv redisoperator redis-operator
	sed -i '/{{/d' charts/redis-operator/crds/databases.spotahome.com_redisfailovers.yaml

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
