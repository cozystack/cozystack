export NAME=vsnap-crd
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

# Stamp helm.sh/resource-policy: keep on every CustomResourceDefinition so a
# chart uninstall never garbage-collects the CRDs and, with them, every
# VolumeSnapshot and VolumeSnapshotContent in the cluster — the objects that
# hold the only reference to a snapshot on the storage backend.
# The stamp goes into the CRD's existing metadata.annotations block; a CRD that
# arrives without one is skipped, and the chart's helm-unittest suite is what
# catches that.
update:
	rm -rf templates
	mkdir templates
	wget -O ./templates/volumesnapshotclasses.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
	wget -O ./templates/volumesnapshotcontents.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
	wget -O ./templates/volumesnapshots.yaml https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/refs/tags/v8.3.0/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
	@set -e; tmp=$$(mktemp); for f in ./templates/*.yaml; do \
	  awk '/^    helm.sh\/resource-policy: keep$$/{next} /^kind: /{kind=$$2} /^  annotations:$$/ && kind=="CustomResourceDefinition"{print; print "    helm.sh/resource-policy: keep"; next} {print}' "$$f" > "$$tmp"; \
	  cat "$$tmp" > "$$f"; \
	done; rm -f "$$tmp"
