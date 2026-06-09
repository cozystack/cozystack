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
	docker buildx build images/migrations \
		$(call image-tags,platform-migrations,v$(COZYSTACK_VERSION)) \
		--cache-from type=registry,ref=$(CACHE_REGISTRY)/platform-migrations:latest \
		--cache-to type=inline \
		--metadata-file images/migrations.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/platform-migrations:$(IMAGE_TAG)@$$(yq -e -o json -r '.["containerimage.digest"]' images/migrations.json)" \
		yq --inplace '.migrations.image = strenv(IMAGE)' values.yaml
	rm -f images/migrations.json
