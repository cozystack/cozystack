.PHONY: manifests assets unit-tests helm-unit-tests bats-unit-tests preflight

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
	make -C packages/system/monitoring image
	make -C packages/system/cozystack-api image
	make -C packages/system/cozystack-controller image
	make -C packages/system/backup-controller image
	make -C packages/system/backupstrategy-controller image
	make -C packages/system/lineage-controller-webhook image
	make -C packages/system/cilium image
	make -C packages/system/linstor image
	make -C packages/system/kubeovn-webhook image
	make -C packages/system/kubeovn-plunger image
	make -C packages/system/dashboard image
	make -C packages/system/metallb image
	make -C packages/system/kamaji image
	make -C packages/system/multus image
	make -C packages/system/bucket image
	make -C packages/system/objectstorage-controller image
	make -C packages/system/grafana-operator image
	make -C packages/core/testing image
	make -C packages/core/talos image
	make -C packages/core/platform image
	make -C packages/core/installer image
	make manifests

manifests:
	mkdir -p _out/assets
	cat internal/crdinstall/manifests/*.yaml > _out/assets/cozystack-crds.yaml
	# Talos variant (default)
	helm template installer packages/core/installer -n cozy-system \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-talos.yaml
	# Generic Kubernetes variant (k3s, kubeadm, RKE2)
	helm template installer packages/core/installer -n cozy-system \
		--set cozystackOperator.variant=generic \
		--set cozystack.apiServerHost=REPLACE_ME \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-generic.yaml
	# Hosted variant (managed Kubernetes)
	helm template installer packages/core/installer -n cozy-system \
		--set cozystackOperator.variant=hosted \
		--show-only templates/cozystack-operator.yaml \
		> _out/assets/cozystack-operator-hosted.yaml

cozypkg:
	go build -ldflags "-X github.com/cozystack/cozystack/cmd/cozypkg/cmd.Version=v$(COZYSTACK_VERSION)" -o _out/bin/cozypkg ./cmd/cozypkg

assets: assets-talos assets-cozypkg openapi-json

openapi-json:
	mkdir -p _out/assets
	VERSION=$(shell git describe --tags --always 2>/dev/null || echo dev) go run ./tools/openapi-gen/ 2>/dev/null > _out/assets/openapi.json

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
	make -C packages/core/testing test

unit-tests: helm-unit-tests bats-unit-tests go-unit-tests

helm-unit-tests:
	hack/helm-unit-tests.sh

# Scoped go test over the cozystack-api surface that this repo owns. Kept
# narrow intentionally - running `go test ./...` pulls in generated code
# round-trip suites whose behavior depends on tool versions outside this
# repo's control (kubebuilder, openapi-gen, etc.) and is better exercised
# from their generator workflows.
go-unit-tests:
	go test ./pkg/registry/... ./pkg/config/... ./pkg/cmd/server/...

# Discover every hack/*.bats file that is NOT an e2e test and run it
# through cozytest.sh. Drop a new *.bats file in hack/ and it is picked
# up automatically on the next `make unit-tests` run.
#
# Caveat: $(wildcard ...) returns space-separated names, so a filename
# containing a literal space would split into multiple tokens here. All
# current bats files use hyphen-separated names; if the project ever
# introduces whitespace-bearing filenames this recipe must be rewritten
# (e.g. to use `find ... -print0 | xargs -0`).
BATS_UNIT_FILES := $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))

bats-unit-tests:
	@if [ -z "$(BATS_UNIT_FILES)" ]; then \
		echo "ERROR: no hack/*.bats unit test files found"; \
		exit 1; \
	fi
	@for f in $(BATS_UNIT_FILES); do \
		echo "--- running $$f ---"; \
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
