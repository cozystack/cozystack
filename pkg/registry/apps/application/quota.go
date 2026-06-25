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

package application

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
)

// rootTenantNamespace is the namespace of the cluster's root tenant. The root
// tenant is special-cased throughout the tenant hierarchy: it is its own parent
// and its HelmRelease lives in its own namespace.
const rootTenantNamespace = "tenant-root"

// resourceQuotasField is the tenant values key holding the declared quota, kept
// in sync with packages/apps/tenant/values.yaml (`resourceQuotas`).
const resourceQuotasField = "resourceQuotas"

// tenantValues is the minimal projection of a tenant's Helm values needed to
// reason about hierarchical quotas. Only resourceQuotas is decoded; everything
// else in the values blob is ignored.
type tenantValues struct {
	ResourceQuotas map[string]resource.Quantity `json:"resourceQuotas"`
}

// parseDeclaredQuotas extracts spec.resourceQuotas from a tenant values JSON
// blob. The quota keys are kept verbatim as the operator writes them (e.g.
// "cpu", "memory", "requests.storage", "count/services") — the same vocabulary
// the parent and every child use — so they can be compared directly. The
// `cozy-lib.resources.flatten` expansion into limits.*/requests.* is a
// downstream rendering concern of the tenant chart and is intentionally not
// applied here.
func parseDeclaredQuotas(raw []byte) (map[string]resource.Quantity, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values tenantValues
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode tenant values: %w", err)
	}
	return values.ResourceQuotas, nil
}

// declaredQuotasFromHelmRelease reads resourceQuotas from a tenant HelmRelease's
// values. A tenant Application is stored as a HelmRelease whose Spec.Values is
// the verbatim Application spec (see ConvertApplicationToHelmRelease).
func declaredQuotasFromHelmRelease(hr *helmv2.HelmRelease) (map[string]resource.Quantity, error) {
	if hr.Spec.Values == nil {
		return nil, nil
	}
	return parseDeclaredQuotas(hr.Spec.Values.Raw)
}

// parentTenantHelmReleaseRef maps the namespace owned by a tenant to the
// name/namespace of that tenant's own HelmRelease. It is the inverse of
// REST.computeTenantNamespace: a tenant created as Application "<name>" in
// namespace C owns the workload namespace computeTenantNamespace(C, name), so
// given the owned namespace we recover (release name, release namespace).
//
// Examples (prefix "tenant-"):
//
//	"tenant-root"        -> ("tenant-root", "tenant-root")  // root is its own parent
//	"tenant-foo"         -> ("tenant-foo",  "tenant-root")  // foo lives in root
//	"tenant-foo-bar"     -> ("tenant-bar",  "tenant-foo")   // bar lives in foo
//	"tenant-foo-bar-baz" -> ("tenant-baz",  "tenant-foo-bar")
func parentTenantHelmReleaseRef(ownedNamespace, prefix string) (name, namespace string, ok bool) {
	if ownedNamespace == rootTenantNamespace {
		return prefix + "root", rootTenantNamespace, true
	}
	if !strings.HasPrefix(ownedNamespace, prefix) {
		return "", "", false
	}
	segments := strings.Split(ownedNamespace, "-")
	last := segments[len(segments)-1]
	// Exactly "tenant-<name>" (two segments) is a direct child of root, so its
	// HelmRelease lives in the root namespace. Deeper names live in the
	// namespace formed by stripping the trailing "-<name>".
	if len(segments) == 2 {
		return prefix + last, rootTenantNamespace, true
	}
	return prefix + last, strings.Join(segments[:len(segments)-1], "-"), true
}

// parentTenantDeclaredQuotas returns the declared resourceQuotas of the tenant
// that owns childNamespace (the namespace a child Tenant CR is created in). An
// empty result means the parent declares no quota and therefore does not
// directly constrain the child at declaration time.
func (r *REST) parentTenantDeclaredQuotas(ctx context.Context, childNamespace string) (map[string]resource.Quantity, error) {
	hrName, hrNamespace, ok := parentTenantHelmReleaseRef(childNamespace, r.releaseConfig.Prefix)
	if !ok {
		return nil, nil
	}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: hrNamespace, Name: hrName}, hr); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return declaredQuotasFromHelmRelease(hr)
}

// siblingDeclaredQuotas sums, per resource, the declared quotas of every tenant
// in namespace that already carved a slice out of the shared parent budget,
// excluding the tenant named excludeName (so updates do not count against
// themselves). These are the direct children of the same parent.
func (r *REST) siblingDeclaredQuotas(ctx context.Context, namespace, excludeName string) (map[string]resource.Quantity, error) {
	list := &helmv2.HelmReleaseList{}
	selector := labels.SelectorFromSet(labels.Set{
		ApplicationKindLabel:  r.kindName,
		ApplicationGroupLabel: r.gvk.Group,
	})
	if err := r.c.List(ctx, list, &client.ListOptions{Namespace: namespace, LabelSelector: selector}); err != nil {
		return nil, err
	}

	sum := map[string]resource.Quantity{}
	for i := range list.Items {
		hr := &list.Items[i]
		name := strings.TrimPrefix(hr.Name, r.releaseConfig.Prefix)
		if name == excludeName {
			continue
		}
		quotas, err := declaredQuotasFromHelmRelease(hr)
		if err != nil {
			// A malformed sibling must not block an unrelated tenant write; skip
			// it (the controller surfaces such tenants separately).
			klog.Warningf("skipping sibling tenant %s/%s with unparseable quotas: %v", hr.Namespace, hr.Name, err)
			continue
		}
		addQuotas(sum, quotas)
	}
	return sum, nil
}

// validateTenantResourceQuotas enforces hierarchical quota allocation for Tenant
// Applications at declaration time. A child tenant's declared quota is "carved
// out" of its parent's remaining quota and may not exceed it: for every resource
// the parent bounds,
//
//	child[r] <= parent[r] - sum(other children already carved out)[r]
//
// A child that declares no quota of its own is always allowed here — it shares
// the parent's pool rather than reserving a fixed slice (runtime enforcement of
// that shared pool is the tenant quota controller's job). A parent that declares
// no quota does not constrain its children at this layer.
//
// This is the deterministic, declaration-time gate against quota escalation
// ("a tenant must not, via quota, claim more space than it was allocated"). It
// runs inside the aggregated apiserver where Tenant writes are served, with full
// client access to the parent and sibling HelmReleases.
func (r *REST) validateTenantResourceQuotas(ctx context.Context, app *appsv1alpha1.Application) field.ErrorList {
	allErrs := field.ErrorList{}
	if r.kindName != validation.TenantKind {
		return allErrs
	}
	fldPath := field.NewPath("spec").Child(resourceQuotasField)

	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	childQuota, err := parseDeclaredQuotas(raw)
	if err != nil {
		return append(allErrs, field.Invalid(fldPath, "", err.Error()))
	}
	if len(childQuota) == 0 {
		// Unbounded child: shares the parent pool, allowed at declaration time.
		return allErrs
	}

	parentQuota, err := r.parentTenantDeclaredQuotas(ctx, app.Namespace)
	if err != nil {
		return append(allErrs, field.InternalError(fldPath, err))
	}
	if len(parentQuota) == 0 {
		// Parent declares no quota: it does not directly constrain the child.
		return allErrs
	}

	siblings, err := r.siblingDeclaredQuotas(ctx, app.Namespace, app.Name)
	if err != nil {
		return append(allErrs, field.InternalError(fldPath, err))
	}

	for _, res := range sortedQuotaKeys(parentQuota) {
		want, requested := childQuota[res]
		if !requested {
			continue
		}
		parentLimit := parentQuota[res]
		allocated := siblings[res] // zero value when no sibling bounds this resource
		remaining := parentLimit.DeepCopy()
		remaining.Sub(allocated)
		if want.Cmp(remaining) > 0 {
			allErrs = append(allErrs, field.Forbidden(fldPath.Key(res),
				fmt.Sprintf("requested %s exceeds the parent tenant's remaining %q quota of %s (parent allows %s, %s already allocated to sibling tenants); a child tenant may not be granted more quota than its parent has left",
					want.String(), res, remaining.String(), parentLimit.String(), allocated.String())))
		}
	}
	return allErrs
}

// addQuotas adds src into dst in place, per resource key.
func addQuotas(dst, src map[string]resource.Quantity) {
	for k, v := range src {
		if cur, ok := dst[k]; ok {
			cur.Add(v)
			dst[k] = cur
		} else {
			dst[k] = v.DeepCopy()
		}
	}
}

// sortedQuotaKeys returns the resource keys of a quota map in a deterministic
// order so that admission error messages are stable.
func sortedQuotaKeys(q map[string]resource.Quantity) []string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
