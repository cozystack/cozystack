include ../../hack/app-helm.mk

update:
	rm -rf charts
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/sergelogvinov/proxmox-cloud-controller-manager | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/sergelogvinov/proxmox-cloud-controller-manager/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 1 proxmox-cloud-controller-manager-$${tag#*v}/charts
	sed -i 's/^  namespace: .*/  namespace: kube-system/' charts/proxmox-cloud-controller-manager/templates/rolebinding.yaml
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/sergelogvinov/proxmox-csi-plugin | awk -F'[/^]' 'END{print $$3}') && \
	curl -sSL https://github.com/sergelogvinov/proxmox-csi-plugin/archive/refs/tags/$${tag}.tar.gz | \
	tar xzvf - --strip 1 proxmox-csi-plugin-$${tag#*v}/charts
	rm -f charts/proxmox-csi-plugin/templates/namespace.yaml
	patch -p 3 < patches/namespace.patch
