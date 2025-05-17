NAMESPACE=cozy-e2e-tests
NAME := sandbox
CLEAN := 1
TESTING_APPS := $(shell find ../../apps -maxdepth 1 -mindepth 1 -type d | awk -F/ '{print $$NF}')
SANDBOX_NAME := cozy-e2e-sandbox-$(shell echo "$$(hostname):$$(pwd)" | sha256sum | cut -c -6)

ROOT_DIR = $(dir $(abspath $(firstword $(MAKEFILE_LIST))/../../..))

include ../../../scripts/common-envs.mk


help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {sub("\\\\n",sprintf("\n%22c"," "), $$2);printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

image: image-e2e-sandbox

image-e2e-sandbox:
	docker buildx build -f images/e2e-sandbox/Dockerfile ../../.. \
		--provenance false \
		--tag $(REGISTRY)/e2e-sandbox:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/e2e-sandbox:latest \
		--cache-to type=inline \
		--metadata-file images/e2e-sandbox.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/e2e-sandbox:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/e2e-sandbox.json -o json -r)" \
		yq -i '.e2e.image = strenv(IMAGE)' values.yaml
	rm -f images/e2e-sandbox.json

test: ## Run the end-to-end tests in existing sandbox.
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && export COZYSTACK_INSTALLER_YAML=$$(helm template -n cozy-system installer ./packages/core/installer) && hack/e2e.sh'

delete: ## Remove sandbox from existing Kubernetes cluster.
	docker rm -f "${SANDBOX_NAME}" || true

exec:  ## Opens an interactive shell in the sandbox container.
	docker exec -ti "${SANDBOX_NAME}" bash

apply: delete
	docker run -d --rm --name "${SANDBOX_NAME}" --privileged "$$(yq .e2e.image values.yaml)" sleep infinity
	docker cp "${ROOT_DIR}" "${SANDBOX_NAME}":/workspace
