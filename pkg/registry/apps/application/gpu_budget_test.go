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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

const (
	h100Key  = "h100.gpuclass.cozystack.io/devices"
	l40sKey  = "l40s-24c.gpuclass.cozystack.io/devices"
	totalKey = "gpuclass.cozystack.io/devices"
)

// The defaults the kubernetes-nodes and vm-instance charts declare for the
// fields the budget reads.
const (
	kubernetesNodesTestSchema = `{"type":"object","properties":{"maxReplicas":{"type":"integer","default":10},"gpus":{"type":"array","default":[],"items":{"type":"object","properties":{"className":{"type":"string"},"count":{"type":"integer"},"name":{"type":"string"}}}}}}`
	vmInstanceTestSchema      = `{"type":"object","properties":{"gpus":{"type":"array","default":[],"items":{"type":"object","properties":{"className":{"type":"string"},"count":{"type":"integer"},"name":{"type":"string"}}}}}}`
)

func registerTestSchemas(t *testing.T) {
	t.Helper()
	for kind, raw := range map[string]string{kubernetesNodesKind: kubernetesNodesTestSchema, vmInstanceKind: vmInstanceTestSchema} {
		s, err := buildSpecSchema(raw)
		if err != nil {
			t.Fatalf("build %s schema: %v", kind, err)
		}
		specSchemas.Store(kind, s)
	}
}

func TestGPUDemand(t *testing.T) {
	registerTestSchemas(t)
	tests := []struct {
		name   string
		kind   string
		values string
		want   map[string]int64
	}{
		{
			name:   "vm-instance class entries",
			kind:   vmInstanceKind,
			values: `{"gpus":[{"className":"h100","count":2},{"className":"l40s-24c"}]}`,
			want:   map[string]int64{h100Key: 2, l40sKey: 1, totalKey: 3},
		},
		{
			name:   "two entries of one class add up",
			kind:   vmInstanceKind,
			values: `{"gpus":[{"className":"h100","count":2},{"className":"h100","count":1}]}`,
			want:   map[string]int64{h100Key: 3, totalKey: 3},
		},
		{
			name:   "raw name entries reserve no budget",
			kind:   vmInstanceKind,
			values: `{"gpus":[{"name":"nvidia.com/GH100_H100"}]}`,
			want:   map[string]int64{},
		},
		{name: "no gpus", kind: vmInstanceKind, values: `{}`, want: map[string]int64{}},
		{
			name:   "kubernetes-nodes pool times maxReplicas",
			kind:   kubernetesNodesKind,
			values: `{"maxReplicas":3,"gpus":[{"className":"h100","count":2}]}`,
			want:   map[string]int64{h100Key: 6, totalKey: 6},
		},
		{
			name:   "kubernetes-nodes maxReplicas from the chart default",
			kind:   kubernetesNodesKind,
			values: `{"gpus":[{"className":"h100"}]}`,
			want:   map[string]int64{h100Key: 10, totalKey: 10},
		},
		{name: "other kinds reserve nothing", kind: "Postgres", values: `{"gpus":[{"className":"h100","count":2}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gpuDemand(tc.kind, []byte(tc.values))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) || (len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Errorf("gpuDemand = %v, want %v", got, tc.want)
			}
		})
	}
}

// Without the chart default a worker pool's size is unknown, and reading it
// as zero would let any pool through.
func TestGPUDemandRefusesAPoolWithoutMaxReplicas(t *testing.T) {
	registerTestSchemas(t)
	s, _ := specSchemas.LoadAndDelete(kubernetesNodesKind)
	t.Cleanup(func() { specSchemas.Store(kubernetesNodesKind, s) })
	if _, err := gpuDemand(kubernetesNodesKind, []byte(`{"gpus":[{"className":"h100"}]}`)); err == nil {
		t.Error("a pool without maxReplicas read as reserving nothing")
	}
	// A pool without class entries reserves nothing whatever its size.
	if got, err := gpuDemand(kubernetesNodesKind, []byte(`{"gpus":[{"name":"nvidia.com/GH100_H100"}]}`)); err != nil || len(got) != 0 {
		t.Errorf("pool without class entries = %v, %v; want no demand and no error", got, err)
	}
}

func TestNewRESTRegistersTheSpecSchema(t *testing.T) {
	NewREST(nil, nil, &config.Resource{Application: config.ApplicationConfig{Kind: "GPUBudgetTestKind", Plural: "gpubudgettestkinds", OpenAPISchema: vmInstanceTestSchema}})
	t.Cleanup(func() { specSchemas.Delete("GPUBudgetTestKind") })
	if _, ok := specSchemas.Load("GPUBudgetTestKind"); !ok {
		t.Error("NewREST did not register the kind's spec schema")
	}
}

// A count below one would offset another entry of the same class, and an
// overflow would wrap to a negative demand; either would slip past the
// budget.
func TestGPUDemandRefusesUnusableValues(t *testing.T) {
	registerTestSchemas(t)
	for name, tc := range map[string]struct{ kind, values string }{
		"count that is not a number":        {vmInstanceKind, `{"gpus":[{"className":"h100","count":"two"}]}`},
		"zero count":                        {vmInstanceKind, `{"gpus":[{"className":"h100","count":0}]}`},
		"negative count":                    {vmInstanceKind, `{"gpus":[{"className":"h100","count":5},{"className":"h100","count":-4}]}`},
		"negative maxReplicas":              {kubernetesNodesKind, `{"maxReplicas":-1,"gpus":[{"className":"h100"}]}`},
		"count times maxReplicas overflows": {kubernetesNodesKind, `{"maxReplicas":4,"gpus":[{"className":"h100","count":4611686018427387904}]}`},
		"sum overflows":                     {vmInstanceKind, `{"gpus":[{"className":"h100","count":4611686018427387904},{"className":"h100","count":4611686018427387904}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := gpuDemand(tc.kind, []byte(tc.values)); err == nil {
				t.Errorf("read as demand %v", got)
			}
		})
	}
}

func appHelmRelease(t *testing.T, kind, prefix, name, namespace, values string) *helmv2.HelmRelease {
	t.Helper()
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prefix + name,
			Namespace: namespace,
			Labels: map[string]string{
				ApplicationKindLabel:  kind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  name,
			},
		},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(values)}},
	}
}

func vmRelease(t *testing.T, name, namespace, values string) *helmv2.HelmRelease {
	return appHelmRelease(t, vmInstanceKind, "vm-instance-", name, namespace, values)
}

func nodesRelease(t *testing.T, name, namespace, values string) *helmv2.HelmRelease {
	return appHelmRelease(t, kubernetesNodesKind, "kubernetes-nodes-", name, namespace, values)
}

func newAppREST(t *testing.T, kind, prefix, plural string, objects ...client.Object) *REST {
	t.Helper()
	r := newTenantREST(t, objects...)
	r.kindName = kind
	r.gvk = schema.GroupVersionKind{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: kind}
	r.gvr = schema.GroupVersionResource{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: plural}
	r.releaseConfig = config.ReleaseConfig{Prefix: prefix}
	return r
}

func vmApplication(name, namespace, values string) *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: vmInstanceKind},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       &apiextv1.JSON{Raw: []byte(values)},
	}
}

func expectGPUErrs(t *testing.T, errs field.ErrorList, want string) {
	t.Helper()
	if want == "" {
		if len(errs) > 0 {
			t.Fatalf("refused: %v", errs)
		}
		return
	}
	if len(errs) == 0 {
		t.Fatalf("admitted, want a refusal with %q", want)
	}
	if !strings.Contains(errs.ToAggregate().Error(), want) {
		t.Errorf("refused for another reason, want %q: %v", want, errs)
	}
}

func TestValidateGPUBudget(t *testing.T) {
	registerTestSchemas(t)
	root := tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "4", totalKey: "6"})
	twoH100 := `{"gpus":[{"className":"h100","count":2}]}`
	tests := []struct {
		name    string
		objects []client.Object
		app     *appsv1alpha1.Application
		old     *appsv1alpha1.Application
		want    string
	}{
		{
			name:    "fits the class budget",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", twoH100)},
			app:     vmApplication("b", "tenant-root", twoH100),
		},
		{
			name:    "over the class budget names pool, key, limit and reservation",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", twoH100)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`),
			want:    `GPU budget "h100.gpuclass.cozystack.io/devices" of tenant pool tenant-root is 4 and 2 is already reserved; this application needs 3`,
		},
		{
			name:    "over the all-class budget",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"l40s-24c","count":5}]}`)},
			app:     vmApplication("b", "tenant-root", twoH100),
			want:    `GPU budget "gpuclass.cozystack.io/devices" of tenant pool tenant-root is 6 and 5 is already reserved`,
		},
		{
			name:    "an unbounded child tenant's VMs count in the parent pool",
			objects: []client.Object{root, tenantHelmRelease(t, "acme", "tenant-root", nil), vmRelease(t, "a", "tenant-acme", `{"gpus":[{"className":"h100","count":3}]}`)},
			app:     vmApplication("b", "tenant-root", twoH100),
			want:    "4 and 3 is already reserved",
		},
		{
			name:    "a write in an unbounded child is checked against the parent pool",
			objects: []client.Object{root, tenantHelmRelease(t, "acme", "tenant-root", nil), vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`)},
			app:     vmApplication("b", "tenant-acme", twoH100),
			want:    "of tenant pool tenant-root is 4",
		},
		{
			name:    "a bounded child tenant's declared budget is carved out",
			objects: []client.Object{root, tenantHelmRelease(t, "acme", "tenant-root", map[string]string{h100Key: "3"})},
			app:     vmApplication("b", "tenant-root", twoH100),
			want:    "4 and 3 is already reserved",
		},
		{
			name: "a bounded child tenant's usage is charged once, as its carve-out",
			objects: []client.Object{
				tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "10"}),
				tenantHelmRelease(t, "acme", "tenant-root", map[string]string{h100Key: "2"}),
				vmRelease(t, "a", "tenant-acme", twoH100),
			},
			app: vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":8}]}`),
		},
		{
			name:    "a write in a bounded child is checked against its own pool",
			objects: []client.Object{root, tenantHelmRelease(t, "acme", "tenant-root", map[string]string{h100Key: "1"})},
			app:     vmApplication("b", "tenant-acme", twoH100),
			want:    "of tenant pool tenant-acme is 1",
		},
		{
			name:    "an ancestor's budget is not carved out of a child pool",
			objects: []client.Object{root, tenantHelmRelease(t, "acme", "tenant-root", map[string]string{h100Key: "3"})},
			app:     vmApplication("b", "tenant-acme", twoH100),
		},
		{
			name:    "a namespace outside the tenant tree is not limited",
			objects: []client.Object{root},
			app:     vmApplication("b", "default", `{"gpus":[{"className":"h100","count":9}]}`),
		},
		{
			name:    "a key the pool does not declare is not limited",
			objects: []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{l40sKey: "1"})},
			app:     vmApplication("b", "tenant-root", twoH100),
		},
		{
			name:    "no bounded tenant at all",
			objects: []client.Object{tenantHelmRelease(t, "root", "tenant-root", nil)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":9}]}`),
		},
		{
			name:    "kubernetes-nodes pools reserve count times maxReplicas",
			objects: []client.Object{root, nodesRelease(t, "workers", "tenant-root", `{"maxReplicas":2,"gpus":[{"className":"h100","count":1}]}`)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`),
			want:    "4 and 2 is already reserved",
		},
		{
			name:    "update excludes the application's own release",
			objects: []client.Object{root, vmRelease(t, "b", "tenant-root", twoH100), vmRelease(t, "a", "tenant-root", twoH100)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":2}]}`),
			old:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":1}]}`),
		},
		{
			name:    "growing over the budget on update",
			objects: []client.Object{root, vmRelease(t, "b", "tenant-root", twoH100), vmRelease(t, "a", "tenant-root", twoH100)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`),
			old:     vmApplication("b", "tenant-root", twoH100),
			want:    "4 and 2 is already reserved; this application needs 3",
		},
		{
			name:    "shrinking passes in a pool already over its budget",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":5}]}`)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":1}]}`),
			old:     vmApplication("b", "tenant-root", twoH100),
		},
		{
			name:    "an unchanged entry passes in a pool already over its budget",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":5}]}`)},
			app:     vmApplication("b", "tenant-root", twoH100),
			old:     vmApplication("b", "tenant-root", twoH100),
		},
		{
			name:    "a fractional budget is not rounded up",
			objects: []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "1500m"})},
			app:     vmApplication("b", "tenant-root", twoH100),
			want:    `of tenant pool tenant-root is 1500m and 0 is already reserved; this application needs 2`,
		},
		{
			name:    "a release with unreadable values is skipped",
			objects: []client.Object{root, vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":"x"}]}`)},
			app:     vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":4}]}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAppREST(t, vmInstanceKind, "vm-instance-", "vminstances", tc.objects...)
			errs := r.validateGPUBudget(context.Background(), tc.app, tc.old)
			expectGPUErrs(t, errs, tc.want)
		})
	}
}

func TestValidateGPUBudgetRefusesUnreadableValues(t *testing.T) {
	registerTestSchemas(t)
	r := newAppREST(t, vmInstanceKind, "vm-instance-", "vminstances", tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "4"}))
	errs := r.validateGPUBudget(context.Background(), vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":"x"}]}`), nil)
	expectGPUErrs(t, errs, "spec.gpus")
}

func TestValidateGPUBudgetIgnoresOtherKinds(t *testing.T) {
	registerTestSchemas(t)
	r := newAppREST(t, "Postgres", "postgres-", "postgreses", tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "0"}))
	expectGPUErrs(t, r.validateGPUBudget(context.Background(), vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":2}]}`), nil), "")
}

// A key in the GPU budget domain that names no class budget would be kept
// out of the ResourceQuota and checked by nothing.
func TestValidateTenantResourceQuotasRefusesMalformedGPUKeys(t *testing.T) {
	r := newTenantREST(t, tenantHelmRelease(t, "foo", "tenant-root", nil))
	for _, key := range []string{"h100.gpuclass.cozystack.io/units", "a.b.gpuclass.cozystack.io/devices", "gpuclass.cozystack.io/total"} {
		t.Run(key, func(t *testing.T) {
			expectGPUErrs(t, r.validateTenantResourceQuotas(context.Background(), childApplication(t, "bar", "tenant-foo", map[string]string{key: "1"})), "not a GPU budget key")
		})
	}
	expectGPUErrs(t, r.validateTenantResourceQuotas(context.Background(), childApplication(t, "bar", "tenant-foo", map[string]string{h100Key: "1", totalKey: "2", "xgpuclass.cozystack.io/devices": "1"})), "")
}

// A child tenant's GPU budget is carved out of what its parent's pool has
// left after the class entries already declared there.
func TestValidateTenantResourceQuotasCountsGPUClassEntries(t *testing.T) {
	registerTestSchemas(t)
	parent := tenantHelmRelease(t, "foo", "tenant-root", map[string]string{h100Key: "4"})
	vm := vmRelease(t, "a", "tenant-foo", `{"gpus":[{"className":"h100","count":3}]}`)
	r := newTenantREST(t, parent, vm)
	expectGPUErrs(t, r.validateTenantResourceQuotas(context.Background(), childApplication(t, "bar", "tenant-foo", map[string]string{h100Key: "2"})), "3 currently in use by the parent pool")
	expectGPUErrs(t, r.validateTenantResourceQuotas(context.Background(), childApplication(t, "bar", "tenant-foo", map[string]string{h100Key: "1"})), "")

	// The child's own namespace is excluded, as for every other key.
	own := vmRelease(t, "b", "tenant-foo-bar", `{"gpus":[{"className":"h100","count":3}]}`)
	r = newTenantREST(t, parent, own)
	expectGPUErrs(t, r.validateTenantResourceQuotas(context.Background(), childApplication(t, "bar", "tenant-foo", map[string]string{h100Key: "4"})), "")
}

func newVMInstanceREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	r := newKubernetesWarnREST(t, objects...)
	r.kindName = vmInstanceKind
	r.gvk.Kind = vmInstanceKind
	r.gvr.Resource = "vminstances"
	r.releaseConfig = config.ReleaseConfig{Prefix: "vm-instance-"}
	return r
}

func TestCreateAndUpdateCheckTheGPUBudget(t *testing.T) {
	registerTestSchemas(t)
	root := tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "1"})
	over := vmApplication("b", "tenant-root", `{"gpus":[{"className":"h100","count":2}]}`)
	ctx := request.WithNamespace(context.Background(), "tenant-root")
	sentinel := errors.New("past the GPU budget check")

	r := newVMInstanceREST(t, root)
	createValidation := func(context.Context, runtime.Object) error { return sentinel }
	if _, err := r.Create(ctx, over.DeepCopy(), createValidation, &metav1.CreateOptions{}); errors.Is(err, sentinel) || !strings.Contains(fmt.Sprint(err), "GPU budget") {
		t.Fatalf("Create = %v, want a GPU budget refusal", err)
	}

	existing := vmRelease(t, "b", "tenant-root", `{"gpus":[{"className":"h100","count":1}]}`)
	r = newVMInstanceREST(t, root, existing)
	updateValidation := func(context.Context, runtime.Object, runtime.Object) error { return nil }
	_, _, err := r.Update(ctx, "b", newDefaultUpdatedObjectInfo(over.DeepCopy()), nil, updateValidation, false, &metav1.UpdateOptions{})
	if !strings.Contains(fmt.Sprint(err), "GPU budget") {
		t.Fatalf("Update = %v, want a GPU budget refusal", err)
	}
}

func TestGPURemainingBudget(t *testing.T) {
	registerTestSchemas(t)
	tests := []struct {
		name        string
		objects     []client.Object
		namespace   string
		class       string
		want        int64
		wantLimited bool
	}{
		{
			name:        "class budget minus what the pool reserves",
			objects:     []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "4"}), vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`)},
			namespace:   "tenant-root",
			class:       "h100",
			want:        1,
			wantLimited: true,
		},
		{
			name: "the smaller of the class and the all-class budget",
			objects: []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "4", totalKey: "5"}),
				vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"l40s-24c","count":3}]}`)},
			namespace:   "tenant-root",
			class:       "h100",
			want:        2,
			wantLimited: true,
		},
		{
			name: "carve-outs of bounded child tenants count",
			objects: []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "4"}),
				tenantHelmRelease(t, "acme", "tenant-root", map[string]string{h100Key: "3"})},
			namespace:   "tenant-root",
			class:       "h100",
			want:        1,
			wantLimited: true,
		},
		{
			name:        "never below zero",
			objects:     []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "1"}), vmRelease(t, "a", "tenant-root", `{"gpus":[{"className":"h100","count":3}]}`)},
			namespace:   "tenant-root",
			class:       "h100",
			want:        0,
			wantLimited: true,
		},
		{
			name:        "a fractional budget offers whole units only",
			objects:     []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "1500m"})},
			namespace:   "tenant-root",
			class:       "h100",
			want:        1,
			wantLimited: true,
		},
		{
			name:      "a large budget does not overflow milliunits",
			objects:   []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{h100Key: "9223372036854775807"})},
			namespace: "tenant-root", class: "h100", want: 9223372036854775807, wantLimited: true,
		},
		{
			name:      "no GPU budget in the pool",
			objects:   []client.Object{tenantHelmRelease(t, "root", "tenant-root", map[string]string{"cpu": "4"})},
			namespace: "tenant-root",
			class:     "h100",
		},
		{
			name:      "no pool",
			objects:   []client.Object{tenantHelmRelease(t, "root", "tenant-root", nil)},
			namespace: "tenant-root",
			class:     "h100",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTenantREST(t, tc.objects...)
			got, limited, err := GPURemainingBudget(r.c)(context.Background(), tc.namespace, tc.class)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || limited != tc.wantLimited {
				t.Errorf("GPURemainingBudget = (%d, %v), want (%d, %v)", got, limited, tc.want, tc.wantLimited)
			}
		})
	}
}
