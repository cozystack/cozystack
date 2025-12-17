package dashboard

import (
	"encoding/json"
	"strings"

	dashboardv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ---------------- Complete refactored static resources ----------------

// CreateAllBreadcrumbs creates all breadcrumb resources using helper functions
func CreateAllBreadcrumbs() []*dashboardv1alpha1.Breadcrumb {
	return []*dashboardv1alpha1.Breadcrumb{
		// Stock project factory configmap details
		createBreadcrumb("stock-project-factory-configmap-details", []map[string]any{
			createBreadcrumbItem("configmaps", "v1/configmaps", "/openapi-ui/{clusterName}/{namespace}/builtin-table/configmaps"),
			createBreadcrumbItem("configmap", "{6}"),
		}),

		// Stock cluster factory namespace details
		createBreadcrumb("stock-cluster-factory-namespace-details", []map[string]any{
			createBreadcrumbItem("namespaces", "v1/namespaces", "/openapi-ui/{clusterName}/builtin-table/namespaces"),
			createBreadcrumbItem("namespace", "{5}"),
		}),

		// Stock cluster factory node details
		createBreadcrumb("stock-cluster-factory-node-details", []map[string]any{
			createBreadcrumbItem("node", "v1/nodes", "/openapi-ui/{clusterName}/builtin-table/nodes"),
			createBreadcrumbItem("node", "{5}"),
		}),

		// Stock project factory pod details
		createBreadcrumb("stock-project-factory-pod-details", []map[string]any{
			createBreadcrumbItem("pods", "v1/pods", "/openapi-ui/{clusterName}/{namespace}/builtin-table/pods"),
			createBreadcrumbItem("pod", "{6}"),
		}),

		// Stock project factory secret details
		createBreadcrumb("stock-project-factory-kube-secret-details", []map[string]any{
			createBreadcrumbItem("secrets", "v1/secrets", "/openapi-ui/{clusterName}/{namespace}/builtin-table/secrets"),
			createBreadcrumbItem("secret", "{6}"),
		}),

		// Stock project factory service details
		createBreadcrumb("stock-project-factory-kube-service-details", []map[string]any{
			createBreadcrumbItem("services", "v1/services", "/openapi-ui/{clusterName}/{namespace}/builtin-table/services"),
			createBreadcrumbItem("service", "{6}"),
		}),

		// Stock project factory ingress details
		createBreadcrumb("stock-project-factory-kube-ingress-details", []map[string]any{
			createBreadcrumbItem("ingresses", "networking.k8s.io/v1/ingresses", "/openapi-ui/{clusterName}/{namespace}/builtin-table/ingresses"),
			createBreadcrumbItem("ingress", "{6}"),
		}),

		// Stock cluster api table
		createBreadcrumb("stock-cluster-api-table", []map[string]any{
			createBreadcrumbItem("api", "{apiGroup}/{apiVersion}/{typeName}"),
		}),

		// Stock cluster api form
		createBreadcrumb("stock-cluster-api-form", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "{apiGroup}/{apiVersion}/{typeName}", "/openapi-ui/{clusterName}/api-table/{apiGroup}/{apiVersion}/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Create"),
		}),

		// Stock cluster api form edit
		createBreadcrumb("stock-cluster-api-form-edit", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "{apiGroup}/{apiVersion}/{typeName}", "/openapi-ui/{clusterName}/api-table/{apiGroup}/{apiVersion}/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Update"),
		}),

		// Stock cluster builtin table
		createBreadcrumb("stock-cluster-builtin-table", []map[string]any{
			createBreadcrumbItem("api", "v1/{typeName}"),
		}),

		// Stock cluster builtin form
		createBreadcrumb("stock-cluster-builtin-form", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "v1/{typeName}", "/openapi-ui/{clusterName}/builtin-table/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Create"),
		}),

		// Stock cluster builtin form edit
		createBreadcrumb("stock-cluster-builtin-form-edit", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "v1/{typeName}", "/openapi-ui/{clusterName}/builtin-table/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Update"),
		}),

		// Stock project api table
		createBreadcrumb("stock-project-api-table", []map[string]any{
			createBreadcrumbItem("api", "{apiGroup}/{apiVersion}/{typeName}"),
		}),

		// Stock project api form
		createBreadcrumb("stock-project-api-form", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "{apiGroup}/{apiVersion}/{typeName}", "/openapi-ui/{clusterName}/{namespace}/api-table/{apiGroup}/{apiVersion}/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Create"),
		}),

		// Stock project api form edit
		createBreadcrumb("stock-project-api-form-edit", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "{apiGroup}/{apiVersion}/{typeName}", "/openapi-ui/{clusterName}/{namespace}/api-table/{apiGroup}/{apiVersion}/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Update"),
		}),

		// Stock project builtin table
		createBreadcrumb("stock-project-builtin-table", []map[string]any{
			createBreadcrumbItem("api", "v1/{typeName}"),
		}),

		// Stock project builtin form
		createBreadcrumb("stock-project-builtin-form", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "v1/{typeName}", "/openapi-ui/{clusterName}/{namespace}/builtin-table/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Create"),
		}),

		// Stock project builtin form edit
		createBreadcrumb("stock-project-builtin-form-edit", []map[string]any{
			createBreadcrumbItem("create-api-res-namespaced-table", "v1/{typeName}", "/openapi-ui/{clusterName}/{namespace}/builtin-table/{typeName}"),
			createBreadcrumbItem("create-api-res-namespaced-typename", "Update"),
		}),
	}
}

// CreateAllCustomColumnsOverrides creates all custom column override resources using helper functions
func CreateAllCustomColumnsOverrides() []*dashboardv1alpha1.CustomColumnsOverride {
	return []*dashboardv1alpha1.CustomColumnsOverride{
		// Factory details v1 services
		createCustomColumnsOverride("factory-details-v1.services", []any{
			createCustomColumnWithSpecificColor("Name", "Service", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-service-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("ClusterIP", ".spec.clusterIP"),
			createStringColumn("LoadbalancerIP", ".spec.loadBalancerIP"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace v1 services
		createCustomColumnsOverride("stock-namespace-/v1/services", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Service", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-service-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("ClusterIP", ".spec.clusterIP"),
			createStringColumn("LoadbalancerIP", ".status.loadBalancer.ingress[0].ip"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace core cozystack io v1alpha1 tenantmodules
		createCustomColumnsOverride("stock-namespace-/core.cozystack.io/v1alpha1/tenantmodules", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Module", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/{reqsJsonPath[0]['.metadata.name']['-']}-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createReadyColumn(),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
			createStringColumn("Version", ".status.version"),
		}),

		// Factory service details port mapping
		createCustomColumnsOverride("factory-kube-service-details-port-mapping", []any{
			createStringColumn("Name", ".name"),
			createStringColumn("Port", ".port"),
			createStringColumn("Protocol", ".protocol"),
			createStringColumn("Pod port or name", ".targetPort"),
		}),

		// Factory details v1alpha1 cozystack io workloadmonitors
		createCustomColumnsOverride("factory-details-v1alpha1.cozystack.io.workloadmonitors", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "WorkloadMonitor", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/workloadmonitor-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("TYPE", ".spec.type"),
			createStringColumn("VERSION", ".spec.version"),
			createStringColumn("REPLICAS", ".spec.replicas"),
			createStringColumn("MINREPLICAS", ".spec.minReplicas"),
			createStringColumn("AVAILABLE", ".status.availableReplicas"),
			createStringColumn("OBSERVED", ".status.observedReplicas"),
		}),

		// Factory details v1alpha1 core cozystack io tenantsecrets
		createCustomColumnsOverride("factory-details-v1alpha1.core.cozystack.io.tenantsecrets", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Secret", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-secret-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createFlatMapColumn("Data", ".data"),
			createStringColumn("Key", "_flatMapData_Key"),
			createSecretBase64Column("Value", "._flatMapData_Value"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Virtual private cloud subnets
		createCustomColumnsOverride("virtualprivatecloud-subnets", []any{
			createFlatMapColumn("Data", ".data"),
			createStringColumn("Subnet Parameters", "_flatMapData_Key"),
			createStringColumn("Values", "_flatMapData_Value"),
		}),

		// Factory ingress details rules
		createCustomColumnsOverride("factory-kube-ingress-details-rules", []any{
			createStringColumn("Host", ".host"),
			createCustomColumnWithJsonPath("Service", ".http.paths[0].backend.service.name", "Service", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-service-details/{reqsJsonPath[0]['.http.paths[0].backend.service.name']['-']}"),
			createStringColumn("Port", ".http.paths[0].backend.service.port.number"),
			createStringColumn("Path", ".http.paths[0].path"),
		}),

		// Factory node images
		createCustomColumnsOverride("factory-node-images", []any{
			createStringColumn("ImageID", ".names[0]"),
			createConverterBytesColumn("Size", ".sizeBytes"),
		}),

		// Factory pod details volume list
		createCustomColumnsOverride("factory-pod-details-volume-list", []any{
			createStringColumn("Name", ".name"),
		}),

		// Factory status conditions
		createCustomColumnsOverride("factory-status-conditions", []any{
			createStringColumn("Type", ".type"),
			createBoolColumn("Status", ".status"),
			createTimestampColumn("Updated", ".lastTransitionTime"),
			createStringColumn("Reason", ".reason"),
			createStringColumn("Message", ".message"),
		}),

		// Container status init containers list
		createCustomColumnsOverride("container-status-init-containers-list", []any{
			createStringColumn("Name", ".name"),
			createStringColumn("Image", ".imageID"),
			createBoolColumn("Started", ".started"),
			createBoolColumn("Ready", ".ready"),
			createStringColumn("RestartCount", ".restartCount"),
			createStringColumn("WaitingReason", ".state.waiting.reason"),
			createStringColumn("TerminatedReason", ".state.terminated.reason"),
		}),

		// Container status containers list
		createCustomColumnsOverride("container-status-containers-list", []any{
			createStringColumn("Name", ".name"),
			createStringColumn("Image", ".imageID"),
			createBoolColumn("Started", ".started"),
			createBoolColumn("Ready", ".ready"),
			createStringColumn("RestartCount", ".restartCount"),
			createStringColumn("WaitingReason", ".state.waiting.reason"),
			createStringColumn("TerminatedReason", ".state.terminated.reason"),
		}),

		// Container spec init containers list
		createCustomColumnsOverride("container-spec-init-containers-list", []any{
			createStringColumn("Name", ".name"),
			createStringColumn("Image", ".image"),
			createArrayColumn("Resources requests", ".resources.requests"),
			createArrayColumn("Resources limits", ".resources.limits"),
		}),

		// Container spec containers list
		createCustomColumnsOverride("container-spec-containers-list", []any{
			createStringColumn("Name", ".name"),
			createStringColumn("Image", ".image"),
			createArrayColumn("Resources requests", ".resources.requests"),
			createArrayColumn("Resources limits", ".resources.limits"),
			createArrayColumn("Ports", ".ports[*].containerPort"),
		}),

		// Factory details networking k8s io v1 ingresses
		createCustomColumnsOverride("factory-details-networking.k8s.io.v1.ingresses", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Ingress", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-ingress-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("Hosts", ".spec.rules[*].host"),
			createStringColumn("Address", ".status.loadBalancer.ingress[0].ip"),
			createStringColumn("Port", ".spec.defaultBackend.service.port.number"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace networking k8s io v1 ingresses
		createCustomColumnsOverride("stock-namespace-/networking.k8s.io/v1/ingresses", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Ingress", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-ingress-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("Hosts", ".spec.rules[*].host"),
			createStringColumn("Address", ".status.loadBalancer.ingress[0].ip"),
			createStringColumn("Port", ".spec.defaultBackend.service.port.number"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock cluster v1 configmaps
		createCustomColumnsOverride("stock-cluster-/v1/configmaps", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "ConfigMap", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/configmap-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithJsonPath("Namespace", ".metadata.namespace", "Namespace", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace v1 configmaps
		createCustomColumnsOverride("stock-namespace-/v1/configmaps", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "ConfigMap", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/configmap-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Cluster v1 configmaps
		createCustomColumnsOverride("cluster-/v1/configmaps", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "ConfigMap", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/configmap-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithJsonPath("Namespace", ".metadata.namespace", "Namespace", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock cluster v1 nodes
		createCustomColumnsOverride("stock-cluster-/v1/nodes", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Node", "", "/openapi-ui/{2}/factory/node-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createSimpleStatusColumn("Status", "node-status"),
		}),

		// Factory node details v1 pods
		createCustomColumnsOverride("factory-node-details-v1.pods", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Pod", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/pod-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithJsonPath("Namespace", ".metadata.namespace", "Namespace", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace"),
			createStringColumn("Restart Policy", ".spec.restartPolicy"),
			createStringColumn("Pod IP", ".status.podIP"),
			createStringColumn("QOS", ".status.qosClass"),
			createSimpleStatusColumn("Status", "pod-status"),
		}),

		// Factory v1 pods
		createCustomColumnsOverride("factory-v1.pods", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Pod", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/pod-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithoutJsonPath("Node", "Node", "", "/openapi-ui/{2}/factory/node-details/{reqsJsonPath[0]['.spec.nodeName']['-']}"),
			createStringColumn("Restart Policy", ".spec.restartPolicy"),
			createStringColumn("Pod IP", ".status.podIP"),
			createStringColumn("QOS", ".status.qosClass"),
			createSimpleStatusColumn("Status", "pod-status"),
		}),

		// Stock cluster v1 pods
		createCustomColumnsOverride("stock-cluster-/v1/pods", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Pod", "#009596", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/pod-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithJsonPath("Namespace", ".metadata.namespace", "Namespace", "#a25792ff", "/openapi-ui/{2}/factory/tenantnamespace/{reqsJsonPath[0]['.metadata.namespace']['-']}"),
			createCustomColumnWithJsonPath("Node", ".spec.nodeName", "Node", "#8476d1", "/openapi-ui/{2}/factory/node-details/{reqsJsonPath[0]['.spec.nodeName']['-']}"),
			createStringColumn("Restart Policy", ".spec.restartPolicy"),
			createStringColumn("Pod IP", ".status.podIP"),
			createStringColumn("QOS", ".status.qosClass"),
			createSimpleStatusColumn("Status", "pod-status"),
		}),

		// Stock namespace v1 pods
		createCustomColumnsOverride("stock-namespace-/v1/pods", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Pod", "#009596", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/pod-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithoutJsonPath("Node", "Node", "#8476d1", "/openapi-ui/{2}/factory/node-details/{reqsJsonPath[0]['.spec.nodeName']['-']}"),
			createStringColumn("Restart Policy", ".spec.restartPolicy"),
			createStringColumn("Pod IP", ".status.podIP"),
			createStringColumn("QOS", ".status.qosClass"),
			createSimpleStatusColumn("Status", "pod-status"),
		}),

		// Stock cluster v1 secrets
		createCustomColumnsOverride("stock-cluster-/v1/secrets", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Secret", "#c46100", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-secret-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createCustomColumnWithJsonPath("Namespace", ".metadata.namespace", "Namespace", "#a25792ff", "/openapi-ui/{2}/factory/tenantnamespace/{reqsJsonPath[0]['.metadata.namespace']['-']}"),
			createStringColumn("Type", ".type"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace v1 secrets
		createCustomColumnsOverride("stock-namespace-/v1/secrets", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Secret", "#c46100", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/kube-secret-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createStringColumn("Type", ".type"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Factory details v1alpha1 cozystack io workloads
		createCustomColumnsOverride("factory-details-v1alpha1.cozystack.io.workloads", []any{
			createStringColumn("Name", ".metadata.name"),
			createStringColumn("Kind", ".status.kind"),
			createStringColumn("Type", ".status.type"),
			createStringColumn("CPU", ".status.resources.cpu"),
			createStringColumn("Memory", ".status.resources.memory"),
			createStringColumn("Operational", ".status.operational"),
		}),

		// Stock cluster core cozystack io v1alpha1 tenantnamespaces
		createCustomColumnsOverride("stock-cluster-/core.cozystack.io/v1alpha1/tenantnamespaces", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "TenantNamespace", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.name']['-']}/factory/marketplace"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),

		// Stock namespace backups cozystack io v1alpha1 plans
		createCustomColumnsOverride("stock-namespace-/backups.cozystack.io/v1alpha1/plans", []any{
			createCustomColumnWithJsonPath("Name", ".metadata.name", "Plan", "", "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/plan-details/{reqsJsonPath[0]['.metadata.name']['-']}"),
			createApplicationRefColumn("Application"),
			createTimestampColumn("Created", ".metadata.creationTimestamp"),
		}),
	}
}

// CreateAllCustomFormsOverrides creates all custom forms override resources using helper functions
func CreateAllCustomFormsOverrides() []*dashboardv1alpha1.CustomFormsOverride {
	return []*dashboardv1alpha1.CustomFormsOverride{
		// Default networking k8s io v1 ingresses
		createCustomFormsOverride("default-/networking.k8s.io/v1/ingresses", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("spec.rules", "Rules", "array"),
			},
		}),

		// Default storage k8s io v1 storageclasses
		createCustomFormsOverride("default-/storage.k8s.io/v1/storageclasses", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("provisioner", "Provisioner", "text"),
				createFormItem("reclaimPolicy", "Reclaim Policy", "select"),
			},
		}),

		// Default v1 configmaps
		createCustomFormsOverride("default-/v1/configmaps", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("data", "Data", "object"),
			},
		}),

		// Default v1 namespaces
		createCustomFormsOverride("default-/v1/namespaces", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.labels", "Labels", "object"),
			},
		}),

		// Default v1 nodes
		createCustomFormsOverride("default-/v1/nodes", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("spec.podCIDR", "Pod CIDR", "text"),
			},
		}),

		// Default v1 persistentvolumeclaims
		createCustomFormsOverride("default-/v1/persistentvolumeclaims", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("spec.accessModes", "Access Modes", "array"),
			},
		}),

		// Default v1 persistentvolumes
		createCustomFormsOverride("default-/v1/persistentvolumes", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("spec.capacity", "Capacity", "object"),
			},
		}),

		// Default v1 pods
		createCustomFormsOverride("default-/v1/pods", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("spec.containers", "Containers", "array"),
			},
		}),

		// Default v1 secrets
		createCustomFormsOverride("default-/v1/secrets", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("type", "Type", "text"),
			},
		}),

		// Default v1 services
		createCustomFormsOverride("default-/v1/services", map[string]any{
			"formItems": []any{
				createFormItem("metadata.name", "Name", "text"),
				createFormItem("metadata.namespace", "Namespace", "text"),
				createFormItem("spec.ports", "Ports", "array"),
			},
		}),
	}
}

// CreateAllFactories creates all factory resources using helper functions
func CreateAllFactories() []*dashboardv1alpha1.Factory {
	// Marketplace factory
	marketplaceSpec := map[string]any{
		"key": "marketplace",
		"sidebarTags": []any{
			"marketplace-sidebar",
		},
		"urlsToFetch":                   []any{},
		"withScrollableMainContentCard": true,
		"data": []any{
			contentCardWithTitle(31, "Marketplace", map[string]any{
				"flexGrow": 1,
			}, []any{
				map[string]any{
					"data": map[string]any{
						"baseApiVersion":       "v1alpha1",
						"baseprefix":           "openapi-ui",
						"clusterNamePartOfUrl": "{2}",
						"id":                   311,
						"mpResourceKind":       "MarketplacePanel",
						"mpResourceName":       "marketplacepanels",
						"namespacePartOfUrl":   "{3}",
						"baseApiGroup":         "dashboard.cozystack.io",
					},
					"type": "MarketplaceCard",
				},
			}),
		},
	}

	// Namespace details factory using unified approach
	namespaceConfig := UnifiedResourceConfig{
		Name:         "namespace-details",
		ResourceType: "factory",
		Kind:         "Namespace",
		Plural:       "namespaces",
		Title:        "namespace",
	}
	namespaceSpec := createUnifiedFactory(namespaceConfig, nil, []any{"/api/clusters/{2}/k8s/api/v1/namespaces/{5}"})

	// Node details factory
	nodeHeader := createNodeHeader()
	// Create node spec with tabs containing items
	nodeTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				map[string]any{
					"type": "ContentCard",
					"data": map[string]any{
						"id": "details-card",
						"style": map[string]any{
							"marginBottom": "24px",
						},
					},
					"children": []any{
						map[string]any{
							"type": "antdText",
							"data": map[string]any{
								"id":     "details-title",
								"text":   "Node details",
								"strong": true,
								"style": map[string]any{
									"fontSize":     float64(20),
									"marginBottom": "12px",
								},
							},
						},
					},
				},
			},
		},
	}
	nodeSpec := map[string]any{
		"key":                           "node-details",
		"sidebarTags":                   []any{"node-sidebar"},
		"withScrollableMainContentCard": true,
		"urlsToFetch":                   []any{"/api/clusters/{2}/k8s/api/v1/nodes/{5}"},
		"data": []any{
			nodeHeader,
			map[string]any{
				"type": "antdTabs",
				"data": map[string]any{
					"id":               "tabs-root",
					"defaultActiveKey": "details",
					"items":            nodeTabs,
				},
			},
		},
	}

	// Pod details factory
	podHeader := createPodHeader()
	// Create pod spec with empty tabs (items: nil)
	podSpec := map[string]any{
		"key":                           "pod-details",
		"sidebarTags":                   []any{"pods-sidebar"},
		"withScrollableMainContentCard": true,
		"urlsToFetch":                   []any{"/api/clusters/{2}/k8s/api/v1/namespaces/{3}/pods/{6}"},
		"data": []any{
			podHeader,
			map[string]any{
				"type": "antdTabs",
				"data": map[string]any{
					"id":               "tabs-root",
					"defaultActiveKey": "details",
					"items":            nil,
				},
			},
		},
	}

	// Secret details factory
	secretHeader := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": "24px",
			},
		},
		"children": []any{
			map[string]any{
				"type": "antdText",
				"data": map[string]any{
					"id":    "badge-secret",
					"text":  "S",
					"title": "secret",
					"style": map[string]any{
						"backgroundColor": "#c46100",
						"borderRadius":    "20px",
						"color":           "#fff",
						"display":         "inline-block",
						"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":        "20px",
						"fontWeight":      400,
						"lineHeight":      "24px",
						"minWidth":        24,
						"padding":         "0 9px",
						"textAlign":       "center",
						"whiteSpace":      "nowrap",
					},
				},
			},
			map[string]any{
				"type": "parsedText",
				"data": map[string]any{
					"id":   "header-secret-name",
					"text": "{reqsJsonPath[0]['.metadata.name']['-']}",
					"style": map[string]any{
						"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":   "20px",
						"lineHeight": "24px",
					},
				},
			},
		},
	}
	secretTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				contentCard("details-card", map[string]any{
					"marginBottom": "24px",
				}, []any{
					antdText("details-title", true, "Secret details", map[string]any{
						"fontSize":     20,
						"marginBottom": "12px",
					}),
					spacer("details-spacer", 16),
					antdRow("details-grid", []any{48, 12}, []any{
						antdCol("col-left", 12, []any{
							antdFlexVertical("col-left-stack", 24, []any{
								antdFlexVertical("meta-name-block", 4, []any{
									antdText("meta-name-label", true, "Name", nil),
									parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
								}),
								antdFlexVertical("meta-namespace-block", 8, []any{
									antdText("meta-name-label", true, "Namespace", nil),
									antdFlex("header-row", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":    "header-badge",
												"text":  "NS",
												"title": "namespace",
												"style": map[string]any{
													"backgroundColor": "#a25792ff",
													"borderRadius":    "20px",
													"color":           "#fff",
													"display":         "inline-block",
													"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
													"fontSize":        "15px",
													"fontWeight":      400,
													"lineHeight":      "24px",
													"minWidth":        24,
													"padding":         "0 9px",
													"textAlign":       "center",
													"whiteSpace":      "nowrap",
												},
											},
										},
										map[string]any{
											"type": "antdLink",
											"data": map[string]any{
												"id":   "namespace-link",
												"text": "{reqsJsonPath[0]['.metadata.namespace']['-']}",
												"href": "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
											},
										},
									}),
								}),
								antdFlexVertical("meta-labels-block", 8, []any{
									antdText("labels-title", true, "Labels", map[string]any{
										"fontSize": 14,
									}),
									map[string]any{
										"type": "Labels",
										"data": map[string]any{
											"id":                                    "labels-editor",
											"endpoint":                              "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/secrets/{6}",
											"jsonPathToLabels":                      ".metadata.labels",
											"pathToValue":                           "/metadata/labels",
											"reqIndex":                              0,
											"modalTitle":                            "Edit labels",
											"modalDescriptionText":                  "",
											"inputLabel":                            "",
											"notificationSuccessMessage":            "Updated successfully",
											"notificationSuccessMessageDescription": "Labels have been updated",
											"editModalWidth":                        650,
											"maxEditTagTextLength":                  35,
											"paddingContainerEnd":                   "24px",
											"containerStyle": map[string]any{
												"marginTop": "-30px",
											},
											"selectProps": map[string]any{
												"maxTagTextLength": 35,
											},
										},
									},
								}),
								antdFlexVertical("ds-annotations", 4, []any{
									antdText("annotations", true, "Annotations", nil),
									map[string]any{
										"type": "Annotations",
										"data": map[string]any{
											"id":                                    "annotations",
											"endpoint":                              "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/secrets/{6}",
											"jsonPathToObj":                         ".metadata.annotations",
											"pathToValue":                           "/metadata/annotations",
											"reqIndex":                              0,
											"modalTitle":                            "Edit annotations",
											"modalDescriptionText":                  "",
											"inputLabel":                            "",
											"notificationSuccessMessage":            "Updated successfully",
											"notificationSuccessMessageDescription": "Annotations have been updated",
											"editModalWidth":                        "800px",
											"errorText":                             "0 Annotations",
											"text":                                  "~counter~ Annotations",
											"cols":                                  []any{11, 11, 2},
										},
									},
								}),
								antdFlexVertical("meta-created-block", 4, []any{
									antdText("time-label", true, "Created", nil),
									antdFlex("time-block", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":   "time-icon",
												"text": "🌐",
											},
										},
										map[string]any{
											"type": "parsedText",
											"data": map[string]any{
												"formatter": "timestamp",
												"id":        "time-value",
												"text":      "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}",
											},
										},
									}),
								}),
							}),
						}),
						antdCol("col-right", 12, []any{
							antdFlexVertical("col-right-stack", 24, []any{
								antdFlexVertical("secret-type-block", 4, []any{
									antdText("secret-type-label", true, "Type", nil),
									parsedText("secret-type-value", "{reqsJsonPath[0]['.type']['-']}", nil),
								}),
								antdFlexVertical("secret-sa-block", 4, []any{
									map[string]any{
										"type": "parsedText",
										"data": map[string]any{
											"id":     "serviceaccount-title",
											"text":   "ServiceAccount",
											"strong": true,
											"style": map[string]any{
												"fontWeight": "bold",
											},
										},
									},
									map[string]any{
										"type": "antdLink",
										"data": map[string]any{
											"id":   "serviceaccount-link",
											"text": "{reqsJsonPath[0]['.metadata.annotations[\"kubernetes.io/service-account.name\"]']['-']}",
											"href": "/openapi-ui/{2}/{3}/factory/serviceaccount-details/{reqsJsonPath[0]['.metadata.annotations[\"kubernetes.io/service-account.name\"]']['-']}",
										},
									},
								}),
							}),
						}),
					}),
				}),
			},
		},
		map[string]any{
			"key":   "yaml",
			"label": "YAML",
			"children": []any{
				map[string]any{
					"type": "YamlEditorSingleton",
					"data": map[string]any{
						"id":                        "yaml-editor",
						"cluster":                   "{2}",
						"isNameSpaced":              true,
						"prefillValuesRequestIndex": 0,
						"substractHeight":           float64(400),
						"type":                      "builtin",
						"typeName":                  "secrets",
						"readOnly":                  true,
					},
				},
			},
		},
	}
	secretSpec := createFactorySpec("kube-secret-details", []any{"secret-sidebar"}, []any{"/api/clusters/{2}/k8s/api/v1/namespaces/{3}/secrets/{6}"}, secretHeader, secretTabs)

	// Service details factory
	serviceHeader := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": "24px",
			},
		},
		"children": []any{
			map[string]any{
				"type": "antdText",
				"data": map[string]any{
					"id":    "badge-service",
					"text":  "S",
					"title": "services",
					"style": map[string]any{
						"backgroundColor": "#6ca100",
						"borderRadius":    "20px",
						"color":           "#fff",
						"display":         "inline-block",
						"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":        "20px",
						"fontWeight":      400,
						"lineHeight":      "24px",
						"minWidth":        24,
						"padding":         "0 9px",
						"textAlign":       "center",
						"whiteSpace":      "nowrap",
					},
				},
			},
			map[string]any{
				"type": "parsedText",
				"data": map[string]any{
					"id":   "service-name",
					"text": "{reqsJsonPath[0]['.metadata.name']['-']}",
					"style": map[string]any{
						"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":   "20px",
						"lineHeight": "24px",
					},
				},
			},
		},
	}
	serviceTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				contentCard("details-card", map[string]any{
					"marginBottom": "24px",
				}, []any{
					antdText("details-title", true, "Service details", map[string]any{
						"fontSize":     20,
						"marginBottom": "12px",
					}),
					spacer("details-spacer", 16),
					antdRow("details-grid", []any{48, 12}, []any{
						antdCol("col-left", 12, []any{
							antdFlexVertical("col-left-stack", 24, []any{
								antdFlexVertical("meta-name-block", 4, []any{
									antdText("meta-name-label", true, "Name", nil),
									parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
								}),
								antdFlexVertical("meta-namespace-block", 8, []any{
									antdText("meta-name-label", true, "Namespace", nil),
									antdFlex("header-row", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":    "header-badge",
												"text":  "NS",
												"title": "namespace",
												"style": map[string]any{
													"backgroundColor": "#a25792ff",
													"borderRadius":    "20px",
													"color":           "#fff",
													"display":         "inline-block",
													"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
													"fontSize":        "15px",
													"fontWeight":      400,
													"lineHeight":      "24px",
													"minWidth":        24,
													"padding":         "0 9px",
													"textAlign":       "center",
													"whiteSpace":      "nowrap",
												},
											},
										},
										map[string]any{
											"type": "antdLink",
											"data": map[string]any{
												"id":   "namespace-link",
												"text": "{reqsJsonPath[0]['.metadata.namespace']['-']}",
												"href": "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
											},
										},
									}),
								}),
								antdFlexVertical("meta-labels-block", 8, []any{
									antdText("labels-title", true, "Labels", map[string]any{
										"fontSize": 14,
									}),
									map[string]any{
										"type": "Labels",
										"data": map[string]any{
											"id":                                    "labels-editor",
											"endpoint":                              "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/services/{6}",
											"jsonPathToLabels":                      ".metadata.labels",
											"pathToValue":                           "/metadata/labels",
											"reqIndex":                              0,
											"modalTitle":                            "Edit labels",
											"modalDescriptionText":                  "",
											"inputLabel":                            "",
											"notificationSuccessMessage":            "Updated successfully",
											"notificationSuccessMessageDescription": "Labels have been updated",
											"editModalWidth":                        650,
											"maxEditTagTextLength":                  35,
											"paddingContainerEnd":                   "24px",
											"containerStyle": map[string]any{
												"marginTop": "-30px",
											},
											"selectProps": map[string]any{
												"maxTagTextLength": 35,
											},
										},
									},
								}),
								antdFlexVertical("meta-pod-selector-block", 4, []any{
									antdText("pod-selector", true, "Pod selector", map[string]any{
										"fontSize": 14,
									}),
									map[string]any{
										"type": "LabelsToSearchParams",
										"data": map[string]any{
											"id":               "pod-to-search-params",
											"jsonPathToLabels": ".spec.selector",
											"linkPrefix":       "/openapi-ui/{2}/search",
											"reqIndex":         0,
											"errorText":        "-",
										},
									},
								}),
								antdFlexVertical("ds-annotations", 4, []any{
									antdText("annotations", true, "Annotations", nil),
									map[string]any{
										"type": "Annotations",
										"data": map[string]any{
											"id":                                    "annotations",
											"endpoint":                              "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/services/{6}",
											"jsonPathToObj":                         ".metadata.annotations",
											"pathToValue":                           "/metadata/annotations",
											"reqIndex":                              0,
											"modalTitle":                            "Edit annotations",
											"modalDescriptionText":                  "",
											"inputLabel":                            "",
											"notificationSuccessMessage":            "Updated successfully",
											"notificationSuccessMessageDescription": "Annotations have been updated",
											"editModalWidth":                        "800px",
											"errorText":                             "0 Annotations",
											"text":                                  "~counter~ Annotations",
											"cols":                                  []any{11, 11, 2},
										},
									},
								}),
								antdFlexVertical("meta-session-affinity-block", 4, []any{
									antdText("meta-session-affinity-label", true, "Session affinity", nil),
									parsedText("meta-session-affinity-value", "{reqsJsonPath[0]['.spec.sessionAffinity']['Not configured']}", nil),
								}),
								antdFlexVertical("meta-created-block", 4, []any{
									antdText("time-label", true, "Created", nil),
									antdFlex("time-block", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":   "time-icon",
												"text": "🌐",
											},
										},
										map[string]any{
											"type": "parsedText",
											"data": map[string]any{
												"formatter": "timestamp",
												"id":        "time-value",
												"text":      "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}",
											},
										},
									}),
								}),
							}),
						}),
						antdCol("col-right", 12, []any{
							antdFlexVertical("col-right-stack", 24, []any{
								antdText("routing-title", true, "Service routing", map[string]any{
									"fontSize":     20,
									"marginBottom": "12px",
								}),
								spacer("routing-spacer", 16),
								antdFlexVertical("service-hostname-block", 4, []any{
									antdText("service-hostname-label", true, "Hostname", nil),
									parsedText("service-hostname-value", "{reqsJsonPath[0]['.metadata.name']['-']}.{reqsJsonPath[0]['.metadata.namespace']['-']}.svc.cluster.local", nil),
								}),
								antdFlexVertical("service-ip-block", 12, []any{
									antdFlexVertical("clusterip-block", 4, []any{
										antdText("clusterip-label", true, "ClusterIP address", nil),
										parsedText("clusterip-value", "{reqsJsonPath[0]['.spec.clusterIP']['-']}", nil),
									}),
									antdFlexVertical("loadbalancerip-block", 4, []any{
										antdText("loadbalancerip-label", true, "LoadBalancerIP address", nil),
										parsedText("loadbalancerip-value", "{reqsJsonPath[0]['.status.loadBalancer.ingress[0].ip']['Not Configured']}", nil),
									}),
								}),
								antdFlexVertical("service-port-mapping-block", 4, []any{
									antdText("service-port-mapping-label", true, "Service port mapping", nil),
									map[string]any{
										"type": "EnrichedTable",
										"data": map[string]any{
											"id":                   "service-port-mapping-table",
											"baseprefix":           "/openapi-ui",
											"clusterNamePartOfUrl": "{2}",
											"customizationId":      "factory-kube-service-details-port-mapping",
											"fetchUrl":             "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/services/{6}",
											"pathToItems":          ".spec.ports",
											"withoutControls":      true,
										},
									},
								}),
								map[string]any{
									"type": "VisibilityContainer",
									"data": map[string]any{
										"id":    "service-pod-serving-vis",
										"value": "{reqsJsonPath[0]['.spec.selector']['-']}",
										"style": map[string]any{
											"margin":  0,
											"padding": 0,
										},
									},
									"children": []any{
										antdFlexVertical("service-pod-serving-block", 4, []any{
											antdText("service-pod-serving-label", true, "Pod serving", nil),
											map[string]any{
												"type": "EnrichedTable",
												"data": map[string]any{
													"id":                   "service-pod-serving-table",
													"baseprefix":           "/openapi-ui",
													"clusterNamePartOfUrl": "{2}",
													"customizationId":      "factory-kube-service-details-endpointslice",
													"fetchUrl":             "/api/clusters/{2}/k8s/apis/discovery.k8s.io/v1/namespaces/{3}/endpointslices",
													"labelSelector": map[string]any{
														"kubernetes.io/service-name": "{reqsJsonPath[0]['.metadata.name']['-']}",
													},
													"pathToItems":     ".items[*].endpoints",
													"withoutControls": true,
												},
											},
										}),
									},
								},
							}),
						}),
					}),
				}),
			},
		},
		map[string]any{
			"key":   "yaml",
			"label": "YAML",
			"children": []any{
				map[string]any{
					"type": "YamlEditorSingleton",
					"data": map[string]any{
						"id":                        "yaml-editor",
						"cluster":                   "{2}",
						"isNameSpaced":              true,
						"prefillValuesRequestIndex": 0,
						"substractHeight":           float64(400),
						"type":                      "builtin",
						"typeName":                  "services",
					},
				},
			},
		},
		map[string]any{
			"key":   "pods",
			"label": "Pods",
			"children": []any{
				map[string]any{
					"type": "VisibilityContainer",
					"data": map[string]any{
						"id":    "service-pod-serving-vis",
						"value": "{reqsJsonPath[0]['.spec.selector']['-']}",
						"style": map[string]any{
							"margin":  0,
							"padding": 0,
						},
					},
					"children": []any{
						map[string]any{
							"type": "EnrichedTable",
							"data": map[string]any{
								"id":                   "pods-table",
								"baseprefix":           "/openapi-ui",
								"clusterNamePartOfUrl": "{2}",
								"customizationId":      "factory-node-details-/v1/pods",
								"fetchUrl":             "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/pods",
								"labelsSelectorFull": map[string]any{
									"pathToLabels": ".spec.selector",
									"reqIndex":     0,
								},
								"pathToItems":     ".items",
								"withoutControls": false,
							},
						},
					},
				},
			},
		},
	}
	serviceSpec := createFactorySpec("kube-service-details", []any{"service-sidebar"}, []any{"/api/clusters/{2}/k8s/api/v1/namespaces/{3}/services/{6}"}, serviceHeader, serviceTabs)

	// Ingress details factory
	ingressHeader := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   6,
			"style": map[string]any{
				"marginBottom": float64(24),
			},
		},
		"children": []any{
			map[string]any{
				"type": "antdText",
				"data": map[string]any{
					"id":    "badge-ingress",
					"text":  "I",
					"title": "ingresses",
					"style": map[string]any{
						"backgroundColor": "#2e7dff",
						"borderRadius":    "20px",
						"color":           "#fff",
						"display":         "inline-block",
						"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":        float64(20),
						"fontWeight":      float64(400),
						"lineHeight":      "24px",
						"minWidth":        float64(24),
						"padding":         "0 9px",
						"textAlign":       "center",
						"whiteSpace":      "nowrap",
					},
				},
			},
			map[string]any{
				"type": "parsedText",
				"data": map[string]any{
					"id":   "ingress-name",
					"text": "{reqsJsonPath[0]['.metadata.name']['-']}",
					"style": map[string]any{
						"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
						"fontSize":   float64(20),
						"lineHeight": "24px",
					},
				},
			},
		},
	}

	ingressTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				contentCard("details-card", map[string]any{
					"marginBottom": float64(24),
				}, []any{
					antdRow("details-grid", []any{48, 12}, []any{
						antdCol("col-left", 12, []any{
							antdFlexVertical("col-left-stack", 24, []any{
								antdFlexVertical("meta-name-block", 4, []any{
									antdText("meta-name-label", true, "Name", nil),
									parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
								}),
								antdFlexVertical("meta-namespace-block", 8, []any{
									antdText("meta-namespace-label", true, "Namespace", nil),
									map[string]any{
										"type": "antdFlex",
										"data": map[string]any{
											"id":    "namespace-row",
											"align": "center",
											"gap":   6,
										},
										"children": []any{
											createUnifiedBadgeFromKind("ns-badge", "Namespace"),
											antdLink("namespace-link",
												"{reqsJsonPath[0]['.metadata.namespace']['-']}",
												"/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
											),
										},
									},
								}),
								antdFlexVertical("meta-created-block", 4, []any{
									antdText("time-label", true, "Created", nil),
									antdFlex("time-block", 6, []any{
										antdText("time-icon", false, "🌐", nil),
										parsedTextWithFormatter("time-value", "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}", "timestamp"),
									}),
								}),
							}),
						}),
						antdCol("col-right", 12, []any{
							antdFlexVertical("col-right-stack", 24, []any{
								antdFlexVertical("status-ingress-ip", 4, []any{
									antdText("status-ingress-ip-label", true, "LoadBalancer IP", nil),
									parsedText("status-ingress-ip-value", "{reqsJsonPath[0]['.status.loadBalancer.ingress[0].ip']['-']}", nil),
								}),
								antdFlexVertical("status-ingress-hostname", 4, []any{
									antdText("status-ingress-hostname-label", true, "LoadBalancer Hostname", nil),
									parsedText("status-ingress-hostname-value", "{reqsJsonPath[0]['.status.loadBalancer.ingress[0].hostname']['-']}", nil),
								}),
							}),
						}),
					}),
					spacer("rules-title-spacer", float64(16)),
					antdText("rules-title", true, "Rules", map[string]any{
						"fontSize": float64(20),
					}),
					spacer("rules-spacer", float64(8)),
					map[string]any{
						"type": "EnrichedTable",
						"data": map[string]any{
							"id":                   "rules-table",
							"fetchUrl":             "/api/clusters/{2}/k8s/apis/networking.k8s.io/v1/namespaces/{3}/ingresses/{6}",
							"clusterNamePartOfUrl": "{2}",
							"customizationId":      "factory-kube-ingress-details-rules",
							"baseprefix":           "/openapi-ui",
							"withoutControls":      true,
							"pathToItems":          []any{"spec", "rules"},
						},
					},
				}),
			},
		},
		map[string]any{
			"key":   "yaml",
			"label": "YAML",
			"children": []any{
				map[string]any{
					"type": "YamlEditorSingleton",
					"data": map[string]any{
						"id":                        "yaml-editor",
						"cluster":                   "{2}",
						"isNameSpaced":              true,
						"type":                      "builtin",
						"typeName":                  "ingresses",
						"prefillValuesRequestIndex": float64(0),
						"substractHeight":           float64(400),
					},
				},
			},
		},
	}
	ingressSpec := createFactorySpec("kube-ingress-details", []any{"ingress-sidebar"}, []any{"/api/clusters/{2}/k8s/apis/networking.k8s.io/v1/namespaces/{3}/ingresses/{6}"}, ingressHeader, ingressTabs)

	// Workloadmonitor details factory
	workloadmonitorHeader := createWorkloadmonitorHeader()
	workloadmonitorTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				contentCard("details-card", map[string]any{
					"marginBottom": float64(24),
				}, []any{
					antdRow("details-grid", []any{48, 12}, []any{
						antdCol("col-left", 12, []any{
							antdFlexVertical("col-left-stack", 24, []any{
								antdFlexVertical("meta-name-block", 4, []any{
									antdText("meta-name-label", true, "Name", nil),
									parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
								}),
								antdFlexVertical("meta-namespace-block", 8, []any{
									antdText("meta-namespace-label", true, "Namespace", nil),
									antdFlex("namespace-row", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":   "ns-badge",
												"text": "NS",
												"style": map[string]any{
													"backgroundColor": "#a25792ff",
													"borderRadius":    "20px",
													"color":           "#fff",
													"display":         "inline-block",
													"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
													"fontSize":        15,
													"fontWeight":      400,
													"lineHeight":      "24px",
													"minWidth":        24,
													"padding":         "0 9px",
													"textAlign":       "center",
													"whiteSpace":      "nowrap",
												},
											},
										},
										map[string]any{
											"type": "antdLink",
											"data": map[string]any{
												"id":   "namespace-link",
												"text": "{reqsJsonPath[0]['.metadata.namespace']['-']}",
												"href": "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
											},
										},
									}),
								}),
								antdFlexVertical("meta-created-block", 4, []any{
									antdText("time-label", true, "Created", nil),
									antdFlex("time-block", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":   "time-icon",
												"text": "🌐",
											},
										},
										parsedTextWithFormatter("time-value", "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}", "timestamp"),
									}),
								}),
								antdFlexVertical("meta-kind-block", 4, []any{
									antdText("kind-label", true, "Kind", nil),
									parsedText("kind-value", "{reqsJsonPath[0]['.spec.kind']['-']}", nil),
								}),
								antdFlexVertical("meta-type-block", 4, []any{
									antdText("type-label", true, "Type", nil),
									parsedText("type-value", "{reqsJsonPath[0]['.spec.type']['-']}", nil),
								}),
							}),
						}),
						antdCol("col-right", 12, []any{
							antdFlexVertical("col-right-stack", 24, []any{
								antdText("params-title", true, "Parameters", map[string]any{
									"fontSize":     float64(20),
									"marginBottom": float64(12),
								}),
								antdFlexVertical("params-list", 24, []any{
									antdFlexVertical("param-version", 4, []any{
										antdText("param-version-label", true, "Version", nil),
										parsedText("param-version-value", "{reqsJsonPath[0]['.spec.version']['-']}", nil),
									}),
									antdFlexVertical("param-replicas", 4, []any{
										antdText("param-replicas-label", true, "Replicas", nil),
										parsedText("param-replicas-value", "{reqsJsonPath[0]['.spec.replicas']['-']}", nil),
									}),
									antdFlexVertical("param-minreplicas", 4, []any{
										antdText("param-minreplicas-label", true, "MinReplicas", nil),
										parsedText("param-minreplicas-value", "{reqsJsonPath[0]['.spec.minReplicas']['-']}", nil),
									}),
									antdFlexVertical("param-available", 4, []any{
										antdText("param-available-label", true, "AvailableReplicas", nil),
										parsedText("param-available-value", "{reqsJsonPath[0]['.status.availableReplicas']['-']}", nil),
									}),
									antdFlexVertical("param-observed", 4, []any{
										antdText("param-observed-label", true, "ObservedReplicas", nil),
										parsedText("param-observed-value", "{reqsJsonPath[0]['.status.observedReplicas']['-']}", nil),
									}),
									antdFlexVertical("param-operational", 4, []any{
										antdText("param-operational-label", true, "Operational", nil),
										parsedText("param-operational-value", "{reqsJsonPath[0]['.status.operational']['-']}", nil),
									}),
								}),
							}),
						}),
					}),
				}),
			},
		},
		map[string]any{
			"key":   "workloads",
			"label": "Workloads",
			"children": []any{
				map[string]any{
					"type": "EnrichedTable",
					"data": map[string]any{
						"id":                   "workloads-table",
						"baseprefix":           "/openapi-ui",
						"clusterNamePartOfUrl": "{2}",
						"customizationId":      "factory-details-v1alpha1.cozystack.io.workloads",
						"fetchUrl":             "/api/clusters/{2}/k8s/apis/cozystack.io/v1alpha1/namespaces/{3}/workloads",
						"labelSelector": map[string]any{
							"workloads.cozystack.io/monitor": "{reqs[0]['metadata','name']}",
						},
						"pathToItems": []any{"items"},
					},
				},
			},
		},
		map[string]any{
			"key":   "yaml",
			"label": "YAML",
			"children": []any{
				map[string]any{
					"type": "YamlEditorSingleton",
					"data": map[string]any{
						"id":                        "yaml-editor",
						"cluster":                   "{2}",
						"isNameSpaced":              true,
						"prefillValuesRequestIndex": 0,
						"substractHeight":           float64(400),
						"type":                      "builtin",
						"typeName":                  "workloadmonitors",
					},
				},
			},
		},
	}
	workloadmonitorSpec := createFactorySpec("workloadmonitor-details", []any{"workloadmonitor-sidebar"}, []any{"/api/clusters/{2}/k8s/apis/cozystack.io/v1alpha1/namespaces/{3}/workloadmonitors/{6}"}, workloadmonitorHeader, workloadmonitorTabs)

	// Plan details factory using unified approach
	planConfig := UnifiedResourceConfig{
		Name:         "plan-details",
		ResourceType: "factory",
		Kind:         "Plan",
		Plural:       "plans",
		Title:        "plan",
	}
	planTabs := []any{
		map[string]any{
			"key":   "details",
			"label": "Details",
			"children": []any{
				contentCard("details-card", map[string]any{
					"marginBottom": "24px",
				}, []any{
					antdText("details-title", true, "Plan details", map[string]any{
						"fontSize":     20,
						"marginBottom": "12px",
					}),
					spacer("details-spacer", 16),
					antdRow("details-grid", []any{48, 12}, []any{
						antdCol("col-left", 12, []any{
							antdFlexVertical("col-left-stack", 24, []any{
								antdFlexVertical("meta-name-block", 4, []any{
									antdText("meta-name-label", true, "Name", nil),
									parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
								}),
								antdFlexVertical("meta-namespace-block", 8, []any{
									antdText("meta-namespace-label", true, "Namespace", nil),
									antdFlex("header-row", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":    "header-badge",
												"text":  "NS",
												"title": "namespace",
												"style": map[string]any{
													"backgroundColor": "#a25792ff",
													"borderRadius":    "20px",
													"color":           "#fff",
													"display":         "inline-block",
													"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
													"fontSize":        "15px",
													"fontWeight":      400,
													"lineHeight":      "24px",
													"minWidth":        24,
													"padding":         "0 9px",
													"textAlign":       "center",
													"whiteSpace":      "nowrap",
												},
											},
										},
										map[string]any{
											"type": "antdLink",
											"data": map[string]any{
												"id":   "namespace-link",
												"text": "{reqsJsonPath[0]['.metadata.namespace']['-']}",
												"href": "/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
											},
										},
									}),
								}),
								antdFlexVertical("meta-created-block", 4, []any{
									antdText("time-label", true, "Created", nil),
									antdFlex("time-block", 6, []any{
										map[string]any{
											"type": "antdText",
											"data": map[string]any{
												"id":   "time-icon",
												"text": "🌐",
											},
										},
										map[string]any{
											"type": "parsedText",
											"data": map[string]any{
												"formatter": "timestamp",
												"id":        "time-value",
												"text":      "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}",
											},
										},
									}),
								}),
							}),
						}),
						antdCol("col-right", 12, []any{
							antdFlexVertical("col-right-stack", 24, []any{
								antdFlexVertical("spec-application-ref-block", 4, []any{
									antdText("application-ref-label", true, "Application", nil),
									parsedText("application-ref-value", "{reqsJsonPath[0]['.spec.applicationRef.kind']['-']}.{reqsJsonPath[0]['.spec.applicationRef.apiGroup']['-']}/{reqsJsonPath[0]['.spec.applicationRef.name']['-']}", nil),
								}),
								antdFlexVertical("spec-storage-ref-block", 4, []any{
									antdText("storage-ref-label", true, "Storage", nil),
									parsedText("storage-ref-value", "{reqsJsonPath[0]['.spec.storageRef.kind']['-']}.{reqsJsonPath[0]['.spec.storageRef.apiGroup']['-']}/{reqsJsonPath[0]['.spec.storageRef.name']['-']}", nil),
								}),
								antdFlexVertical("spec-strategy-ref-block", 4, []any{
									antdText("strategy-ref-label", true, "Strategy", nil),
									parsedText("strategy-ref-value", "{reqsJsonPath[0]['.spec.strategyRef.kind']['-']}.{reqsJsonPath[0]['.spec.strategyRef.apiGroup']['-']}/{reqsJsonPath[0]['.spec.strategyRef.name']['-']}", nil),
								}),
								antdFlexVertical("spec-schedule-type-block", 4, []any{
									antdText("schedule-type-label", true, "Schedule Type", nil),
									parsedText("schedule-type-value", "{reqsJsonPath[0]['.spec.schedule.type']['-']}", nil),
								}),
								antdFlexVertical("spec-schedule-cron-block", 4, []any{
									antdText("schedule-cron-label", true, "Schedule Cron", nil),
									parsedText("schedule-cron-value", "{reqsJsonPath[0]['.spec.schedule.cron']['-']}", nil),
								}),
							}),
						}),
					}),
				}),
			},
		},
	}
	planSpec := createUnifiedFactory(planConfig, planTabs, []any{"/api/clusters/{2}/k8s/apis/backups.cozystack.io/v1alpha1/namespaces/{3}/plans/{6}"})

	return []*dashboardv1alpha1.Factory{
		createFactory("marketplace", marketplaceSpec),
		createFactory("namespace-details", namespaceSpec),
		createFactory("node-details", nodeSpec),
		createFactory("pod-details", podSpec),
		createFactory("kube-secret-details", secretSpec),
		createFactory("kube-service-details", serviceSpec),
		createFactory("kube-ingress-details", ingressSpec),
		createFactory("workloadmonitor-details", workloadmonitorSpec),
		createFactory("plan-details", planSpec),
	}
}

// CreateAllNavigations creates all navigation resources using helper functions
func CreateAllNavigations() []*dashboardv1alpha1.Navigation {
	return []*dashboardv1alpha1.Navigation{
		createNavigation("navigation", map[string]any{
			"namespaces": map[string]any{
				"change": "/openapi-ui/{selectedCluster}/{value}/factory/marketplace",
				"clear":  "/openapi-ui/{selectedCluster}/api-table/core.cozystack.io/v1alpha1/tenantnamespaces",
			},
		}),
	}
}

// CreateAllTableUriMappings creates all table URI mapping resources using helper functions
func CreateAllTableUriMappings() []*dashboardv1alpha1.TableUriMapping {
	// links are now handled through CustomFormsPrefills
	return []*dashboardv1alpha1.TableUriMapping{}
}

// ---------------- Additional helper functions for missing resource types ----------------

// createCustomFormsOverride creates a CustomFormsOverride resource
func createCustomFormsOverride(customizationId string, spec map[string]any) *dashboardv1alpha1.CustomFormsOverride {
	// Generate name from customizationId
	name := customizationId
	if strings.Contains(customizationId, "default-/") {
		// For default-/ resources, replace "default-/" with "default-" and slashes with dots
		name = strings.ReplaceAll(customizationId, "default-/", "default-")
		name = strings.ReplaceAll(name, "/", ".")
	}

	// Create hidden fields list
	hidden := []any{
		[]any{"metadata", "creationTimestamp"},
	}

	// Add namespace to hidden for specific resources in the correct order
	if strings.Contains(name, "namespaces") || strings.Contains(name, "nodes") {
		hidden = append(hidden, []any{"metadata", "namespace"})
	}

	// Add remaining hidden fields
	hidden = append(hidden, []any{
		[]any{"metadata", "deletionGracePeriodSeconds"},
		[]any{"metadata", "deletionTimestamp"},
		[]any{"metadata", "finalizers"},
		[]any{"metadata", "generateName"},
		[]any{"metadata", "generation"},
		[]any{"metadata", "managedFields"},
		[]any{"metadata", "ownerReferences"},
		[]any{"metadata", "resourceVersion"},
		[]any{"metadata", "selfLink"},
		[]any{"metadata", "uid"},
		[]any{"kind"},
		[]any{"apiVersion"},
		[]any{"status"},
	}...)

	// Create new spec with all required fields including formItems from the original spec
	newSpec := map[string]any{
		"customizationId": customizationId,
		"hidden":          hidden,
		"schema":          map[string]any{},
		"strategy":        "merge",
	}

	// Merge caller-provided fields (like formItems) into newSpec
	for key, value := range spec {
		if key != "customizationId" && key != "hidden" && key != "schema" && key != "strategy" {
			newSpec[key] = value
		}
	}

	jsonData, _ := json.Marshal(newSpec)

	return &dashboardv1alpha1.CustomFormsOverride{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "CustomFormsOverride",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashboardv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// createNavigation creates a Navigation resource
func createNavigation(name string, spec map[string]any) *dashboardv1alpha1.Navigation {
	jsonData, _ := json.Marshal(spec)

	return &dashboardv1alpha1.Navigation{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "Navigation",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashboardv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// createFormItem creates a form item for CustomFormsOverride
func createFormItem(path, label, fieldType string) map[string]any {
	return map[string]any{
		"path":  path,
		"label": label,
		"type":  fieldType,
	}
}

// ---------------- Workloadmonitor specific functions ----------------

// createNamespaceHeader creates a header specifically for namespace with correct colors and text
func createNamespaceHeader() map[string]any {
	badge := map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    "header-badge",
			"text":  "NS",
			"title": "Namespace",
			"style": map[string]any{
				"backgroundColor": "#a25792ff",
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        "20px",
				"fontWeight":      float64(400),
				"lineHeight":      "24px",
				"minWidth":        float64(24),
				"padding":         "0 9px",
				"textAlign":       "center",
				"whiteSpace":      "nowrap",
			},
		},
	}

	nameText := parsedText("header-name", "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontSize":   "20px",
		"lineHeight": "24px",
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
	})

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": "24px",
			},
		},
		"children": []any{
			badge,
			nameText,
		},
	}
}

// createNodeHeader creates a header specifically for node with correct colors and text
func createNodeHeader() map[string]any {
	badge := map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    "header-badge",
			"text":  "N",
			"title": "nodes",
			"style": map[string]any{
				"backgroundColor": "#8476d1",
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        "20px",
				"fontWeight":      float64(400),
				"lineHeight":      "24px",
				"minWidth":        float64(24),
				"padding":         "0 9px",
				"textAlign":       "center",
				"whiteSpace":      "nowrap",
			},
		},
	}

	nameText := parsedText("header-name", "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontSize":   "20px",
		"lineHeight": "24px",
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
	})

	statusBlock := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":       "status-header-block",
			"vertical": true,
			"gap":      float64(4),
		},
		"children": []any{
			map[string]any{
				"type": "StatusText",
				"data": map[string]any{
					"id": "node-status",
					"values": []any{
						"{reqsJsonPath[0]['.status.conditions[?(@.status=='True')].reason']['-']}",
					},
					"criteriaSuccess":       "equals",
					"strategySuccess":       "every",
					"valueToCompareSuccess": "KubeletReady",
					"criteriaError":         "equals",
					"strategyError":         "every",
					"valueToCompareError": []any{
						"KernelDeadlock",
						"ReadonlyFilesystem",
						"NetworkUnavailable",
						"MemoryPressure",
						"DiskPressure",
						"PIDPressure",
					},
					"successText":  "Available",
					"errorText":    "Unavailable",
					"fallbackText": "Progressing",
				},
			},
		},
	}

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": "24px",
			},
		},
		"children": []any{
			badge,
			nameText,
			statusBlock,
		},
	}
}

// createPodHeader creates a header specifically for pod with correct colors and text
func createPodHeader() map[string]any {
	badge := map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    "header-badge",
			"text":  "P",
			"title": "Pods",
			"style": map[string]any{
				"backgroundColor": "#009596",
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        "20px",
				"fontWeight":      float64(400),
				"lineHeight":      "24px",
				"minWidth":        float64(24),
				"padding":         "0 9px",
				"textAlign":       "center",
				"whiteSpace":      "nowrap",
			},
		},
	}

	nameText := parsedText("header-pod-name", "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontSize":   "20px",
		"lineHeight": "24px",
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
	})

	statusBlock := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":       "status-header-block",
			"vertical": true,
			"gap":      float64(4),
		},
		"children": []any{
			map[string]any{
				"type": "StatusText",
				"data": map[string]any{
					"id": "pod-status",
				},
			},
		},
	}

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": "24px",
			},
		},
		"children": []any{
			badge,
			nameText,
			statusBlock,
		},
	}
}

// createWorkloadmonitorHeader creates a header specifically for workloadmonitor with correct colors and text
func createWorkloadmonitorHeader() map[string]any {
	badge := map[string]any{
		"type": "antdText",
		"data": map[string]any{
			"id":    "badge-workloadmonitor",
			"text":  "W",
			"title": "workloadmonitors",
			"style": map[string]any{
				"backgroundColor": "#c46100",
				"borderRadius":    "20px",
				"color":           "#fff",
				"display":         "inline-block",
				"fontFamily":      "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
				"fontSize":        float64(20),
				"fontWeight":      float64(400),
				"lineHeight":      "24px",
				"minWidth":        float64(24),
				"padding":         "0 9px",
				"textAlign":       "center",
				"whiteSpace":      "nowrap",
			},
		},
	}

	nameText := parsedText("workloadmonitor-name", "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
		"fontSize":   float64(20),
		"lineHeight": "24px",
	})

	return map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":    "header-row",
			"align": "center",
			"gap":   float64(6),
			"style": map[string]any{
				"marginBottom": float64(24),
			},
		},
		"children": []any{
			badge,
			nameText,
		},
	}
}

// ---------------- Complete resource creation function ----------------

// CreateAllStaticResources creates all static dashboard resources using helper functions
func CreateAllStaticResources() []client.Object {
	var resources []client.Object

	// Add all breadcrumbs
	for _, breadcrumb := range CreateAllBreadcrumbs() {
		resources = append(resources, breadcrumb)
	}

	// Add all custom column overrides
	for _, customColumns := range CreateAllCustomColumnsOverrides() {
		resources = append(resources, customColumns)
	}

	// Add all custom forms overrides
	for _, customForms := range CreateAllCustomFormsOverrides() {
		resources = append(resources, customForms)
	}

	// Add all factories
	for _, factory := range CreateAllFactories() {
		resources = append(resources, factory)
	}

	// Add all navigations
	for _, navigation := range CreateAllNavigations() {
		resources = append(resources, navigation)
	}

	// Add all table URI mappings
	for _, tableUriMapping := range CreateAllTableUriMappings() {
		resources = append(resources, tableUriMapping)
	}

	return resources
}
