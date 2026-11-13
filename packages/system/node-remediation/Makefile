export NAME=node-remediation
export NAMESPACE=cozy-node-remediation

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

FAR_COMMIT=$(shell grep -F 'ARG COMMIT_REF=' images/fence-agents-remediation/Containerfile | cut -f2 -d=)
FAR_VERSION=$(shell grep -F 'ARG UPSTREAM_VERSION=' images/fence-agents-remediation/Containerfile | cut -f2 -d=)
NHC_VERSION=$(shell grep -F 'ARG VERSION=' images/node-healthcheck-operator/Containerfile | cut -f2 -d=)
UPSTREAM=_out/upstream

.PHONY: test update image image-fence-agents-remediation image-node-healthcheck-operator

test:
	helm unittest .

# Renders both operators from the pinned upstream trees with the same patches
# the images apply, through the kustomize overlays under kustomize/, which sit
# next to upstream's config/default. Upstream has no chart and installs through
# OLM; the pieces OLM would provide (the namespace, the webhook certificates,
# the safety-mode grants) are this chart's own templates. The CRDs go to
# ../node-remediation-crds, as cert-manager refills cert-manager-crds.
# Inside this checkout git apply would resolve the patch paths against the
# repository root and skip them all; with GIT_DIR pointing nowhere it is the
# plain patch tool it is in the image builds.
update:
	rm -rf $(UPSTREAM)
	mkdir -p $(UPSTREAM)/far $(UPSTREAM)/nhc
	curl -sSL https://github.com/medik8s/fence-agents-remediation/archive/$(FAR_COMMIT).tar.gz | tar -xz --strip-components=1 -C $(UPSTREAM)/far
	curl -sSL https://github.com/medik8s/node-healthcheck-operator/archive/refs/tags/$(NHC_VERSION).tar.gz | tar -xz --strip-components=1 -C $(UPSTREAM)/nhc
	cd $(UPSTREAM)/far && GIT_DIR=/nonexistent git apply $(CURDIR)/images/fence-agents-remediation/patches/*.diff
	cd $(UPSTREAM)/nhc && GIT_DIR=/nonexistent git apply $(CURDIR)/images/node-healthcheck-operator/patches/*.diff
	cp -R kustomize/far $(UPSTREAM)/far/config/cozystack
	cp $(UPSTREAM)/far/config/rbac/safety/cluster_role.yaml $(UPSTREAM)/far/config/cozystack/safety-clusterrole.yaml
	cp -R kustomize/nhc $(UPSTREAM)/nhc/config/cozystack
	# Dropped from the fence-agents output: the CRDs (shipped by the crds chart),
	# the Namespace (created by the PackageSource), the unused manager ConfigMap,
	# and the default manager grants together with their bindings, which the
	# safety-mode roles replace. Everything else keeps its upstream shape.
	kubectl kustomize $(UPSTREAM)/far/config/cozystack \
	  | yq 'select(.kind != "CustomResourceDefinition" and .kind != "Namespace" and .kind != "ConfigMap" and (.metadata.name | test("^fence-agents-remediation-manager-role") | not)) | with(select(.kind == "Deployment"); .spec.template.spec.containers[0].image = "{{ .Values.fenceAgentsRemediation.image }}")' \
	  | sed -e 's/^\([[:space:]]*\)namespace: cozy-node-remediation$$/\1namespace: {{ .Release.Namespace }}/' \
	        -e 's#: cozy-node-remediation/#: {{ .Release.Namespace }}/#' \
	        -e 's/serviceaccount:cozy-node-remediation:/serviceaccount:{{ .Release.Namespace }}:/' \
	  > templates/fence-agents-remediation.yaml
	kubectl kustomize $(UPSTREAM)/nhc/config/cozystack \
	  | yq 'select(.kind != "CustomResourceDefinition" and .kind != "Namespace") | with(select(.kind == "Deployment"); .spec.template.spec.containers[0].image = "{{ .Values.nodeHealthcheck.image }}")' \
	  | sed -e 's/^\([[:space:]]*\)namespace: cozy-node-remediation$$/\1namespace: {{ .Release.Namespace }}/' \
	        -e 's#: cozy-node-remediation/#: {{ .Release.Namespace }}/#' \
	  > templates/node-healthcheck-operator.yaml
	rm -f ../node-remediation-crds/templates/crd-*.yaml
	for f in $(UPSTREAM)/far/config/crd/bases/*.yaml $(UPSTREAM)/nhc/config/crd/bases/*.yaml; do \
	  yq '.metadata.annotations["helm.sh/resource-policy"] = "keep"' "$$f" > ../node-remediation-crds/templates/crd-$$(basename "$$f"); \
	done
	rm -rf $(UPSTREAM)

image: image-fence-agents-remediation image-node-healthcheck-operator

image-fence-agents-remediation:
	docker buildx build images/fence-agents-remediation --file images/fence-agents-remediation/Containerfile \
		$(call image-tags,fence-agents-remediation,$(FAR_VERSION)-cozystack.0) \
		$(call cache-args,fence-agents-remediation) \
		--metadata-file images/fence-agents-remediation.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/fence-agents-remediation:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/fence-agents-remediation.json -o json -r)" \
		yq -i '.fenceAgentsRemediation.image = strenv(IMAGE)' values.yaml
	rm -f images/fence-agents-remediation.json

image-node-healthcheck-operator:
	docker buildx build images/node-healthcheck-operator --file images/node-healthcheck-operator/Containerfile \
		$(call image-tags,node-healthcheck-operator,$(NHC_VERSION)-cozystack.0) \
		$(call cache-args,node-healthcheck-operator) \
		--metadata-file images/node-healthcheck-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/node-healthcheck-operator:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/node-healthcheck-operator.json -o json -r)" \
		yq -i '.nodeHealthcheck.image = strenv(IMAGE)' values.yaml
	rm -f images/node-healthcheck-operator.json
