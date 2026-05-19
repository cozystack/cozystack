export NAME=forklift-operator
export NAMESPACE=cozy-forklift

include ../../../hack/package.mk

# Forklift release. Upstream tags container images as `release-2.11`,
# not `v2.11.x`, so we use that channel here. Bump VERSION to ride
# patch releases; bumping FORKLIFT_CHANNEL (e.g. release-2.12) is a
# breaking change that requires re-checking CRDs against vm-import.
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
	sed -i 's|$${OPERATOR_IMAGE}|quay.io/kubev2v/forklift-operator:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${CONTROLLER_IMAGE}|quay.io/kubev2v/forklift-controller:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${API_IMAGE}|quay.io/kubev2v/forklift-api:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${MUST_GATHER_IMAGE}|quay.io/kubev2v/forklift-must-gather:latest|g' templates/forklift-operator.yaml
	sed -i 's|$${UI_PLUGIN_IMAGE}|quay.io/kubev2v/forklift-console-plugin:latest|g' templates/forklift-operator.yaml
	sed -i 's|$${CLI_DOWNLOAD_IMAGE}|quay.io/kubev2v/forklift-cli-download:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${VALIDATION_IMAGE}|quay.io/kubev2v/forklift-validation:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE}|quay.io/kubev2v/forklift-virt-v2v:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${VIRT_V2V_IMAGE_RHEL9}|quay.io/kubev2v/forklift-virt-v2v-xfs:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${POPULATOR_CONTROLLER_IMAGE}|quay.io/kubev2v/populator-controller:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${OVIRT_POPULATOR_IMAGE}|quay.io/kubev2v/ovirt-populator:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${OPENSTACK_POPULATOR_IMAGE}|quay.io/kubev2v/openstack-populator:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-ova-provider-server:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${OVA_PROXY_IMAGE}|quay.io/kubev2v/forklift-ova-proxy:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${VSPHERE_XCOPY_VOLUME_POPULATOR_IMAGE}|quay.io/kubev2v/vsphere-xcopy-volume-populator:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	sed -i 's|$${HYPERV_PROVIDER_SERVER_IMAGE}|quay.io/kubev2v/forklift-hyperv-provider-server:$(FORKLIFT_CHANNEL)|g' templates/forklift-operator.yaml
	# Tunable env vars (defaults from upstream Ansible role).
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
