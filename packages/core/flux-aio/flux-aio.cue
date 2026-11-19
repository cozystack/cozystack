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
				// source-watcher builds one ExternalArtifact per PackageSource
				// component; on the management cluster that is ~100 concurrent
				// builds during install. Give it CPU headroom so it is not
				// starved by its sibling controllers in the all-in-one pod.
				// The module has no per-controller args override for the
				// watcher, so its --concurrent bump is applied as a post-render
				// patch in the Makefile.
				controllers: watcher: resources: {
					requests: {
						cpu:    "500m"
						memory: "64Mi"
					}
					limits: memory: "1Gi"
				}
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
