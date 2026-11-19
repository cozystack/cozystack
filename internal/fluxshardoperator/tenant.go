package fluxshardoperator

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TenantNamespaceForHR derives the owning tenant's namespace from HelmRelease
// metadata alone. The tenant namespace is the unit of placement: all
// HelmReleases resolving to the same tenant namespace carry the same shard
// label.
//
// Rules (mirroring the tenant chart's "tenant.name" helper):
//   - A parent tenant HelmRelease (label apps.cozystack.io/application.kind=Tenant,
//     name "tenant-<x>") released in tenant-root owns namespace "tenant-<x>";
//     released in any other tenant namespace it owns "<release-namespace>-<x>"
//     (nested tenants).
//   - Any other HelmRelease in a "tenant-*" namespace belongs to the tenant of
//     that namespace.
//
// ok=false means the object cannot be attributed to a tenant.
func TenantNamespaceForHR(obj metav1.Object) (string, bool) {
	name, namespace := obj.GetName(), obj.GetNamespace()
	if !strings.HasPrefix(namespace, "tenant-") {
		return "", false
	}
	if obj.GetLabels()[ApplicationKindLabel] == TenantKind {
		if suffix, found := strings.CutPrefix(name, "tenant-"); found && suffix != "" {
			if namespace == "tenant-root" {
				// Includes the root tenant itself: tenant-root/tenant-root.
				return name, true
			}
			return namespace + "-" + suffix, true
		}
	}
	return namespace, true
}
