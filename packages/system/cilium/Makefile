CILIUM_TAG=$(shell awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml)

export NAME=cilium
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# hack/helm-unit-tests.sh discovers work by probing for this target, so a
# package without one is skipped in silence however many suites sit under
# tests/.
#
# Phony because hack/package.mk's .PHONY line deliberately does not carry
# `test` -- it lists only the targets that file defines, and hack/package-mk-phony.bats
# pins that -- so a path named `test` next to this Makefile would otherwise make
# the target up to date and skip the recipe. That matters here beyond tidiness:
# tests/kube_proxy_replacement_test.yaml is the pin two other packages' deny
# policies rely on, and a skipped recipe removes it in silence.
.PHONY: test
test:
	helm unittest .

update:
	rm -rf charts
	helm repo add cilium https://helm.cilium.io/
	helm repo update cilium
	helm pull cilium/cilium --untar --untardir charts --version 1.20
	$(SED_INPLACE) -e '/Used in iptables/d' -e '/SYS_MODULE/d' charts/cilium/values.yaml
	# Drop the whole k8s<1.30 AppArmor annotations block from the cilium-agent
	# DaemonSet. Cozystack manages these annotations through cilium.podAnnotations
	# in values.yaml on every k8s version, so keeping the upstream block would
	# produce duplicate mapping keys on k8s<1.30.
	perl -i -0pe 's|\n        \{\{- if not \.Values\.securityContext\.privileged \}\}\n        \{\{- if semverCompare "<1\.30\.0".*?\n        \{\{- end \}\}\n        \{\{- end \}\}\n        \{\{- end \}\}||s' \
		charts/cilium/templates/cilium-agent/daemonset.yaml
	@! grep -q 'container\.apparmor\.security\.beta\.kubernetes\.io' \
		charts/cilium/templates/cilium-agent/daemonset.yaml || \
		{ echo 'ERROR: perl patch did not remove the upstream AppArmor block from cilium-agent/daemonset.yaml (upstream template format may have changed)' >&2; exit 1; }
	# Both images are pinned by the digest the vendored chart carries for the
	# upstream image they start from; yq prints "null" for a missing key, which
	# would otherwise be written into the Dockerfile.
	version=$$(awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml) && \
	agent=$$(yq e '.image.digest' charts/cilium/values.yaml) && \
	operator=$$(yq e '.operator.image.genericDigest' charts/cilium/values.yaml) && \
	case "$$agent$$operator" in *null*|"") echo "ERROR: charts/cilium/values.yaml lacks .image.digest or .operator.image.genericDigest" >&2; exit 1;; esac && \
	$(SED_INPLACE) "s/ARG VERSION=.*/ARG VERSION=v$${version}@$${agent}/" images/cilium/Dockerfile && \
	$(SED_INPLACE) -e "s/ARG VERSION=.*/ARG VERSION=v$${version}/" -e "s/ARG UPSTREAM_DIGEST=.*/ARG UPSTREAM_DIGEST=$${operator}/" images/cilium-operator/Dockerfile
	# The operator patches are Go and touch several files, so they are checked
	# against the whole source tree of the new tag, and the operator builder
	# takes the Go toolchain of upstream's builder image, which compiles the
	# upstream operator.
	version=$$(awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml) && \
	tmpdir=$$(mktemp -d) && \
	{ curl --fail --silent --show-error --location -o "$$tmpdir/source.tar.gz" \
		"https://github.com/cilium/cilium/archive/refs/tags/v$${version}.tar.gz" && \
		mkdir "$$tmpdir/src" && tar -xzf "$$tmpdir/source.tar.gz" --strip-components=1 -C "$$tmpdir/src" || { rm -rf "$$tmpdir"; exit 1; }; } && \
	source=$$(sha256sum "$$tmpdir/source.tar.gz" | cut -d' ' -f1) && \
	$(SED_INPLACE) "s/ARG SOURCE_SHA256=.*/ARG SOURCE_SHA256=$${source}/" images/cilium-operator/Dockerfile && \
	{ git -C "$$tmpdir/src" apply --check $(CURDIR)/images/cilium-operator/patches/*.diff || \
		{ echo "ERROR: images/cilium-operator/patches/*.diff no longer apply to cilium v$${version}; regenerate them, or drop the ones the new tag already carries" >&2; \
		rm -rf "$$tmpdir"; exit 1; }; } && \
	golang=$$(sed -n 's/^ARG GOLANG_IMAGE=//p' "$$tmpdir/src/images/builder/Dockerfile") && \
	{ [ -n "$$golang" ] || { echo "ERROR: no GOLANG_IMAGE in upstream images/builder/Dockerfile v$${version}" >&2; rm -rf "$$tmpdir"; exit 1; }; } && \
	$(SED_INPLACE) "s|^FROM --platform=\$$BUILDPLATFORM [^ ]* AS builder$$|FROM --platform=\$$BUILDPLATFORM $${golang} AS builder|" images/cilium-operator/Dockerfile && \
	{ grep -qF "FROM --platform=\$$BUILDPLATFORM $${golang} AS builder" images/cilium-operator/Dockerfile || \
		{ echo "ERROR: the builder FROM line in images/cilium-operator/Dockerfile was not re-pinned; its shape no longer matches the update recipe" >&2; rm -rf "$$tmpdir"; exit 1; }; } && \
	rm -rf "$$tmpdir"
	# Verify the BPF patches still apply to the new upstream tag, so the bump
	# author learns about drift immediately instead of at image build.
	version=$$(awk '$$1 == "version:" {print $$2}' charts/cilium/Chart.yaml) && \
	tmpdir=$$(mktemp -d) && \
	{ curl --fail --silent --show-error --location \
		"https://raw.githubusercontent.com/cilium/cilium/v$${version}/bpf/bpf_host.c" \
		-o "$$tmpdir/bpf_host.c" || { rm -rf "$$tmpdir"; exit 1; }; } && \
	{ patch --dry-run --batch --forward --fuzz=0 -p1 -d "$$tmpdir" \
		< images/cilium/patches/pass-ipv6-to-stack-when-ipv6-datapath-disabled.diff || \
		{ echo "ERROR: images/cilium/patches/pass-ipv6-to-stack-when-ipv6-datapath-disabled.diff no longer applies to upstream bpf/bpf_host.c v$${version}; regenerate it for the new cilium version" >&2; \
		rm -rf "$$tmpdir"; exit 1; }; } && \
	mkdir -p "$$tmpdir/lib" && \
	{ curl --fail --silent --show-error --location \
		"https://raw.githubusercontent.com/cilium/cilium/v$${version}/bpf/lib/nodeport_egress.h" \
		-o "$$tmpdir/lib/nodeport_egress.h" || { rm -rf "$$tmpdir"; exit 1; }; } && \
	{ patch --dry-run --batch --forward --fuzz=0 -p1 -d "$$tmpdir" \
		< images/cilium/patches/skip-snat-conflict-for-local-destinations.diff || \
		{ echo "ERROR: images/cilium/patches/skip-snat-conflict-for-local-destinations.diff no longer applies to upstream bpf/lib/nodeport_egress.h v$${version}; regenerate it for the new cilium version" >&2; \
		rm -rf "$$tmpdir"; exit 1; }; } && \
	rm -rf "$$tmpdir"

.PHONY: image image-cilium image-operator
image: image-cilium image-operator

image-cilium:
	docker buildx build images/cilium \
		$(call image-tags,cilium,$(CILIUM_TAG)) \
		$(call cache-args,cilium) \
		--metadata-file images/cilium.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/cilium" \
		yq -i '.cilium.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG) \
		yq -i '.cilium.image.tag = strenv(TAG)' values.yaml
	DIGEST=$$(yq e '."containerimage.digest"' images/cilium.json -o json -r) \
		yq -i '.cilium.image.digest = strenv(DIGEST)' values.yaml
	rm -f images/cilium.json

# After image-cilium: both stamp values.yaml with yq -i, and two concurrent
# writes under `make -j image` can drop one of the stamps.
image-operator: image-cilium
	docker buildx build images/cilium-operator \
		$(call image-tags,cilium-operator,$(CILIUM_TAG)) \
		$(call cache-args,cilium-operator) \
		--metadata-file images/cilium-operator.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/cilium-operator:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/cilium-operator.json -o json -r)" \
		yq -i '.cilium.operator.image.override = strenv(IMAGE)' values.yaml
	rm -f images/cilium-operator.json
