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
	docker buildx build -f images/e2e-sandbox/Dockerfile images/e2e-sandbox \
		--tag $(REGISTRY)/e2e-sandbox:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/e2e-sandbox:latest \
		--cache-to type=inline \
		--metadata-file images/e2e-sandbox.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/e2e-sandbox:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/e2e-sandbox.json -o json -r)" \
		yq -i '.e2e.image = strenv(IMAGE)' values.yaml
	rm -f images/e2e-sandbox.json

test: test-cluster test-openapi test-apps ## Run the end-to-end tests in existing sandbox

copy-nocloud-image:
	docker cp ../../../_out/assets/nocloud-amd64.raw.xz "${SANDBOX_NAME}":/workspace/_out/assets/nocloud-amd64.raw.xz

copy-installer-manifest:
	docker cp ../../../_out/assets/cozystack-installer.yaml "${SANDBOX_NAME}":/workspace/_out/assets/cozystack-installer.yaml

prepare-cluster: copy-nocloud-image
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-prepare-cluster.bats'

install-cozystack: copy-installer-manifest
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-install-cozystack.bats'

test-cluster: copy-nocloud-image copy-installer-manifest ## Run the end-to-end for creating a cluster
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-cluster.bats'

test-openapi:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-test-openapi.bats'

test-apps-%:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-apps/$*.bats'

collect-report: ## Collect the test report from the sandbox.
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozyreport.sh cozyreport'
	mkdir -p ../../../_out
	docker cp "${SANDBOX_NAME}:/workspace/cozyreport.tgz" ../../../_out/cozyreport.tgz

collect-images: ## Collect the list of images used in the sandbox.
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/collect-images.sh'
	mkdir -p ../../../_out
	docker cp "${SANDBOX_NAME}":/workspace/images.txt ../../../_out/images.txt

delete: ## Remove sandbox from existing Kubernetes cluster.
	docker rm -f "${SANDBOX_NAME}" || true

exec:  ## Opens an interactive shell in the sandbox container.
	docker exec -ti "${SANDBOX_NAME}" bash

apply: delete
	mkdir -p /tmp/${SANDBOX_NAME}
	chmod 777 /tmp/${SANDBOX_NAME}
	docker run \
		-d --rm --name "${SANDBOX_NAME}" --privileged \
		-e TALOSCONFIG=/workspace/talosconfig \
		-e KUBECONFIG=/workspace/kubeconfig \
		-e SANDBOX_NAME=${SANDBOX_NAME} \
		"$$(yq .e2e.image values.yaml)" \
		--timeout 30m
	docker cp "${ROOT_DIR}/." "${SANDBOX_NAME}":/workspace
