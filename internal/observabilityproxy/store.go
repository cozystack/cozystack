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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
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

// Resolve answers the root tenant for a namespace that no longer exists,
// because its label went with it. Lines it wrote while routed to another
// tenant's store are not found there; reading them needs routing history
// (an open question of the design).
func (r *StoreResolver) Resolve(namespace string) (string, error) {
	ns, err := r.namespaces.Get(namespace)
	if apierrors.IsNotFound(err) {
		return r.root, nil
	}
	if err != nil {
		return "", err
	}
	target := ns.Labels[monitoringLabel]
	if target == "" {
		return r.root, nil
	}
	// The value becomes a DNS name; a dot in it would address a host in
	// some other namespace.
	if errs := validation.IsDNS1123Label(target); len(errs) > 0 {
		return "", fmt.Errorf("namespace %s: invalid %s label %q: %s", namespace, monitoringLabel, target, strings.Join(errs, "; "))
	}
	return target, nil
}
