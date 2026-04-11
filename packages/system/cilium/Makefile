CILIUM_TAG=$(shell awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml)

export NAME=cilium
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add cilium https://helm.cilium.io/
	helm repo update cilium
	helm pull cilium/cilium --untar --untardir charts --version 1.19
	$(SED_INPLACE) -e '/Used in iptables/d' -e '/SYS_MODULE/d' charts/cilium/values.yaml
	# Drop the whole k8s<1.30 AppArmor annotations block from the cilium-agent
	# DaemonSet. Cozystack manages these annotations through cilium.podAnnotations
	# in values.yaml on every k8s version, so keeping the upstream block would
	# produce duplicate mapping keys on k8s<1.30.
	perl -i -0pe 's|\n        \{\{- if not \.Values\.securityContext\.privileged \}\}\n        \{\{- if semverCompare "<1\.30\.0".*?\n        \{\{- end \}\}\n        \{\{- end \}\}\n        \{\{- end \}\}||s' \
		charts/cilium/templates/cilium-agent/daemonset.yaml
	@! grep -q 'container\.apparmor\.security\.beta\.kubernetes\.io/cilium-agent: "unconfined"' \
		charts/cilium/templates/cilium-agent/daemonset.yaml || \
		{ echo 'ERROR: perl patch did not remove the upstream AppArmor block from cilium-agent/daemonset.yaml (upstream template format may have changed)' >&2; exit 1; }
	version=$$(awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml) && \
	$(SED_INPLACE) "s/ARG VERSION=.*/ARG VERSION=v$${version}/" images/cilium/Dockerfile

image:
	docker buildx build images/cilium \
		--tag $(REGISTRY)/cilium:$(call settag,$(CILIUM_TAG)) \
		--tag $(REGISTRY)/cilium:$(call settag,$(CILIUM_TAG)-$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cilium:latest \
		--cache-to type=inline \
		--metadata-file images/cilium.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/cilium" \
		yq -i '.cilium.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(call settag,$(CILIUM_TAG)) \
		yq -i '.cilium.image.tag = strenv(TAG)' values.yaml
	DIGEST=$$(yq e '."containerimage.digest"' images/cilium.json -o json -r) \
		yq -i '.cilium.image.digest = strenv(DIGEST)' values.yaml
	rm -f images/cilium.json
