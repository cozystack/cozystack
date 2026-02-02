.PHONY: manifests assets unit-tests helm-unit-tests

include hack/common-envs.mk

build-deps:
	@command -V find docker skopeo jq gh helm > /dev/null
	@yq --version | grep -q "mikefarah" || (echo "mikefarah/yq is required" && exit 1)
	@tar --version | grep -q GNU || (echo "GNU tar is required" && exit 1)
	@sed --version | grep -q GNU || (echo "GNU sed is required" && exit 1)
	@awk --version | grep -q GNU || (echo "GNU awk is required" && exit 1)

build: build-deps
	make -C packages/apps/http-cache image
	make -C packages/apps/mysql image
	make -C packages/apps/clickhouse image
	make -C packages/apps/kubernetes image
	make -C packages/extra/monitoring image
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
	make -C packages/system/kilo image
	make -C packages/system/bucket image
	make -C packages/system/objectstorage-controller image
	make -C packages/system/grafana-operator image
	make -C packages/core/testing image
	make -C packages/core/talos image
	make -C packages/core/installer image
	make manifests

manifests:
	mkdir -p _out/assets
	helm template installer packages/core/installer -n cozy-system \
		-s templates/crds.yaml \
		> _out/assets/cozystack-crds.yaml
	# Talos variant (default)
	helm template installer packages/core/installer -n cozy-system \
		-s templates/cozystack-operator.yaml \
		-s templates/packagesource.yaml \
		> _out/assets/cozystack-operator.yaml
	# Generic Kubernetes variant (k3s, kubeadm, RKE2)
	helm template installer packages/core/installer -n cozy-system \
		-s templates/cozystack-operator-generic.yaml \
		-s templates/packagesource.yaml \
		> _out/assets/cozystack-operator-generic.yaml
	# Hosted variant (managed Kubernetes)
	helm template installer packages/core/installer -n cozy-system \
		-s templates/cozystack-operator-hosted.yaml \
		-s templates/packagesource.yaml \
		> _out/assets/cozystack-operator-hosted.yaml

cozypkg:
	go build -ldflags "-X github.com/cozystack/cozystack/cmd/cozypkg/cmd.Version=v$(COZYSTACK_VERSION)" -o _out/bin/cozypkg ./cmd/cozypkg

assets: assets-talos assets-cozypkg

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

unit-tests: helm-unit-tests

helm-unit-tests:
	hack/helm-unit-tests.sh

prepare-env:
	make -C packages/core/testing apply
	make -C packages/core/testing prepare-cluster

generate:
	hack/update-codegen.sh

upload_assets: manifests
	hack/upload-assets.sh
