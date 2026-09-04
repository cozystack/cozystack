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
	sed -i 's|$${OPERATOR_IMAGE}|quay.io/kubev2v/forklift-operator:release-2.11@sha256:1ffde2c6b88419a2aee17b871eaf36c078af1f1b21369bf131b84f7b93cb012f|g' templates/forklift-operator.yaml
	sed -i 's|$${CONTROLLER_IMAGE}|quay.io/kubev2v/forklift-controller:release-2.11@sha256:2600e68a235e4c3351a38ce2dd97562206795a733abd8b0cfd8444a541b0db51|g' templates/forklift-operator.yaml
	sed -i 's|$${API_IMAGE}|quay.io/kubev2v/forklift-api:release-2.11@sha256:f8ad08949f5d0f051a81b2e198df7273f55d16cfd41878f41133e3a0ed1ad5ec|g' templates/forklift-operator.yaml
	sed -i 's|$${MUST_GATHER_IMAGE}|quay.io/kubev2v/forklift-must-gather:v2.7.1@sha256:62eecec1408be00e907fbde6b13be9223fdbca116933ecba85cd87d68d6acfbb|g' templates/forklift-operator.yaml
	sed -i 's|$${UI_PLUGIN_IMAGE}|quay.io/kubev2v/forklift-console-plugin:release-v2.6.7@sha256:99c34ac714604853f4abf589e1ccddb13cd9eaf1bd1eae2c928c622deb22304d|g' templates/forklift-operator.yaml
	sed -i 's|$${CLI_DOWNLOAD_IMAGE}|quay.io/kubev2v/forklift-cli-download:release-2.11@sha256:8d59927b902bbdea6ecb76c0bae358c9d9d2ed40aaa56155d82d8c15ac56634a|g' templates/forklift-operator.yaml
	sed -i 's|$${VALIDATION_IMAGE}|quay.io/kubev2v/forklift-validation:release-2.11@sha256:fc06f88fe54130ca24d39f4c9261e2359c122cb7b5146fa93403e8b0f0c0716f|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE}|quay.io/kubev2v/forklift-virt-v2v:release-2.11@sha256:fe373a538db180c5bed2c99df667a450c432671ad02057d4c692776f7d7cc203|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE_RHEL9}|quay.io/kubev2v/forklift-virt-v2v-xfs:release-2.11@sha256:5402dbc95daacb7d5caab2c855c1cb6889575e2d9d4d282ce9dae201d6cf0331|g' templates/forklift-operator.yaml
	sed -i 's|$${POPULATOR_CONTROLLER_IMAGE}|quay.io/kubev2v/populator-controller:release-2.11@sha256:70d8dc184b41168496d280622e41e8d7fc15c82640be6f025c22d101c1f336bb|g' templates/forklift-operator.yaml
	sed -i 's|$${OVIRT_POPULATOR_IMAGE}|quay.io/kubev2v/ovirt-populator:release-2.11@sha256:223857c91dd2a1050d6d087c6b58845bada676a54d450a7ef7bc7c7bfec28784|g' templates/forklift-operator.yaml
	sed -i 's|$${OPENSTACK_POPULATOR_IMAGE}|quay.io/kubev2v/openstack-populator:release-2.11@sha256:f50cfb4f4bc86666ba5cd304f1baa95bbefc843ee677ad338e927cbef88604b0|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-ova-provider-server:release-2.11@sha256:5901f34027354554f91f2be6669eb458bcb0a568d572c93188a3313904e0020a|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROXY_IMAGE}|quay.io/kubev2v/forklift-ova-proxy:release-2.11@sha256:bd4e662e8c3055c7b4aa2ce1db80ec06b27016c9c39e0358d036031669fc88ca|g' templates/forklift-operator.yaml
	sed -i 's|$${VSPHERE_XCOPY_VOLUME_POPULATOR_IMAGE}|quay.io/kubev2v/vsphere-xcopy-volume-populator:release-2.11@sha256:e63773d07e8612e9d153d9e2b93df757d0887980f2455ea278f02c32821fd954|g' templates/forklift-operator.yaml
	sed -i 's|$${HYPERV_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-hyperv-provider-server:release-2.11@sha256:7bba40b2fca79a96c3388cb1c4f4a71694f5059324ba52e5821342b3a3058e23|g' templates/forklift-operator.yaml
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
