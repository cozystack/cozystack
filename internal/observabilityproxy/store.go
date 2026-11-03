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

package observabilityproxy

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

// monitoringLabel is the label the tenant chart sets on every tenant
// namespace and the collector routes logs by
// (packages/system/monitoring-agents/templates/_tenant-log-routes.tpl).
const monitoringLabel = "namespace.cozystack.io/monitoring"

// StoreResolver maps a namespace to the tenant namespace whose store holds
// its logs, by the same rule the collector writes with.
type StoreResolver struct {
	namespaces corev1listers.NamespaceLister
	root       string
}

func NewStoreResolver(namespaces corev1listers.NamespaceLister, rootTenant string) *StoreResolver {
	return &StoreResolver{namespaces: namespaces, root: rootTenant}
}

// Resolve answers the root tenant for a namespace that no longer exists:
// its label is gone with it, and the collector's default output is the
// root store, so that is where any lines written without a route are.
func (r *StoreResolver) Resolve(namespace string) (string, error) {
	ns, err := r.namespaces.Get(namespace)
	if apierrors.IsNotFound(err) {
		return r.root, nil
	}
	if err != nil {
		return "", err
	}
	if target := ns.Labels[monitoringLabel]; target != "" {
		return target, nil
	}
	return r.root, nil
}
