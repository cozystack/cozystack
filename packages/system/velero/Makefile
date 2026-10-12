KUBEVIRT_VELERO_PLUGIN_TAG=$(shell grep -F 'ARG VERSION=' images/kubevirt-velero-plugin/Dockerfile | cut -f2 -d=)
export NAME=velero
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/kubevirt-velero-plugin \
		$(call image-tags,kubevirt-velero-plugin,$(KUBEVIRT_VELERO_PLUGIN_TAG)) \
		$(call cache-args,kubevirt-velero-plugin) \
		--metadata-file images/kubevirt-velero-plugin.json \
		$(BUILDX_ARGS)
	# sed rather than yq -i: yq re-indents the initContainers list and drops
	# blank lines elsewhere in the file, so every stamp would reformat it.
	IMAGE="$(REGISTRY)/kubevirt-velero-plugin:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/kubevirt-velero-plugin.json -o json -r)" && \
		sed -i "s|image: [^ ]*/kubevirt-velero-plugin:[^ ]*$$|image: $$IMAGE|" values.yaml && \
		grep -qF "image: $$IMAGE" values.yaml
	rm -f images/kubevirt-velero-plugin.json

test:
	helm unittest .

update:
	rm -rf charts
	# Velero
	helm repo add tanzu https://vmware-tanzu.github.io/helm-charts
	helm repo update tanzu
	helm pull tanzu/velero --untar --untardir charts --version 12.0.3
	# Render a startupProbe from .Values.startupProbe (upstream chart omits it),
	# so a slow server start under heavy parallel install does not trip liveness.
	patch --no-backup-if-mismatch -p4 < patches/add-startup-probe.patch
