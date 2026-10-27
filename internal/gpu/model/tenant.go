/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package model

import (
	"strings"

	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	rootTenantNamespace   = "tenant-root"
	tenantNamespacePrefix = "tenant-"
)

// ParentNamespace returns the namespace of the parent of the tenant that
// owns ns, or "" for the root tenant and non-tenant namespaces. The tenant
// chart encodes the hierarchy in the name (a child x of namespace P owns
// P-x, or tenant-x directly under root), so the parent is ns without its
// last "-" segment.
func ParentNamespace(ns string) string {
	if ns == rootTenantNamespace || !strings.HasPrefix(ns, tenantNamespacePrefix) {
		return ""
	}
	segments := strings.Split(ns, "-")
	if len(segments) == 2 {
		return rootTenantNamespace
	}
	return strings.Join(segments[:len(segments)-1], "-")
}

// Ancestors returns ns followed by each ancestor tenant namespace up to
// the root.
func Ancestors(ns string) []string {
	var out []string
	for cur := ns; cur != ""; cur = ParentNamespace(cur) {
		out = append(out, cur)
	}
	return out
}

// InSubtree reports whether ns is root or a descendant of it.
func InSubtree(ns, root string) bool {
	for cur := ns; cur != ""; cur = ParentNamespace(cur) {
		if cur == root {
			return true
		}
	}
	return false
}

// Entitled reports whether a tenant namespace's labels grant vGPU. The
// tenant chart writes the effective value, parent included, so the label
// alone decides.
func Entitled(namespaceLabels map[string]string) bool {
	return namespaceLabels[gpu.LabelNamespaceVGPU] == gpu.LabelValueTrue
}

// TenantReleaseRef returns the name and namespace of the HelmRelease of
// the tenant that owns ns, the inverse of the tenant namespace naming in
// cozystack-api: tenant-root is its own, tenant-foo lives in tenant-root
// as tenant-foo, and tenant-foo-bar lives in tenant-foo as tenant-bar.
func TenantReleaseRef(ns string) (name, namespace string, ok bool) {
	parent := ParentNamespace(ns)
	switch {
	case ns == rootTenantNamespace:
		return rootTenantNamespace, rootTenantNamespace, true
	case parent == "":
		return "", "", false
	}
	return tenantNamespacePrefix + ns[strings.LastIndex(ns, "-")+1:], parent, true
}
