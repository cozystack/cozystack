export NAME=vsnap-crd
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/package.mk

update:
	rm -rf templates
	mkdir templates
	wget -O ./templates/volumesnapshotclasses.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
	wget -O ./templates/volumesnapshotcontents.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
	wget -O ./templates/volumesnapshots.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
