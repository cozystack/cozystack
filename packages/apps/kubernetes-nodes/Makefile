include ../../../hack/package.mk

.PHONY: test generate update

test:
	helm unittest .
	bash tests/render-parity.sh

generate:
	cozyvalues-gen -m 'kubernetesnodes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetesnodes/types.go
	../../../hack/update-crd.sh

# Keep the worker-image inputs (Talos<->Kubernetes support matrix and the
# kubectl.tag used by the talos-reconcile Job) and the worker machine-config
# template in lockstep with the parent kubernetes chart. They are copied, not
# symlinked, so `helm package` bundles them; run this after the parent chart's
# `make update`.
#
# _talosconfigtemplate.tpl must stay byte-identical to the parent's copy: the
# TalosConfigTemplate it renders is content-hash named, so any divergence would
# point this pool's MachineDeployment at a different bootstrap template than the
# parent rendered for the same group and roll every live worker. tests/render-
# parity.sh compares the MachineDeployment across both charts and fails on it.
update:
	cp ../kubernetes/files/versions.yaml files/versions.yaml
	cp ../kubernetes/images/kubectl.tag images/kubectl.tag
	cp ../kubernetes/templates/talos/_talosconfigtemplate.tpl templates/_talosconfigtemplate.tpl
	$(MAKE) generate
