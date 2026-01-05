export NAME=seaweedfs-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	mkdir -p charts
	version=$$(git ls-remote --tags --sort="v:refname" https://github.com/seaweedfs/seaweedfs | grep -v '\^{}' | grep 'refs/tags/[0-9]' | awk -F'/' 'END{print $$3}') && \
	curl -sSL https://github.com/seaweedfs/seaweedfs/archive/refs/tags/$${version}.tar.gz | \
	tar xzvf - --strip 3 -C charts seaweedfs-$${version}/k8s/charts/seaweedfs
	patch --no-backup-if-mismatch -p4 < patches/resize-api-server-annotation.diff
	patch --no-backup-if-mismatch -p4 < patches/s3-traffic-distribution.patch
	#patch --no-backup-if-mismatch -p4 < patches/retention-policy-delete.yaml

