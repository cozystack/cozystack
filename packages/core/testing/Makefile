NAMESPACE=cozy-e2e-tests
NAME := sandbox
CLEAN := 1
TESTING_APPS := $(shell find ../../apps -maxdepth 1 -mindepth 1 -type d | awk -F/ '{print $$NF}')
SANDBOX_NAME := cozy-e2e-sandbox-airgapped

ROOT_DIR = $(dir $(abspath $(firstword $(MAKEFILE_LIST))/../../..))

include ../../../scripts/common-envs.mk


help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {sub("\\\\n",sprintf("\n%22c"," "), $$2);printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

image: image-e2e-sandbox

image-e2e-sandbox:
	docker buildx build -f images/e2e-sandbox/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
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

test: test-cluster test-apps ## Run the end-to-end tests in existing sandbox

test-cluster: ## Run the end-to-end for creating a cluster
	docker cp ../../../_out/assets/cozystack-installer.yaml "${SANDBOX_NAME}":/workspace/_out/assets/cozystack-installer.yaml
	docker cp ../../../_out/assets/nocloud-amd64.raw.xz "${SANDBOX_NAME}":/workspace/_out/assets/nocloud-amd64.raw.xz
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-cluster.bats'

test-apps: ## Run the end-to-end tests for apps
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-apps.bats'

delete: ## Remove sandbox from existing Kubernetes cluster.
	docker rm -f "${SANDBOX_NAME}" || true

exec:  ## Opens an interactive shell in the sandbox container.
	docker exec -ti "${SANDBOX_NAME}" bash

apply: delete
	docker run \
		-d --rm --name "${SANDBOX_NAME}" --privileged \
		-e TALOSCONFIG=/workspace/talosconfig \
		-e KUBECONFIG=/workspace/kubeconfig \
		"$$(yq .e2e.image values.yaml)" \
		sleep infinity
	docker cp "${ROOT_DIR}" "${SANDBOX_NAME}":/workspace
