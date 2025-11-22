NAME=cozystack-platform
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-migrations

image-migrations:
	docker buildx build images/migrations \
		--tag $(REGISTRY)/migrations:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/migrations:latest \
		--cache-to type=inline \
		--metadata-file images/migrations.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/migrations:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/migrations.json -o json -r)" \
		yq -i '.migrations.image = strenv(IMAGE)' values.yaml
	rm -f images/migrations.json

generate:
	@echo "Generating target version from migrations..."
	@LAST_MIGRATION=$$(ls -1 images/migrations/migrations/ 2>/dev/null | grep -E '^[0-9]+$$' | sort -n | tail -1); \
	if [ -z "$$LAST_MIGRATION" ]; then \
		echo "Error: No migration files found" >&2; \
		exit 1; \
	fi; \
	TARGET_VERSION=$$((LAST_MIGRATION + 1)); \
	echo "Last migration: $$LAST_MIGRATION, Target version: $$TARGET_VERSION"; \
	yq -i '.migrations.targetVersion = $$TARGET_VERSION' values.yaml; \
	echo "Updated targetVersion to $$TARGET_VERSION in values.yaml"
