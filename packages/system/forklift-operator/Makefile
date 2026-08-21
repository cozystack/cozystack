export NAME=forklift-operator
export NAMESPACE=cozy-forklift

include ../../../hack/package.mk

test:
	helm unittest .

# Forklift release. Upstream tags container images as `release-2.11`,
# not `v2.11.x`, so we use that channel here. Bump VERSION to ride
# patch releases; bumping FORKLIFT_CHANNEL (e.g. release-2.12) is a
# breaking change that requires re-checking CRDs against vm-import.
#
# The image refs substituted below are pinned to immutable @sha256 digests
# so pulls are reproducible and mirrored / air-gapped installs can resolve
# them (mirror the digests, not the floating tag). Renovate keeps the
# digests fresh via a custom manager over this Makefile and the rendered
# templates/forklift-operator.yaml (see .github/renovate.json). must-gather
# and console-plugin are versioned independently of the operator channel
# upstream (no release-2.11 tag exists for them), so they carry their own
# tags (v2.7.1 / release-v2.6.7); re-resolve when bumping FORKLIFT_CHANNEL.
FORKLIFT_VERSION=v2.11.5
FORKLIFT_CHANNEL=release-2.11

update:
	rm -rf templates
	mkdir templates
	git clone --depth 1 --branch $(FORKLIFT_VERSION) https://github.com/kubev2v/forklift.git /tmp/forklift-$(FORKLIFT_VERSION)
	kubectl kustomize /tmp/forklift-$(FORKLIFT_VERSION)/operator/config/default/ > templates/forklift-operator.yaml
	rm -rf /tmp/forklift-$(FORKLIFT_VERSION)
	# Forklift kustomize emits ${VAR} placeholders for images and tunables;
	# the upstream OLM bundle substitutes them at install time. We don't use
	# OLM, so we substitute them statically against the kubev2v registry.
	sed -i 's|namespace: forklift-operator-system|namespace: $(NAMESPACE)|g' templates/forklift-operator.yaml
	sed -i 's|namespace: konveyor-forklift|namespace: $(NAMESPACE)|g' templates/forklift-operator.yaml
	# Cozystack doesn't use OLM, so WATCH_NAMESPACE (sourced from the
	# olm.targetNamespaces annotation) resolves empty and the Ansible role
	# defaults app_namespace to `konveyor-forklift`, deploying the controller
	# outside our RBAC scope. Pin it to our namespace.
	python3 -c "import re; p='templates/forklift-operator.yaml'; c=open(p).read(); c=re.sub(r'- name: WATCH_NAMESPACE\n\s*valueFrom:\n\s*fieldRef:\n\s*fieldPath: metadata.annotations\[.olm.targetNamespaces.\]', '- name: WATCH_NAMESPACE\n          value: $(NAMESPACE)', c); open(p,'w').write(c)"
	sed -i 's|$${OPERATOR_IMAGE}|quay.io/kubev2v/forklift-operator:release-2.11@sha256:6d91046e8e8d50869db67699779c5ff76d0efddeecafdd26c1de8a819e67afe4|g' templates/forklift-operator.yaml
	sed -i 's|$${CONTROLLER_IMAGE}|quay.io/kubev2v/forklift-controller:release-2.11@sha256:abad06ff5110ee23502f86d0a8d6ed53348a7a03da53956b94ba0ea146be2de3|g' templates/forklift-operator.yaml
	sed -i 's|$${API_IMAGE}|quay.io/kubev2v/forklift-api:release-2.11@sha256:64efe1504f51046df89d49e8d6321893afd19be9c34d1afbc681f324bff3920d|g' templates/forklift-operator.yaml
	sed -i 's|$${MUST_GATHER_IMAGE}|quay.io/kubev2v/forklift-must-gather:v2.7.1@sha256:62eecec1408be00e907fbde6b13be9223fdbca116933ecba85cd87d68d6acfbb|g' templates/forklift-operator.yaml
	sed -i 's|$${UI_PLUGIN_IMAGE}|quay.io/kubev2v/forklift-console-plugin:release-v2.6.7@sha256:99c34ac714604853f4abf589e1ccddb13cd9eaf1bd1eae2c928c622deb22304d|g' templates/forklift-operator.yaml
	sed -i 's|$${CLI_DOWNLOAD_IMAGE}|quay.io/kubev2v/forklift-cli-download:release-2.11@sha256:91fec2c426773196b87632b8f27bf7482c664be5689d03e9dc324233f0a060a5|g' templates/forklift-operator.yaml
	sed -i 's|$${VALIDATION_IMAGE}|quay.io/kubev2v/forklift-validation:release-2.11@sha256:a1d0c310db0341fc6608c2b2b955b7699327355c6506cbea5c1a2aabd5e96a2c|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE}|quay.io/kubev2v/forklift-virt-v2v:release-2.11@sha256:e5280a54920c4eed86a85ab9b5d255c9ba008c7030c8334ff3c21dd5cbf24075|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE_RHEL9}|quay.io/kubev2v/forklift-virt-v2v-xfs:release-2.11@sha256:2b9140edaca17e9c5c414a9c42ef422096186ebbf032dd378ed9d93b967550b9|g' templates/forklift-operator.yaml
	sed -i 's|$${POPULATOR_CONTROLLER_IMAGE}|quay.io/kubev2v/populator-controller:release-2.11@sha256:d42c7f97b7dea571c0fc111b628a59c5ec45a66225fec11052730a35b7c3cb43|g' templates/forklift-operator.yaml
	sed -i 's|$${OVIRT_POPULATOR_IMAGE}|quay.io/kubev2v/ovirt-populator:release-2.11@sha256:4cf3f458c5975c36f4cdf87daf3460a8cc37080487b88bb652f37d9445c11df4|g' templates/forklift-operator.yaml
	sed -i 's|$${OPENSTACK_POPULATOR_IMAGE}|quay.io/kubev2v/openstack-populator:release-2.11@sha256:00a0c95aed4e3114b2f60e33d9f9ae5ea7ff19f823be194dae4dd623ffe39ca8|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-ova-provider-server:release-2.11@sha256:7254926ed5fcb4fe90868672fb2997ffe426c2968466c450172e3c3372805b5b|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROXY_IMAGE}|quay.io/kubev2v/forklift-ova-proxy:release-2.11@sha256:1b307c9283c4b328f74381b0b89c138f4475bb2d31ba3a88d3e0b1d5b276a27e|g' templates/forklift-operator.yaml
	sed -i 's|$${VSPHERE_XCOPY_VOLUME_POPULATOR_IMAGE}|quay.io/kubev2v/vsphere-xcopy-volume-populator:release-2.11@sha256:b9008754d42c314ea3aa6362a397a7c0a93fae88444faa3234c08c75b8902f93|g' templates/forklift-operator.yaml
	sed -i 's|$${HYPERV_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-hyperv-provider-server:release-2.11@sha256:4c7b9df841f0f29320763ff2586ff22be4ed2e509bf5148583ef3bad5936469c|g' templates/forklift-operator.yaml
	# Tunable env vars (defaults from upstream Ansible role).
	sed -i 's|$${VIRT_V2V_DONT_REQUEST_KVM}|"false"|g' templates/forklift-operator.yaml
	sed -i 's|$${SNAPSHOT_REMOVAL_TIMEOUT}|"120"|g' templates/forklift-operator.yaml
	sed -i 's|$${SNAPSHOT_STATUS_CHECK_RATE}|"10"|g' templates/forklift-operator.yaml
	sed -i 's|$${CLEANUP_RETRIES}|"10"|g' templates/forklift-operator.yaml
	sed -i 's|$${CDI_EXPORT_TOKEN_TTL}|""|g' templates/forklift-operator.yaml
	sed -i 's|$${FILESYSTEM_OVERHEAD}|"0.055"|g' templates/forklift-operator.yaml
	sed -i 's|$${BLOCK_OVERHEAD}|"0"|g' templates/forklift-operator.yaml
	sed -i 's|$${OVIRT_OS_MAP}|""|g' templates/forklift-operator.yaml
	sed -i 's|$${VSPHERE_OS_MAP}|""|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_CUSTOMIZE_MAP}|""|g' templates/forklift-operator.yaml
	# Drop residual Namespace resources — those are provisioned by Cozystack.
	python3 -c "content=open('templates/forklift-operator.yaml').read(); docs=content.split('---'); result=[d for d in docs if d.strip() and not any(l.strip()=='kind: Namespace' for l in d.strip().split(chr(10)))]; open('templates/forklift-operator.yaml','w').write('---'.join(result))"
	# Forklift CRDs ship Go-template literals (e.g. `{{.PlanName}}`) in
	# field descriptions/examples; Helm would try to evaluate them. Escape
	# every `{{`/`}}` as `{{\`{{\`}}`/`{{\`}}\`}}` so they survive rendering.
	python3 -c "import sys; p='templates/forklift-operator.yaml'; c=open(p).read(); c=c.replace('{{','XXLEFTXX').replace('}}','{{\`}}\`}}').replace('XXLEFTXX','{{\`{{\`}}'); open(p,'w').write(c)"
