package dashboard

import (
	"encoding/json"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------- Static resource helpers ----------------

// createBreadcrumb creates a Breadcrumb resource with the given name and breadcrumb items
func createBreadcrumb(name string, breadcrumbItems []map[string]any) *dashv1alpha1.Breadcrumb {
	// Generate spec.id from name
	specID := generateSpecID(name)

	data := map[string]any{
		"breadcrumbItems": breadcrumbItems,
		"id":              specID,
	}
	jsonData, _ := json.Marshal(data)

	return &dashv1alpha1.Breadcrumb{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "Breadcrumb",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// createCustomColumnsOverride creates a CustomColumnsOverride resource
func createCustomColumnsOverride(id string, additionalPrinterColumns []any) *dashv1alpha1.CustomColumnsOverride {
	// Generate metadata.name from spec.id
	name := generateMetadataName(id)

	data := map[string]any{
		"additionalPrinterColumns": additionalPrinterColumns,
	}

	// Add ID field for resources that should have it
	shouldHaveID := true
	if name == "stock-cluster-.v1.nodes" ||
		name == "stock-cluster-.v1.pods" ||
		name == "stock-namespace-.v1.pods" ||
		name == "factory-node-details-v1.pods" ||
		name == "factory-v1.pods" {
		shouldHaveID = false
	}

	// ID will be set later for specific resources, so don't set it here for pod/node resources
	if shouldHaveID && !strings.Contains(name, "pods") && !strings.Contains(name, "nodes") {
		data["id"] = id
	}

	// Add additional fields for specific resources
	if name == "factory-details-v1.services" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "ClusterIP",
				"value": "-",
			},
			map[string]any{
				"key":   "LoadbalancerIP",
				"value": "-",
			},
		}
	}

	if name == "cluster-v1.configmaps" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Namespace",
				"value": "-",
			},
		}
	}

	if name == "stock-cluster-v1.configmaps" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Namespace",
				"value": "-",
			},
		}
	}

	if name == "stock-namespace-v1.configmaps" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
	}

	if name == "factory-details-v1alpha1.core.cozystack.io.tenantsecrets" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Namespace",
				"value": "-",
			},
		}
	}

	if name == "factory-details-v1alpha1.cozystack.io.workloads" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Name",
				"value": "-",
			},
			map[string]any{
				"key":   "Kind",
				"value": "-",
			},
			map[string]any{
				"key":   "Type",
				"value": "-",
			},
			map[string]any{
				"key":   "CPU",
				"value": "-",
			},
			map[string]any{
				"key":   "Memory",
				"value": "-",
			},
			map[string]any{
				"key":   "Operational",
				"value": "-",
			},
		}
	}

	if name == "factory-kube-ingress-details-rules" {
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Service",
				"value": "-",
			},
			map[string]any{
				"key":   "Port",
				"value": "-",
			},
			map[string]any{
				"key":   "Path",
				"value": "-",
			},
		}
	}

	if name == "container-spec-containers-list" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Image",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Name",
				"value": "-",
			},
			map[string]any{
				"key":   "Image",
				"value": "-",
			},
			map[string]any{
				"key":   "Resources limits",
				"value": "-",
			},
			map[string]any{
				"key":   "Resources requests",
				"value": "-",
			},
			map[string]any{
				"key":   "Ports",
				"value": "-",
			},
		}
	}

	if name == "container-spec-init-containers-list" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Image",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Name",
				"value": "-",
			},
			map[string]any{
				"key":   "Image",
				"value": "-",
			},
			map[string]any{
				"key":   "Resources limits",
				"value": "-",
			},
			map[string]any{
				"key":   "Resources requests",
				"value": "-",
			},
		}
	}

	if name == "container-status-containers-list" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(63),
			},
			map[string]any{
				"key":   "Image",
				"value": float64(63),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "TerminatedReason",
				"value": "-",
			},
			map[string]any{
				"key":   "WaitingReason",
				"value": "-",
			},
		}
	}

	if name == "container-status-init-containers-list" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(63),
			},
			map[string]any{
				"key":   "Image",
				"value": float64(63),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "TerminatedReason",
				"value": "-",
			},
			map[string]any{
				"key":   "WaitingReason",
				"value": "-",
			},
		}
	}

	if name == "factory-details-networking.k8s.io.v1.ingresses" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Hosts",
				"value": "-",
			},
			map[string]any{
				"key":   "Address",
				"value": "-",
			},
			map[string]any{
				"key":   "Port",
				"value": "-",
			},
		}
	}

	if name == "stock-namespace-networking.k8s.io.v1.ingresses" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Hosts",
				"value": "-",
			},
			map[string]any{
				"key":   "Address",
				"value": "-",
			},
			map[string]any{
				"key":   "Port",
				"value": "-",
			},
		}
	}

	if name == "factory-node-images" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "ImageID",
				"value": float64(128),
			},
			map[string]any{
				"key":   "Size",
				"value": float64(63),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Message",
				"value": "-",
			},
		}
	}

	if name == "factory-status-conditions" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Message",
				"value": float64(63),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Reason",
				"value": "-",
			},
			map[string]any{
				"key":   "Message",
				"value": "-",
			},
		}
	}

	if name == "stock-cluster-v1.secrets" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
	}

	if name == "stock-namespace-networking.k8s.io.v1.ingresses" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Hosts",
				"value": "-",
			},
			map[string]any{
				"key":   "Address",
				"value": "-",
			},
			map[string]any{
				"key":   "Port",
				"value": "-",
			},
		}
	}

	if name == "stock-namespace-v1.configmaps" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
	}

	if name == "stock-namespace-v1.secrets" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "Namespace",
				"value": "-",
			},
		}
	}

	if name == "stock-namespace-v1.services" {
		data["additionalPrinterColumnsTrimLengths"] = []any{
			map[string]any{
				"key":   "Name",
				"value": float64(64),
			},
		}
		data["additionalPrinterColumnsUndefinedValues"] = []any{
			map[string]any{
				"key":   "ClusterIP",
				"value": "-",
			},
			map[string]any{
				"key":   "LoadbalancerIP",
				"value": "-",
			},
		}
	}

	jsonData, _ := json.Marshal(data)

	return &dashv1alpha1.CustomColumnsOverride{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "CustomColumnsOverride",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// createFactory creates a Factory resource
func createFactory(name string, spec map[string]any) *dashv1alpha1.Factory {
	jsonData, _ := json.Marshal(spec)

	return &dashv1alpha1.Factory{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "Factory",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// createTableUriMapping creates a TableUriMapping resource
func createTableUriMapping(name string, spec map[string]any) *dashv1alpha1.TableUriMapping {
	jsonData, _ := json.Marshal(spec)

	return &dashv1alpha1.TableUriMapping{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dashboard.cozystack.io/v1alpha1",
			Kind:       "TableUriMapping",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "",
		},
		Spec: dashv1alpha1.ArbitrarySpec{
			JSON: v1.JSON{
				Raw: jsonData,
			},
		},
	}
}

// ---------------- Breadcrumb item helpers ----------------

// createBreadcrumbItem creates a breadcrumb item with key, label, and optional link
func createBreadcrumbItem(key, label string, link ...string) map[string]any {
	item := map[string]any{
		"key":   key,
		"label": label,
	}
	if len(link) > 0 && link[0] != "" {
		item["link"] = link[0]
	}
	return item
}

// ---------------- Custom column helpers ----------------

// createCustomColumn creates a custom column with factory type and badge
func createCustomColumn(name, kind, plural, href string) map[string]any {
	link := antdLink("name-link", "{reqsJsonPath[0]['.metadata.name']['-']}", href)

	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"children": []any{
						map[string]any{
							"type": "ResourceBadge",
							"data": map[string]any{
								"id":    "header-badge",
								"value": kind,
								// abbreviation auto-generated by ResourceBadge from value
							},
						},
						link,
					},
					"type": "antdFlex",
					"data": map[string]any{
						"align": "center",
						"gap":   float64(6),
					},
				},
			},
		},
	}
}

// createCustomColumnWithBadge creates a custom column with a specific badge
// badgeValue should be the kind in PascalCase (e.g., "Service", "Pod")
// abbreviation is auto-generated by ResourceBadge from badgeValue
func createCustomColumnWithBadge(name, badgeValue, href string) map[string]any {
	link := antdLink("name-link", "{reqsJsonPath[0]['.metadata.name']['-']}", href)

	badgeData := map[string]any{
		"id":    "header-badge",
		"value": badgeValue,
	}

	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"children": []any{
						map[string]any{
							"type": "ResourceBadge",
							"data": badgeData,
						},
						link,
					},
					"type": "antdFlex",
					"data": map[string]any{
						"align": "center",
						"gap":   float64(6),
					},
				},
			},
		},
	}
}

// createCustomColumnWithSpecificColor creates a custom column with a specific kind and optional color
// badgeValue should be the kind in PascalCase (e.g., "Service", "Pod")
func createCustomColumnWithSpecificColor(name, kind, color, href string) map[string]any {
	link := antdLink("name-link", "{reqsJsonPath[0]['.metadata.name']['-']}", href)

	badgeData := map[string]any{
		"id":    "header-badge",
		"value": kind,
	}
	// Add custom color if specified
	if color != "" {
		badgeData["style"] = map[string]any{
			"backgroundColor": color,
		}
	}

	return map[string]any{
		"name":     name,
		"type":     "factory",
		"jsonPath": ".metadata.name",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"children": []any{
						map[string]any{
							"type": "ResourceBadge",
							"data": badgeData,
						},
						link,
					},
					"type": "antdFlex",
					"data": map[string]any{
						"align": "center",
						"gap":   float64(6),
						"id":    "header-row",
					},
				},
			},
		},
	}
}

// createStringColumn creates a simple string column
func createStringColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "string",
		"jsonPath": jsonPath,
	}
}

// createTimestampColumn creates a timestamp column with custom formatting
func createTimestampColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "factory",
		"jsonPath": jsonPath,
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"children": []any{
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
								"text":      "{reqsJsonPath[0]['" + jsonPath + "']['-']}",
							},
						},
					},
					"type": "antdFlex",
					"data": map[string]any{
						"align": "center",
						"gap":   float64(6),
						"id":    "time-block",
					},
				},
			},
		},
	}
}

// ---------------- Factory helpers ----------------

// createFactoryHeader creates a header for factory resources
func createFactoryHeader(kind, plural string) map[string]any {
	lowerKind := strings.ToLower(kind)
	badge := createUnifiedBadgeFromKind("badge-"+lowerKind, kind)
	nameText := parsedText(lowerKind+"-name", "{reqsJsonPath[0]['.metadata.name']['-']}", map[string]any{
		"fontFamily": "RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif",
		"fontSize":   float64(20),
		"lineHeight": "24px",
	})

	return antdFlex("header-row", float64(6), []any{
		badge,
		nameText,
	})
}

// getTabsId returns the appropriate tabs ID for a given key
func getTabsId(key string) string {
	// Special cases
	if key == "workloadmonitor-details" {
		return "workloadmonitor-tabs"
	}
	if key == "kube-secret-details" {
		return "secret-tabs"
	}
	if key == "kube-service-details" {
		return "service-tabs"
	}
	return strings.ToLower(key) + "-tabs"
}

// createFactorySpec creates a factory spec with header and tabs
func createFactorySpec(key string, sidebarTags []any, urlsToFetch []any, header map[string]any, tabs []any) map[string]any {
	return map[string]any{
		"key":                           key,
		"sidebarTags":                   sidebarTags,
		"withScrollableMainContentCard": true,
		"urlsToFetch":                   urlsToFetch,
		"data": []any{
			header,
			map[string]any{
				"type": "antdTabs",
				"data": map[string]any{
					"id":               getTabsId(key),
					"defaultActiveKey": "details",
					"items":            tabs,
				},
			},
		},
	}
}

// createCustomColumnWithJsonPath creates a column with a custom badge and link using jsonPath
// badgeValue should be the kind in PascalCase (e.g., "Service", "VirtualMachine")
// abbreviation is auto-generated by ResourceBadge from badgeValue
func createCustomColumnWithJsonPath(name, jsonPath, badgeValue, badgeColor, linkHref string) map[string]any {
	// Determine link ID based on jsonPath
	linkId := "name-link"
	if jsonPath == ".metadata.namespace" {
		linkId = "namespace-link"
	}

	badgeData := map[string]any{
		"id":    "header-badge",
		"value": badgeValue,
	}
	// Add custom color if specified
	if badgeColor != "" {
		badgeData["style"] = map[string]any{
			"backgroundColor": badgeColor,
		}
	}

	return map[string]any{
		"name":     name,
		"type":     "factory",
		"jsonPath": jsonPath,
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "antdFlex",
					"data": map[string]any{
						"id":    "header-row",
						"align": "center",
						"gap":   6,
					},
					"children": []any{
						map[string]any{
							"type": "ResourceBadge",
							"data": badgeData,
						},
						map[string]any{
							"type": "antdLink",
							"data": map[string]any{
								"id":   linkId,
								"text": "{reqsJsonPath[0]['" + jsonPath + "']['-']}",
								"href": linkHref,
							},
						},
					},
				},
			},
		},
	}
}

// createCustomColumnWithoutJsonPath creates a column with a custom badge and link without jsonPath
// badgeValue should be the kind in PascalCase (e.g., "Node", "Pod")
// abbreviation is auto-generated by ResourceBadge from badgeValue
func createCustomColumnWithoutJsonPath(name, badgeValue, badgeColor, linkHref string) map[string]any {
	badgeData := map[string]any{
		"id":    "header-badge",
		"value": badgeValue,
	}
	// Add custom color if specified
	if badgeColor != "" {
		badgeData["style"] = map[string]any{
			"backgroundColor": badgeColor,
		}
	}

	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "antdFlex",
					"data": map[string]any{
						"id":    "header-row",
						"align": "center",
						"gap":   6,
					},
					"children": []any{
						map[string]any{
							"type": "ResourceBadge",
							"data": badgeData,
						},
						map[string]any{
							"type": "antdLink",
							"data": map[string]any{
								"id":   "name-link",
								"text": "{reqsJsonPath[0]['.spec.nodeName']['-']}",
								"href": linkHref,
							},
						},
					},
				},
			},
		},
	}
}

// createStatusColumn creates a status column with StatusText component
func createStatusColumn(name, statusId string) map[string]any {
	var statusData map[string]any

	if statusId == "pod-status" {
		statusData = map[string]any{
			"id":              statusId,
			"criteriaError":   "equals",
			"criteriaSuccess": "notEquals",
			"errorText":       "Error",
			"fallbackText":    "Progressing",
			"strategySuccess": "every",
			"strategyError":   "every",
			"successText":     "{reqsJsonPath[0]['.status.phase']['-']}",
			"valueToCompareError": []any{
				"Failed",
				"Unknown",
				"Evicted",
				"NodeLost",
				"UnexpectedAdmissionError",
				"SchedulerError",
				"FailedScheduling",
				"CrashLoopBackOff",
				"ImagePullBackOff",
				"ErrImagePull",
				"ErrImageNeverPull",
				"InvalidImageName",
				"ImageInspectError",
				"CreateContainerConfigError",
				"CreateContainerError",
				"RunContainerError",
				"StartError",
				"PostStartHookError",
				"ContainerCannotRun",
				"OOMKilled",
				"Error",
				"DeadlineExceeded",
				"CreatePodSandboxError",
			},
			"valueToCompareSuccess": []any{
				"Preempted",
				"Shutdown",
				"NodeShutdown",
				"DisruptionTarget",
				"Unschedulable",
				"SchedulingGated",
				"ContainersNotReady",
				"ContainersNotInitialized",
				"BackOff",
				"PreStopHookError",
				"KillError",
				"ContainerStatusUnknown",
			},
			"values": []any{
				"{reqsJsonPath[0]['.status.initContainerStatuses[*].state.waiting.reason']['-']}",
				"{reqsJsonPath[0]['.status.initContainerStatuses[*].state.terminated.reason']['-']}",
				"{reqsJsonPath[0]['.status.initContainerStatuses[*].lastState.terminated.reason']['-']}",
				"{reqsJsonPath[0]['.status.containerStatuses[*].state.waiting.reason']['-']}",
				"{reqsJsonPath[0]['.status.containerStatuses[*].state.terminated.reason']['-']}",
				"{reqsJsonPath[0]['.status.containerStatuses[*].lastState.terminated.reason']['-']}",
				"{reqsJsonPath[0]['.status.phase']['-']}",
				"{reqsJsonPath[0]['.status.reason']['-']}",
				"{reqsJsonPath[0]['.status.conditions[*].reason']['-']}",
			},
		}
	} else if statusId == "node-status" {
		statusData = map[string]any{
			"id":              statusId,
			"criteriaError":   "equals",
			"criteriaSuccess": "equals",
			"errorText":       "Unavailable",
			"fallbackText":    "Progressing",
			"strategySuccess": "every",
			"strategyError":   "every",
			"successText":     "Available",
			"valueToCompareError": []any{
				"KernelDeadlock",
				"ReadonlyFilesystem",
				"NetworkUnavailable",
				"MemoryPressure",
				"DiskPressure",
				"PIDPressure",
			},
			"valueToCompareSuccess": "KubeletReady",
			"values": []any{
				"{reqsJsonPath[0]['.status.conditions[?(@.status=='True')].reason']['-']}",
			},
		}
	} else {
		// Default status data for other status types
		statusData = map[string]any{
			"id": statusId,
		}
	}

	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "StatusText",
					"data": statusData,
				},
			},
		},
	}
}

// createSimpleStatusColumn creates a simple status column with basic StatusText component
func createSimpleStatusColumn(name, statusId string) map[string]any {
	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "StatusText",
					"data": map[string]any{
						"id": statusId,
					},
				},
			},
		},
	}
}

// createSecretBase64Column creates a column with SecretBase64Plain component
func createSecretBase64Column(name, jsonPath string) map[string]any {
	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "SecretBase64Plain",
					"data": map[string]any{
						"id":             "example-secretbase64",
						"plainTextValue": "hello",
						"base64Value":    "{reqsJsonPath[0]['" + jsonPath + "']['']}",
					},
				},
			},
		},
	}
}

// createArrayColumn creates a column with array type
func createArrayColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "array",
		"jsonPath": jsonPath,
	}
}

// createBoolColumn creates a column with boolean type
func createBoolColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "bool",
		"jsonPath": jsonPath,
	}
}

// createReadyColumn creates a Ready column with Boolean type and condition check
func createReadyColumn() map[string]any {
	return map[string]any{
		"name":     "Ready",
		"type":     "Boolean",
		"jsonPath": `.status.conditions[?(@.type=="Ready")].status`,
	}
}

// createApplicationRefColumn creates a column that displays
// applicationRef in the format "Kind.apiGroup/name"
func createApplicationRefColumn(name string) map[string]any {
	return map[string]any{
		"name": name,
		"type": "factory",
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "parsedText",
					"data": map[string]any{
						"id":   "application-ref-text",
						"text": "{reqsJsonPath[0]['.spec.applicationRef.kind']['-']}.{reqsJsonPath[0]['.spec.applicationRef.apiGroup']['-']}/{reqsJsonPath[0]['.spec.applicationRef.name']['-']}",
					},
				},
			},
		},
	}
}

// createConverterBytesColumn creates a column with ConverterBytes component
func createConverterBytesColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "factory",
		"jsonPath": jsonPath,
		"customProps": map[string]any{
			"disableEventBubbling": true,
			"items": []any{
				map[string]any{
					"type": "ConverterBytes",
					"data": map[string]any{
						"id":         "example-converter-bytes",
						"bytesValue": "{reqsJsonPath[0]['" + jsonPath + "']['-']}",
						"format":     true,
						"precision":  float64(1),
					},
				},
			},
		},
	}
}

// createFlatMapColumn creates a flatMap column that expands a map into separate rows
func createFlatMapColumn(name, jsonPath string) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "flatMap",
		"jsonPath": jsonPath,
	}
}

// ---------------- Factory UI helper functions ----------------

// labelsEditor creates a Labels editor component
func labelsEditor(id, endpoint string, reqIndex int) map[string]any {
	return map[string]any{
		"type": "Labels",
		"data": map[string]any{
			"id":                                    id,
			"endpoint":                              endpoint,
			"reqIndex":                              reqIndex,
			"jsonPathToLabels":                      ".metadata.labels",
			"pathToValue":                           "/metadata/labels",
			"modalTitle":                            "Edit labels",
			"modalDescriptionText":                  "",
			"inputLabel":                            "",
			"notificationSuccessMessage":            "Updated successfully",
			"notificationSuccessMessageDescription": "Labels have been updated",
			"editModalWidth":                        650,
			"maxEditTagTextLength":                  35,
			"paddingContainerEnd":                   "24px",
			"containerStyle":                        map[string]any{"marginTop": -30},
			"selectProps":                           map[string]any{"maxTagTextLength": 35},
		},
	}
}

// annotationsEditor creates an Annotations editor component
func annotationsEditor(id, endpoint string, reqIndex int) map[string]any {
	return map[string]any{
		"type": "Annotations",
		"data": map[string]any{
			"id":                                    id,
			"endpoint":                              endpoint,
			"reqIndex":                              reqIndex,
			"jsonPathToObj":                         ".metadata.annotations",
			"pathToValue":                           "/metadata/annotations",
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
	}
}

// yamlEditor creates a YamlEditorSingleton component
func yamlEditor(id, cluster string, isNameSpaced bool, typeName string, prefillValuesRequestIndex int) map[string]any {
	return map[string]any{
		"type": "YamlEditorSingleton",
		"data": map[string]any{
			"id":                        id,
			"cluster":                   cluster,
			"isNameSpaced":              isNameSpaced,
			"type":                      "builtin",
			"typeName":                  typeName,
			"prefillValuesRequestIndex": prefillValuesRequestIndex,
			"substractHeight":           float64(400),
		},
	}
}
