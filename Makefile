.PHONY: manifests assets unit-tests helm-unit-tests bats-unit-tests bats-posix-compat-tests print-bats-unit-files print-bats-jobs rd-presets-check migrations-target-check test test-controllers preflight

include hack/common-envs.mk

build-deps:
	@command -V find docker skopeo jq gh helm > /dev/null
	@yq --version | grep -q "mikefarah" || (echo "mikefarah/yq is required" && exit 1)
	@tar --version | grep -q GNU || (echo "GNU tar is required" && exit 1)
	@sed --version | grep -q GNU || (echo "GNU sed is required" && exit 1)
	@awk --version | grep -q GNU || (echo "GNU awk is required" && exit 1)

build: build-deps
	make -C packages/apps/http-cache image
	make -C packages/apps/mariadb image
	make -C packages/apps/clickhouse image
	make -C packages/apps/kubernetes image
	make -C packages/system/cozystack-api image
	make -C packages/system/cozystack-controller image
	make -C packages/system/backup-controller image
	make -C packages/system/backupstrategy-controller image
	make -C packages/system/lineage-controller-webhook image
	make -C packages/system/flux-shard-operator image
	make -C packages/system/cilium image
	make -C packages/system/linstor image
	make -C packages/system/linstor-gui image
	make -C packages/system/kubeovn-webhook image
	make -C packages/system/kubeovn-plunger image
	make -C packages/system/dashboard image
	make -C packages/system/metallb image
	make -C packages/system/kamaji image
	make -C packages/system/capi-providers-cpprovider image
	make -C packages/system/multus image
	make -C packages/system/bucket image
	make -C packages/system/objectstorage-controller image
	make -C packages/system/securitygroup-controller image
	make -C packages/system/grafana-operator image
	make -C packages/core/testing image
	make -C packages/core/talos image
	make -C packages/core/platform image
	make -C packages/core/installer image
	make manifests

manifests:
	mkdir -p _out/assets
	cat internal/crdinstall/manifests/*.yaml > _out/assets/cozystack-crds.yaml
	# kubectl-apply install path: render the bare Namespace resource alongside
	# the operator. bareNamespace=true gates a Namespace cozy-system with PSA +
	# identity labels (see packages/core/installer/templates/cozy-system-namespace.yaml).
	# helm install/upgrade users keep the default (false) and use --create-namespace
	# + the pre-install labeler hook.
	# platformVersion is rendered into the COZYSTACK_VERSION env on the operator so
	# the install artifacts carry the version as deploy-time data (not baked into
	# the image). Promotion re-renders these with the stable version, no rebuild.
	# Talos variant (default)
	helm template installer packages/core/installer -n cozy-system \
		--set bareNamespace=true \
		--set cozystackOperator.platformVersion=$(if $(COZYSTACK_VERSION),v$(COZYSTACK_VERSION),) \
		--show-only templates/cozy-system-namespace.yaml \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-talos.yaml
	# Generic Kubernetes variant (k3s, kubeadm, RKE2)
	helm template installer packages/core/installer -n cozy-system \
		--set bareNamespace=true \
		--set cozystackOperator.variant=generic \
		--set cozystackOperator.platformVersion=$(if $(COZYSTACK_VERSION),v$(COZYSTACK_VERSION),) \
		--set cozystack.apiServerHost=REPLACE_ME \
		--show-only templates/cozy-system-namespace.yaml \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-generic.yaml
	# Hosted variant (managed Kubernetes)
	helm template installer packages/core/installer -n cozy-system \
		--set bareNamespace=true \
		--set cozystackOperator.variant=hosted \
		--set cozystackOperator.platformVersion=$(if $(COZYSTACK_VERSION),v$(COZYSTACK_VERSION),) \
		--show-only templates/cozy-system-namespace.yaml \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-hosted.yaml

cozypkg:
	go build -ldflags "-X github.com/cozystack/cozystack/cmd/cozypkg/cmd.Version=v$(COZYSTACK_VERSION)" -o _out/bin/cozypkg ./cmd/cozypkg

# Operator diagnostic: reports non-ready cozystack / Flux / Kubernetes resources.
check-readiness:
	go build -ldflags "-X main.Version=v$(COZYSTACK_VERSION)" -o _out/bin/check-readiness ./cmd/check-readiness

assets: assets-talos assets-cozypkg openapi-json

openapi-json:
	mkdir -p _out/assets
	VERSION=$(if $(COZYSTACK_VERSION),v$(COZYSTACK_VERSION),$(shell git describe --tags --always 2>/dev/null || echo dev)) go run ./tools/openapi-gen/ 2>/dev/null > _out/assets/openapi.json

assets-talos:
	make -C packages/core/talos assets

assets-cozypkg: assets-cozypkg-linux-amd64 assets-cozypkg-linux-arm64 assets-cozypkg-darwin-amd64 assets-cozypkg-darwin-arm64 assets-cozypkg-windows-amd64 assets-cozypkg-windows-arm64
	(cd _out/assets/ && sha256sum cozypkg-*.tar.gz) > _out/assets/cozypkg-checksums.txt

assets-cozypkg-%:
	$(eval EXT := $(if $(filter windows,$(firstword $(subst -, ,$*))),.exe,))
	mkdir -p _out/assets
	GOOS=$(firstword $(subst -, ,$*)) GOARCH=$(lastword $(subst -, ,$*)) go build -ldflags "-X github.com/cozystack/cozystack/cmd/cozypkg/cmd.Version=v$(COZYSTACK_VERSION)" -o _out/bin/cozypkg-$*/cozypkg$(EXT) ./cmd/cozypkg
	cp LICENSE _out/bin/cozypkg-$*/LICENSE
	tar -C _out/bin/cozypkg-$* -czf _out/assets/cozypkg-$*.tar.gz LICENSE cozypkg$(EXT)

test:
	make -C packages/core/testing apply
	make -C packages/core/testing e2e

unit-tests: helm-unit-tests bats-unit-tests bats-posix-compat-tests go-unit-tests rd-presets-check test-check-readiness migrations-target-check

helm-unit-tests:
	hack/helm-unit-tests.sh

# Pin the resourcesPreset enum in every ApplicationDefinition openAPISchema
# to the canonical 47-value set (40 instance-type names + 7 legacy aliases).
# Catches the regression where one chart's Makefile forgets to invoke
# hack/update-crd.sh and that RD's schema drifts from values.schema.json.
rd-presets-check:
	hack/check-rd-presets.sh

# Catch the off-by-one where migrations/<N> is added but
# migrations.targetVersion in packages/core/platform/values.yaml is not
# bumped to >= N+1. run-migrations.sh loops `seq $$CURRENT $$((TARGET-1))`,
# so the new migration is silently skipped on every cluster upgrade.
migrations-target-check:
	hack/check-migrations-target.sh

# Scoped go test over the cozystack-api surface that this repo owns. Kept
# narrow intentionally - running `go test ./...` pulls in generated code
# round-trip suites whose behavior depends on tool versions outside this
# repo's control (kubebuilder, openapi-gen, etc.) and is better exercised
# from their generator workflows.
go-unit-tests:
	go test ./pkg/registry/... ./pkg/config/... ./pkg/cmd/server/...

# Go tests for the controllers and supporting packages under ./internal.
# Excludes ./pkg/... and ./cmd/... — those are run separately by
# go-unit-tests above (pkg subset) and skipped (cmd) until their tests
# stabilise. Run as its own step in CI alongside helm/bats unit tests;
# locally invoke directly (`make test-controllers`) or chain
# (`make unit-tests test-controllers`).
test-controllers:
	go test ./internal/... -count=1

# Black-box golden test for cmd/check-readiness. Builds the binary and runs it
# against a mock kubectl (fixtures under test/check-readiness/testdata),
# diffing stdout/stderr/exit against golden files. Regenerate goldens with:
#   go test ./test/check-readiness/ -update
test-check-readiness:
	go test ./test/check-readiness/ -count=1

# Discover every hack/*.bats file that is NOT an e2e test and run it under
# bats(1). Drop a new *.bats file in hack/ and it is picked up automatically
# on the next `make unit-tests` run.
#
# These are hermetic unit tests of hack/*.sh, and they run under real bats
# rather than hack/cozytest.sh (#3453). The live-cluster suite
# (hack/e2e-*.bats) stays on cozytest, whose streaming trace and snapshot
# behaviour earn their place over a 15-minute test; bats buffers a test's
# output until it completes. Filtering by the e2e- prefix is what keeps the
# two runners apart.
#
# Caveat: $(wildcard ...) returns space-separated names, so a filename
# containing a literal space would split into multiple tokens here. All
# current bats files use hyphen-separated names; if the project ever
# introduces whitespace-bearing filenames this recipe must be rewritten
# (e.g. to use `find ... -print0 | xargs -0`).
BATS_UNIT_FILES := $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))

# The same list, one file per line, for hack/bats-strict-setup.bats. Every unit
# file has to load hack/test_helper.bash to get `set -u` back, and that audit is
# only worth anything if the set it walks is the set that actually runs -- so it
# asks here rather than keeping a second copy of the filter above.
print-bats-unit-files:
	@printf '%s\n' $(BATS_UNIT_FILES)

# `bats -j` needs GNU parallel and exits non-zero rather than degrading when it
# is missing. moreutils also ships a command named parallel, so identify the GNU
# implementation from its version output before enabling concurrency.
BATS_JOBS ?= $(shell parallel --version 2>/dev/null | grep -q '^GNU parallel ' && nproc 2>/dev/null || echo 1)

print-bats-jobs:
	@printf '%s\n' "$(BATS_JOBS)"

# JUnit XML is preserved as a CI artifact for inspection. _out is gitignored.
BATS_REPORT_DIR ?= _out/test-reports

bats-unit-tests:
	@if [ -z "$(BATS_UNIT_FILES)" ]; then \
		echo "ERROR: no hack/*.bats unit test files found"; \
		exit 1; \
	fi
	@command -v bats >/dev/null 2>&1 || { \
		echo "ERROR: bats not found. Install bats-core >= 1.5 — https://bats-core.readthedocs.io"; \
		exit 1; \
	}
	@mkdir -p "$(BATS_REPORT_DIR)"
	bats -j $(BATS_JOBS) --report-formatter junit -o "$(BATS_REPORT_DIR)" $(BATS_UNIT_FILES)

# Real Bats is authoritative. These files also source production code whose
# contract is POSIX sh, so retain a narrow pass through cozytest.sh's /bin/sh
# translator. This catches a Bash-only regression without running the entire
# unit suite twice.
BATS_POSIX_COMPAT_FILES := \
	hack/nightly-mirror_test.bats \
	hack/cozystack-version-stamp.bats \
	hack/pod-label-census_test.bats \
	hack/seaweedfs-naming-audit.bats \
	hack/runner-identity.bats

bats-posix-compat-tests:
	@for f in $(BATS_POSIX_COMPAT_FILES); do \
		echo "--- running POSIX compatibility: $$f ---"; \
		hack/cozytest.sh "$$f" || exit 1; \
	done

# Operator-facing host preflight check. Warns about a standalone
# containerd.service or docker.service running alongside the embedded
# k3s runtime. Safe to run at any time; always exits 0.
preflight:
	@hack/check-host-runtime.sh

prepare-env:
	make -C packages/core/testing apply
	make -C packages/core/testing prepare-cluster

generate:
	hack/update-codegen.sh

upload_assets: manifests
	hack/upload-assets.sh
