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
	sed -i 's|$${OPERATOR_IMAGE}|quay.io/kubev2v/forklift-operator:release-2.11@sha256:ed13c2890d09555580c5b53a7e9f67c7846d759c227b2145576ce9cfca7c58e2|g' templates/forklift-operator.yaml
	sed -i 's|$${CONTROLLER_IMAGE}|quay.io/kubev2v/forklift-controller:release-2.11@sha256:f265574b8432d164d4ae4b388825cbb8ef7806b4afe1e87880735a8dc1868754|g' templates/forklift-operator.yaml
	sed -i 's|$${API_IMAGE}|quay.io/kubev2v/forklift-api:release-2.11@sha256:b78142f746c6380afc3719956e47b1860afd4435e449377629cd03459dc73255|g' templates/forklift-operator.yaml
	sed -i 's|$${MUST_GATHER_IMAGE}|quay.io/kubev2v/forklift-must-gather:v2.7.1@sha256:62eecec1408be00e907fbde6b13be9223fdbca116933ecba85cd87d68d6acfbb|g' templates/forklift-operator.yaml
	sed -i 's|$${UI_PLUGIN_IMAGE}|quay.io/kubev2v/forklift-console-plugin:release-v2.6.7@sha256:99c34ac714604853f4abf589e1ccddb13cd9eaf1bd1eae2c928c622deb22304d|g' templates/forklift-operator.yaml
	sed -i 's|$${CLI_DOWNLOAD_IMAGE}|quay.io/kubev2v/forklift-cli-download:release-2.11@sha256:3330eab526f490fabbd9c6640696e45b0482377edf07b805a07663b562145337|g' templates/forklift-operator.yaml
	sed -i 's|$${VALIDATION_IMAGE}|quay.io/kubev2v/forklift-validation:release-2.11@sha256:e202c93485dbef2536ecfd9b0ddab4cb12c2efc33ff5af721eb4fa492aff742d|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE}|quay.io/kubev2v/forklift-virt-v2v:release-2.11@sha256:c25718c24f84ee0a4c72152ee9641ecc13d94f1397c9942af1825906bce50b58|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE_RHEL9}|quay.io/kubev2v/forklift-virt-v2v-xfs:release-2.11@sha256:5963fade6ba16e62ead0979becf7782de787a5cdceb559097e47dbf64939559d|g' templates/forklift-operator.yaml
	sed -i 's|$${POPULATOR_CONTROLLER_IMAGE}|quay.io/kubev2v/populator-controller:release-2.11@sha256:b0912446fb9d3c112e60f6867742c7333d22aff4a666f26bef8f8846c2faa67b|g' templates/forklift-operator.yaml
	sed -i 's|$${OVIRT_POPULATOR_IMAGE}|quay.io/kubev2v/ovirt-populator:release-2.11@sha256:3e0320559c4e16437c30511e06e7759822d656a7a5d5d9b780df7d4c833da64e|g' templates/forklift-operator.yaml
	sed -i 's|$${OPENSTACK_POPULATOR_IMAGE}|quay.io/kubev2v/openstack-populator:release-2.11@sha256:b8505145af2fa24006bbdf4598039121c0922a74f0b248b584da6293451690d7|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-ova-provider-server:release-2.11@sha256:db4fa560d37ff90917f8f62a7b4dc26b5e6d4acfa6a5d73adb94e41431d824fd|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROXY_IMAGE}|quay.io/kubev2v/forklift-ova-proxy:release-2.11@sha256:4c88ec127830ed39a50c67357e22ea75c654c53d5422ea4bcaa7457eb8bc545d|g' templates/forklift-operator.yaml
	sed -i 's|$${VSPHERE_XCOPY_VOLUME_POPULATOR_IMAGE}|quay.io/kubev2v/vsphere-xcopy-volume-populator:release-2.11@sha256:b76dd4d4f942ccad94c694918455cab3471ef2142dfdda8bcfc9f0ba3c1b327a|g' templates/forklift-operator.yaml
	sed -i 's|$${HYPERV_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-hyperv-provider-server:release-2.11@sha256:49b3e8cb06c9e854f67eb0d8b2e4b2c2f6463018a5ee08b0ec81600b45f5231d|g' templates/forklift-operator.yaml
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
