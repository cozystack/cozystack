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
	"math"
	"slices"
	"sort"
	"strings"
	"sync"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu"
	"github.com/cozystack/cozystack/internal/gpu/model"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
)

// The application kinds whose gpus entries reserve GPU budget.
const (
	vmInstanceKind      = "VMInstance"
	kubernetesNodesKind = "KubernetesNodes"
)

// tenantReleasePrefix is the name prefix of a Tenant's HelmRelease and of
// every tenant namespace, which computeTenantNamespace spells out too.
const tenantReleasePrefix = "tenant-"

// specSchemas maps an application kind to its spec schema. NewREST fills
// it, so a check served by one kind can default the stored values of
// another, the way the chart reads them.
var specSchemas sync.Map

type gpuEntry struct {
	ClassName string `json:"className"`
	Count     *int64 `json:"count"`
}

type gpuValues struct {
	GPUs        []gpuEntry `json:"gpus"`
	MaxReplicas *int64     `json:"maxReplicas"`
}

// gpuDemand returns the GPU budget units an application's values reserve,
// by budget key: every class entry reserves its count under its class key
// and under the all-class key, times maxReplicas for a worker pool. Raw
// name entries reserve nothing.
func gpuDemand(kind string, raw []byte) (map[string]int64, error) {
	if kind != vmInstanceKind && kind != kubernetesNodesKind {
		return nil, nil
	}
	values := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("decode %s values: %w", kind, err)
		}
	}
	if s, ok := specSchemas.Load(kind); ok {
		if err := defaultLikeKubernetes(&values, s.(*structuralschema.Structural)); err != nil {
			return nil, fmt.Errorf("default %s values: %w", kind, err)
		}
	}
	defaulted, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var v gpuValues
	if err := json.Unmarshal(defaulted, &v); err != nil {
		return nil, fmt.Errorf("decode %s gpus: %w", kind, err)
	}
	classEntries := slices.ContainsFunc(v.GPUs, func(e gpuEntry) bool { return e.ClassName != "" })
	replicas := int64(1)
	if kind == kubernetesNodesKind && classEntries {
		if v.MaxReplicas == nil {
			return nil, fmt.Errorf("decode %s values: no maxReplicas", kind)
		}
		replicas = *v.MaxReplicas
		if replicas < 0 {
			return nil, fmt.Errorf("decode %s values: maxReplicas %d is negative", kind, replicas)
		}
	}
	demand := map[string]int64{}
	var perWorker int64
	for _, e := range v.GPUs {
		if e.ClassName == "" {
			continue
		}
		count := int64(1)
		if e.Count != nil {
			count = *e.Count
		}
		if count < 1 {
			return nil, fmt.Errorf("decode %s gpus: class %s has count %d, below 1", kind, e.ClassName, count)
		}
		perWorker += count
		if perWorker > gpu.MaxVMIUnits {
			return nil, fmt.Errorf("a VMI can expand at most %d GPU units from class entries", gpu.MaxVMIUnits)
		}
		if replicas > 0 && count > math.MaxInt64/replicas {
			return nil, fmt.Errorf("decode %s gpus: class %s reserves more than %d units", kind, e.ClassName, int64(math.MaxInt64))
		}
		units := count * replicas
		for _, k := range []string{string(model.ClassQuotaKey(e.ClassName)), gpu.QuotaKeyTotal} {
			if units > math.MaxInt64-demand[k] {
				return nil, fmt.Errorf("decode %s gpus: more than %d units under %s", kind, int64(math.MaxInt64), k)
			}
			demand[k] += units
		}
	}
	return demand, nil
}

// tenantQuotas returns the declared resourceQuotas of every tenant, keyed
// by the namespace the tenant owns.
func (r *REST) tenantQuotas(ctx context.Context) (map[string]map[string]resource.Quantity, error) {
	releases := &helmv2.HelmReleaseList{}
	selector := labels.SelectorFromSet(labels.Set{
		ApplicationKindLabel:  validation.TenantKind,
		ApplicationGroupLabel: appsv1alpha1.GroupName,
	})
	if err := r.c.List(ctx, releases, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]resource.Quantity, len(releases.Items))
	for i := range releases.Items {
		hr := &releases.Items[i]
		ns := r.computeTenantNamespace(hr.Namespace, strings.TrimPrefix(hr.Name, tenantReleasePrefix))
		quotas, err := declaredQuotasFromHelmRelease(hr)
		if err != nil {
			klog.Warningf("skipping tenant %s/%s with unparseable quotas while checking GPU budgets: %v", hr.Namespace, hr.Name, err)
			quotas = nil
		}
		out[ns] = quotas
	}
	return out, nil
}

func boundedTenants(quotas map[string]map[string]resource.Quantity) map[string]bool {
	bounded := make(map[string]bool, len(quotas))
	for ns, q := range quotas {
		if len(q) > 0 {
			bounded[ns] = true
		}
	}
	return bounded
}

// gpuPoolUsage sums the GPU budget the class entries in the pool rooted at
// root reserve: the root's namespace plus every namespace whose nearest
// bounded ancestor is root, the same members parentPoolUsage reads, minus
// excludeNS. A release of the kind and name in skip is left out, so an
// update does not count against itself.
func (r *REST) gpuPoolUsage(ctx context.Context, root, excludeNS string, bounded map[string]bool, skip client.ObjectKey) (map[string]int64, error) {
	used := map[string]int64{}
	for _, kind := range []string{vmInstanceKind, kubernetesNodesKind} {
		releases := &helmv2.HelmReleaseList{}
		selector := labels.SelectorFromSet(labels.Set{
			ApplicationKindLabel:  kind,
			ApplicationGroupLabel: appsv1alpha1.GroupName,
		})
		if err := r.c.List(ctx, releases, &client.ListOptions{LabelSelector: selector}); err != nil {
			return nil, err
		}
		for i := range releases.Items {
			hr := &releases.Items[i]
			if hr.Namespace == excludeNS || client.ObjectKeyFromObject(hr) == skip || r.poolRootOf(hr.Namespace, bounded) != root {
				continue
			}
			var raw []byte
			if hr.Spec.Values != nil {
				raw = hr.Spec.Values.Raw
			}
			demand, err := gpuDemand(kind, raw)
			if err != nil {
				klog.Warningf("skipping %s %s/%s with unreadable gpus while checking GPU budgets: %v", kind, hr.Namespace, hr.Name, err)
				continue
			}
			for k, v := range demand {
				used[k] = min(used[k], math.MaxInt64-v) + v
			}
		}
	}
	return used, nil
}

// validateGPUBudget refuses a VMInstance or KubernetesNodes write whose
// class entries would take its pool over a declared GPU budget. It applies
// the tenant pool arithmetic to the declared resourceQuotas and the
// declared class entries, the inputs the pool-wide admission gate of the
// design reads. On an update only growth is checked, so shrinking and
// unchanged entries always pass.
func (r *REST) validateGPUBudget(ctx context.Context, app, old *appsv1alpha1.Application) field.ErrorList {
	fldPath := field.NewPath("spec", "gpus")
	demand, err := gpuDemand(r.kindName, specRaw(app))
	if err != nil {
		return field.ErrorList{field.Invalid(fldPath, "", err.Error())}
	}
	var previous map[string]int64
	if old != nil {
		// The stored values passed this check before, so a read failure
		// counts them as reserving nothing and checks the full new demand.
		previous, _ = gpuDemand(r.kindName, specRaw(old))
	}
	var grown []string
	for k, v := range demand {
		if v > previous[k] {
			grown = append(grown, k)
		}
	}
	if len(grown) == 0 {
		return nil
	}
	sort.Strings(grown)

	pool, err := r.gpuPool(ctx, app.Namespace, client.ObjectKey{Namespace: app.Namespace, Name: r.releaseConfig.Prefix + app.Name})
	if err != nil {
		return field.ErrorList{field.InternalError(fldPath, err)}
	}
	if pool == nil {
		return nil
	}

	var errs field.ErrorList
	for _, k := range grown {
		limit, ok := pool.budget[k]
		if !ok {
			continue
		}
		reserved := pool.reserved(k)
		total := reserved.DeepCopy()
		total.Add(*resource.NewQuantity(demand[k], resource.DecimalSI))
		if total.Cmp(limit) > 0 {
			errs = append(errs, field.Forbidden(fldPath, fmt.Sprintf(
				"GPU budget %q of tenant pool %s is %s and %s is already reserved; this application needs %d",
				k, pool.root, limit.String(), reserved.String(), demand[k])))
		}
	}
	return errs
}

type gpuPoolFigures struct {
	root   string
	budget map[string]resource.Quantity
	carved map[string]resource.Quantity
	used   map[string]int64
}

func (p *gpuPoolFigures) reserved(key string) resource.Quantity {
	q := p.carved[key].DeepCopy()
	q.Add(*resource.NewQuantity(p.used[key], resource.DecimalSI))
	return q
}

// gpuPool returns the figures of the pool ns draws from, nil when no pool
// governs ns. The release named by skip reserves nothing.
func (r *REST) gpuPool(ctx context.Context, ns string, skip client.ObjectKey) (*gpuPoolFigures, error) {
	quotas, err := r.tenantQuotas(ctx)
	if err != nil {
		return nil, err
	}
	bounded := boundedTenants(quotas)
	root := r.poolRootOf(ns, bounded)
	if root == "" {
		return nil, nil
	}
	p := &gpuPoolFigures{root: root, budget: quotas[root], carved: map[string]resource.Quantity{}}
	for n, q := range quotas {
		if r.poolRootOf(r.parentNamespaceOf(n), bounded) == root {
			addQuotas(p.carved, q)
		}
	}
	if p.used, err = r.gpuPoolUsage(ctx, root, "", bounded, skip); err != nil {
		return nil, err
	}
	return p, nil
}

// GPURemainingBudget returns how many more units of a class the pool of a
// namespace may declare under its GPU budgets, the smaller of what its
// class key and its all-class key leave, and false when neither is
// declared. It is the figure the budget check on VMInstance and
// KubernetesNodes writes compares against.
func GPURemainingBudget(c client.Client) func(ctx context.Context, namespace, class string) (int64, bool, error) {
	r := &REST{c: c}
	return func(ctx context.Context, namespace, class string) (int64, bool, error) {
		pool, err := r.gpuPool(ctx, namespace, client.ObjectKey{})
		if err != nil || pool == nil {
			return 0, false, err
		}
		var left int64
		limited := false
		for _, k := range []string{string(model.ClassQuotaKey(class)), gpu.QuotaKeyTotal} {
			limit, ok := pool.budget[k]
			if !ok {
				continue
			}
			q := limit.DeepCopy()
			q.Sub(pool.reserved(k))
			whole := wholeGPUUnits(q)
			if !limited || whole < left {
				left = whole
			}
			limited = true
		}
		return left, limited, nil
	}
}

// wholeGPUUnits floors a remaining budget without converting it to
// milliunits, which can overflow even when the unit count fits int64.
func wholeGPUUnits(q resource.Quantity) int64 {
	if q.Sign() <= 0 {
		return 0
	}
	if q.Cmp(*resource.NewQuantity(math.MaxInt64, resource.DecimalSI)) >= 0 {
		return math.MaxInt64
	}
	n := q.Value()
	if q.Cmp(*resource.NewQuantity(n, resource.DecimalSI)) < 0 {
		n--
	}
	return n
}

// parentPoolGPUUsage is the GPU counterpart of parentPoolUsage for the
// tenant declaration gate.
func (r *REST) parentPoolGPUUsage(ctx context.Context, parentNS, excludeNS string) (map[string]int64, error) {
	quotas, err := r.tenantQuotas(ctx)
	if err != nil {
		return nil, err
	}
	return r.gpuPoolUsage(ctx, parentNS, excludeNS, boundedTenants(quotas), client.ObjectKey{})
}

func specRaw(app *appsv1alpha1.Application) []byte {
	if app == nil || app.Spec == nil {
		return nil
	}
	return app.Spec.Raw
}

// validateGPUClassModes refuses a KubernetesNodes write whose class entries
// name a vGPU class: every worker pool runs Talos, which carries no vGPU
// guest driver, so the workers would start and never use their GPUs. A
// class must exist so its mode is checked before the pool is stored.
func (r *REST) validateGPUClassModes(ctx context.Context, app *appsv1alpha1.Application) field.ErrorList {
	if r.kindName != kubernetesNodesKind {
		return nil
	}
	var v gpuValues
	if raw := specRaw(app); len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return field.ErrorList{field.Invalid(field.NewPath("spec", "gpus"), "", err.Error())}
		}
	}
	var errs field.ErrorList
	for i, e := range v.GPUs {
		if e.ClassName == "" {
			continue
		}
		c := &cozyv1alpha1.GPUClass{}
		err := r.w.Get(ctx, client.ObjectKey{Name: e.ClassName}, c)
		if apierrors.IsNotFound(err) {
			errs = append(errs, field.Invalid(field.NewPath("spec", "gpus").Index(i).Child("className"), e.ClassName,
				fmt.Sprintf("GPUClass %s does not exist; its mode must be known before creating a worker pool", e.ClassName)))
			continue
		}
		if err != nil {
			return append(errs, field.InternalError(field.NewPath("spec", "gpus").Index(i), err))
		}
		if c.Spec.Mode == cozyv1alpha1.GPUModeVGPU {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "gpus").Index(i).Child("className"), fmt.Sprintf(
				"GPUClass %s is vGPU, and Talos workers carry no vGPU guest driver; a worker pool takes passthrough classes only", e.ClassName)))
		}
	}
	return errs
}
