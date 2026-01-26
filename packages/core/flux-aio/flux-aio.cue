bundle: {
	apiVersion: "v1alpha1"
	name:       "flux-aio"
	instances: {
		"flux": {
			module: {
				url:     "oci://ghcr.io/stefanprodan/modules/flux-aio"
				version: "latest"
			}
			namespace: "cozy-fluxcd"
			values: {
				securityProfile: "privileged"
				tolerations: [{
					operator: "Exists"
					key:      "node.kubernetes.io/not-ready"
				}, {
					operator:          "Exists"
					key:               "node.kubernetes.io/unreachable"
				}, {
					operator:          "Exists"
					key:               "node.cilium.io/agent-not-ready"
				}, {
					operator: "Exists"
					key:      "node.cloudprovider.kubernetes.io/uninitialized"
				}]
			}
		}
	}
}
