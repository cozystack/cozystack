export NAME=seaweedfs-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	mkdir -p charts
	version=$$(git ls-remote --tags --sort="v:refname" https://github.com/seaweedfs/seaweedfs | grep -v '\^{}' | grep 'refs/tags/[0-9]' | awk -F'/' 'END{print $$3}') && \
	curl -sSL https://github.com/seaweedfs/seaweedfs/archive/refs/tags/$${version}.tar.gz | \
	tar xzvf - --strip 3 -C charts seaweedfs-$${version}/k8s/charts/seaweedfs && \
	sed -i.bak "/ARG VERSION/ s|=.*|=$${version}|g" images/seaweedfs/Dockerfile && \
	rm -f images/seaweedfs/Dockerfile.bak
	patch --no-backup-if-mismatch -p4 < patches/resize-api-server-annotation.diff
	patch --no-backup-if-mismatch -p4 < patches/long-term-ca.diff
	#patch --no-backup-if-mismatch -p4 < patches/retention-policy-delete.yaml

image:
	docker buildx build images/seaweedfs \
		--tag $(REGISTRY)/seaweedfs:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/seaweedfs:latest \
		--cache-to type=inline \
		--metadata-file images/seaweedfs.json \
		$(BUILDX_ARGS)
	REGISTRY="$(REGISTRY)" \
		yq -i '.seaweedfs.image.registry = strenv(REGISTRY)' values.yaml
	TAG=$(TAG)@$$(yq e '."containerimage.digest"' images/seaweedfs.json -o json -r) \
		yq -i '.seaweedfs.image.tag = strenv(TAG)' values.yaml
	yq -i '.global.imageName = "seaweedfs"' values.yaml
	rm -f images/seaweedfs.json
