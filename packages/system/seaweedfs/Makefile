export NAME=seaweedfs-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	mkdir -p charts
	version=$$(git ls-remote --tags --sort="v:refname" https://github.com/seaweedfs/seaweedfs | grep -v '\^{}' | grep 'refs/tags/[0-9]' | awk -F'/' 'END{print $$3}') && \
	curl -sSL https://github.com/seaweedfs/seaweedfs/archive/refs/tags/$${version}.tar.gz | \
	tar xzvf - --strip 3 -C charts seaweedfs-$${version}/k8s/charts/seaweedfs
	patch --no-backup-if-mismatch -p4 < patches/resize-api-server-annotation.diff
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-tls-main-port.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-provisioner-sa-name.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-bucket-class-lock-readonly.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-service-name.patch
	#patch --no-backup-if-mismatch -p4 < patches/retention-policy-delete.yaml

