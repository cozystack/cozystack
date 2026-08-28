NAMESPACE=cozy-e2e-tests
NAME := sandbox
CLEAN := 1
TESTING_APPS := $(shell find ../../apps -maxdepth 1 -mindepth 1 -type d | awk -F/ '{print $$NF}')
SANDBOX_NAME := cozy-e2e-sandbox-$(shell echo "$$(hostname):$$(pwd)" | sha256sum | cut -c -6)
COZY_E2E_NODE_CPUS ?= 8
COZY_E2E_NODE_MEMORY_MIB ?= 24576

ROOT_DIR = $(dir $(abspath $(firstword $(MAKEFILE_LIST))/../../..))
COMPOSE_PROJECT ?= cozy-e2e
COMPOSE_FILE ?= $(ROOT_DIR)hack/e2e-compose.yaml
ZPOOL_BACKING_DIR ?= /var/lib/cozy-e2e-zpools

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

# Container lane: Talos nodes as containers instead of QEMU guests, so tenant
# workloads sit one nesting level shallower. Sits BESIDE prepare-cluster rather
# than replacing it; nothing downstream changes, because the addressing, CPU and
# memory in hack/e2e-compose.yaml mirror the QEMU lane exactly.
#
# Two steps, not one, because the halves must run in different places. The nodes
# have to be siblings on the runner's docker daemon for the /dev and /run/udev
# binds in hack/e2e-compose.yaml to resolve against the real host devtmpfs --
# that bind is what lets this lane carry LINSTOR at all -- so the host-side half
# needs a docker socket the sandbox does not have. Everything Talos-side then
# runs inside the sandbox exactly as the QEMU lane does.
#
# No copy-nocloud-image dependency: containers boot from the Talos image
# directly and need no nocloud asset, which also means this lane does not wait
# on `make -C packages/core/talos assets`.
prepare-cluster-container: delete-cluster-container
	SANDBOX_NAME="${SANDBOX_NAME}" COMPOSE_PROJECT="${COMPOSE_PROJECT}" COMPOSE_FILE="${COMPOSE_FILE}" ZPOOL_BACKING_DIR="${ZPOOL_BACKING_DIR}" COZY_E2E_NODE_CPUS="${COZY_E2E_NODE_CPUS}" COZY_E2E_NODE_MEMORY_MIB="${COZY_E2E_NODE_MEMORY_MIB}" $(ROOT_DIR)hack/e2e-container-up.sh
	docker exec -e COZY_E2E_NODE_CPUS="${COZY_E2E_NODE_CPUS}" -e COZY_E2E_NODE_MEMORY_MIB="${COZY_E2E_NODE_MEMORY_MIB}" "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-prepare-cluster-container.bats'

# Tear the container lane down. Docker volumes hold etcd and kubelet state, and
# the host zpools hold LINSTOR data, so both halves must disappear before a
# retry or the next run can adopt a half-dead cluster.
# USERDATA is set to a placeholder because compose interpolates the whole file
# before it decides what the subcommand needs, so the `${USERDATA:?}` guard that
# makes a missing machine config fail loudly on `up` also fails `down`, where no
# machine config is required. The value is never read: `down` matches containers
# by project and service name. Without it teardown always errored, `|| true`
# swallowed it, and the nodes and their /var volumes survived into the next run
# -- the exact half-dead cluster this target exists to prevent.
delete-cluster-container:
	USERDATA=teardown docker compose -p "${COMPOSE_PROJECT}" -f "${COMPOSE_FILE}" down -v || true
	@set -eu; \
		if [ -z "${ZPOOL_BACKING_DIR}" ] || [ "${ZPOOL_BACKING_DIR}" = / ]; then \
			echo "refusing unsafe ZPOOL_BACKING_DIR '${ZPOOL_BACKING_DIR}'" >&2; \
			exit 1; \
		fi; \
		if [ "$$(id -u)" -eq 0 ]; then sudo_cmd=; else sudo_cmd=sudo; fi; \
		for n in 1 2 3; do \
			if $$sudo_cmd zpool list "data-srv$${n}" >/dev/null 2>&1; then \
				$$sudo_cmd zpool destroy "data-srv$${n}"; \
			fi; \
			$$sudo_cmd rm -f "${ZPOOL_BACKING_DIR}/srv$${n}.img"; \
		done; \
		$$sudo_cmd rmdir "${ZPOOL_BACKING_DIR}" 2>/dev/null || true

# The API endpoint and E2E-only storage mode cross into the sandbox explicitly:
# docker exec starts a fresh environment, so values exported by the caller do
# not reach the manifest generator or root-tenant setup. Empty values retain the
# QEMU defaults; the container lane supplies its node endpoint, disables DRBD,
# and selects local storage because its Talos nodes share one kernel.
install-cozystack:
	docker exec -e COZY_APISERVER_ENDPOINT="${COZY_APISERVER_ENDPOINT}" -e COZY_LINSTOR_DRBD_ENABLED="${COZY_LINSTOR_DRBD_ENABLED}" -e COZY_E2E_STORAGE_CLASS="${COZY_E2E_STORAGE_CLASS}" "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-install-cozystack.bats'

test-openapi:
	docker exec "${SANDBOX_NAME}" sh -c 'cd /workspace && hack/cozytest.sh hack/e2e-test-openapi.bats'

# Run the Chainsaw E2E app suite. Chainsaw loads .chainsaw.yaml from the cwd,
# discovers every chainsaw-test.yaml under hack/e2e-chainsaw/, polls each
# assertion to its own timeout, and writes a JUnit chainsaw-report.xml the CI
# job uploads. CHAINSAW_SUITES restricts the run to the named suite dirs
# (space-separated, e.g. "postgres bucket"); empty runs the whole suite. The
# CI Test-Impact-Analysis step (hack/select-e2e.sh) sets it from the PR diff.
# COZY_E2E_STORAGE_CLASS is passed both to script operations and as a Chainsaw
# value for templated resource files; empty keeps the QEMU default, replicated.
test-chainsaw:
	docker exec -e COZY_E2E_STORAGE_CLASS="${COZY_E2E_STORAGE_CLASS}" "${SANDBOX_NAME}" sh -c 'cd /workspace/hack/e2e-chainsaw && chainsaw test --set-string storageClass="$${COZY_E2E_STORAGE_CLASS:-replicated}" ${CHAINSAW_SUITES}'

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
