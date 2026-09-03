NAMESPACE=cozy-e2e-tests
NAME := sandbox
CLEAN := 1
TESTING_APPS := $(shell find ../../apps -maxdepth 1 -mindepth 1 -type d | awk -F/ '{print $$NF}')
SANDBOX_NAME := cozy-e2e-sandbox-$(shell echo "$$(hostname):$$(pwd)" | sha256sum | cut -c -6)

ROOT_DIR = $(dir $(abspath $(firstword $(MAKEFILE_LIST))/../../..))

include ../../../hack/common-envs.mk


help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {sub("\\\\n",sprintf("\n%22c"," "), $$2);printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

image: image-e2e-sandbox

image-e2e-sandbox:
	docker buildx build -f images/e2e-sandbox/Dockerfile images/e2e-sandbox \
		$(call image-tags,e2e-sandbox,v$(COZYSTACK_VERSION)) \
		$(call cache-args,e2e-sandbox) \
		--metadata-file images/e2e-sandbox.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/e2e-sandbox:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/e2e-sandbox.json -o json -r)" \
		yq -i '.e2e.image = strenv(IMAGE)' values.yaml
	rm -f images/e2e-sandbox.json

# Named `e2e` (not `test`) so hack/helm-unit-tests.sh — which runs the `test`
# target of every package whose `make -n test` resolves — does not pick up this
# sandbox-driven e2e flow and try to run it in the unit-test phase (no sandbox
# exists then). The root `make test` target invokes this.
e2e: test-openapi test-chainsaw ## Run the end-to-end tests in existing sandbox

copy-nocloud-image:
	docker cp ../../../_out/assets/nocloud-amd64.raw.xz "${SANDBOX_NAME}":/workspace/_out/assets/nocloud-amd64.raw.xz

prepare-cluster: copy-nocloud-image
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-prepare-cluster.bats'

install-cozystack:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-install-cozystack.bats'

test-openapi:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-test-openapi.bats'

# Run the Chainsaw E2E app suite. Chainsaw loads .chainsaw.yaml from the cwd,
# discovers every chainsaw-test.yaml under hack/e2e-chainsaw/, polls each
# assertion to its own timeout, and writes a JUnit chainsaw-report.xml the CI
# job uploads. CHAINSAW_SUITES restricts the run to the named suite dirs
# (space-separated, e.g. "postgres bucket"); empty runs the whole suite. The
# CI Test-Impact-Analysis step (hack/select-e2e.sh) sets it from the PR diff.
test-chainsaw:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace/hack/e2e-chainsaw && chainsaw test ${CHAINSAW_SUITES}'

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

# Which machine this run landed on, recorded before the container exists.
#
# Here rather than in a workflow step for two reasons. The metadata service
# answers on a link-local address the container is not guaranteed to reach, so
# the read has to happen on this side of `docker run`; and the record has to be
# inside the tree the `docker cp` below carries in, or hack/cozyreport.sh cannot
# fold it into the artifact from inside. Every workflow that runs the suite reaches
# the sandbox through `make prepare-env`, so one call here covers each of them on
# both the green and the red path, with no per-workflow step to keep in step. No
# count of those workflows here on purpose: the count is what goes stale, and the
# funnel is the property worth stating.
#
# Ahead of `docker run`, so a runner whose sandbox fails to start is still
# identified. Never fatal: the script says why it could not read something and
# exits 0. On a machine with no metadata service -- a laptop running this target
# directly -- it costs the read's own ceiling once, against a target that then
# boots three QEMU guests.
apply: delete
	COZY_RUNNER_IDENTITY_FILE="$(ROOT_DIR)_out/runner-identity.txt" $(ROOT_DIR)hack/e2e-runner-identity.sh
	docker run \
		-d --rm --name "${SANDBOX_NAME}" --privileged \
		-e TALOSCONFIG=/workspace/talosconfig \
		-e KUBECONFIG=/workspace/kubeconfig \
		"$$(yq .e2e.image values.yaml)" \
		--timeout 30m
	docker cp "${ROOT_DIR}/." "${SANDBOX_NAME}":/workspace
