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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// shippedResource reads an in-tree ApplicationDefinition as cozystack-api
// would serve it, consumption included.
func shippedResource(t *testing.T, path string) config.Resource {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var def cozyv1alpha1.ApplicationDefinition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if def.Spec.Consumption == nil {
		t.Fatalf("%s declares no consumption", path)
	}
	return config.Resource{
		Application: config.ApplicationConfig{
			Kind:          def.Spec.Application.Kind,
			Singular:      def.Spec.Application.Singular,
			Plural:        def.Spec.Application.Plural,
			OpenAPISchema: def.Spec.Application.OpenAPISchema,
			Consumption:   def.Spec.Consumption,
		},
		Release: config.ReleaseConfig{Prefix: def.Spec.Release.Prefix},
	}
}

func testEnvironment() reservation.Environment {
	return &reservation.StaticEnvironment{
		Instancetypes: map[string]reservation.ResourceList{
			"u1.small":  {"cpu": resource.MustParse("1"), "memory": resource.MustParse("4Gi")},
			"u1.medium": {"cpu": resource.MustParse("2"), "memory": resource.MustParse("8Gi")},
			"u1.large":  {"cpu": resource.MustParse("4"), "memory": resource.MustParse("16Gi")},
		},
		Presets: reservation.ShippedPresets(),
	}
}

type gateFixture struct {
	client client.WithWatch
	gate   *Reservation
	vms    *REST
}

// newGateFixture serves VMInstance with the gate on, over a fake client
// seeded with objects.
func newGateFixture(t *testing.T, objects ...client.Object) gateFixture {
	t.Helper()
	return newGateFixtureWith(t, interceptor.Funcs{}, objects...)
}

// newGateFixtureWith serves VMInstance through a client whose calls funcs
// can intercept, while the fixture's own client reads and writes the store
// directly.
func newGateFixtureWith(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) gateFixture {
	t.Helper()
	res := shippedResource(t, "../../../../packages/system/vm-instance-rd/cozyrds/vm-instance.yaml")
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{helmv2.AddToScheme, corev1.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{res}}); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	served := interceptor.NewClient(c, funcs)
	gate := NewReservation([]config.Resource{res}, testEnvironment())
	vms := NewREST(served, served, &res)
	vms.SetReservation(gate)
	return gateFixture{client: c, gate: gate, vms: vms}
}

func vmApplication(t *testing.T, name, namespace string, values map[string]any) *appsv1alpha1.Application {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: "VMInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       &apiextv1.JSON{Raw: raw},
	}
}

func (f gateFixture) create(t *testing.T, app *appsv1alpha1.Application) error {
	t.Helper()
	ctx := request.WithNamespace(context.Background(), app.Namespace)
	_, err := f.vms.Create(ctx, app, nil, &metav1.CreateOptions{})
	return err
}

func (f gateFixture) update(t *testing.T, app *appsv1alpha1.Application) error {
	t.Helper()
	ctx := request.WithNamespace(context.Background(), app.Namespace)
	_, _, err := f.vms.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{})
	return err
}

func TestReservationGateRefusesAnOrderTheBudgetCannotHold(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))

	for _, name := range []string{"one", "two"} {
		if err := f.create(t, vmApplication(t, name, "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	err := f.create(t, vmApplication(t, "three", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("third VM: want an Invalid error, got %v", err)
	}
	for _, want := range []string{`"memory" budget of tenant pool "tenant-acme"`, "8Gi allowed", "8Gi already reserved by 2 applications", "4Gi requested", "1 cpu, 4Gi memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}

	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-three"}, hr); !apierrors.IsNotFound(err) {
		t.Fatalf("a refused order must not be stored: %v", err)
	}
}

func TestReservationGateCountsOnlyTheDeltaOnUpdate(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}

	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatalf("u1.small to u1.large in a 20Gi pool reserves 12Gi more and must fit: %v", err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("16Gi + 4Gi fits 20Gi: %v", err)
	}
	err := f.create(t, vmApplication(t, "cache", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("a full pool must refuse a new order, got %v", err)
	}
}

func TestReservationGateRefusesAGrowthPastTheBudget(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "16Gi"}))
	for _, name := range []string{"web", "db"} {
		if err := f.create(t, vmApplication(t, name, "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
			t.Fatal(err)
		}
	}
	err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "12Gi requested") {
		t.Fatalf("4Gi + 16Gi does not fit 16Gi; want the 12Gi growth refused, got %v", err)
	}
}

func TestReservationGateAcceptsShrinksAndNoOpsInAnOvercommittedPool(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}

	// The operator lowers the budget below what is already reserved.
	tenant := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: rootTenantNamespace, Name: "tenant-acme"}, tenant); err != nil {
		t.Fatal(err)
	}
	tenant.Spec.Values = tenantValuesJSON(t, map[string]string{"memory": "8Gi"})
	if err := f.client.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}

	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large", "external": false})); err != nil {
		t.Fatalf("an edit that reserves nothing more must pass an overcommitted pool: %v", err)
	}
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("a shrink must always pass: %v", err)
	}
}

func TestReservationGateChargesSubTenantCarveOuts(t *testing.T) {
	f := newGateFixture(t,
		tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "16Gi"}),
		tenantHelmRelease(t, "dev", "tenant-acme", map[string]string{"memory": "12Gi"}),
		tenantHelmRelease(t, "shared", "tenant-acme", nil),
	)
	if err := f.create(t, vmApplication(t, "one", "tenant-acme-shared", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("an unbounded sub-tenant draws from its parent's remaining 4Gi: %v", err)
	}
	err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "12Gi carved out by sub-tenants") {
		t.Fatalf("the parent pool holds 16Gi - 12Gi carved out - 4Gi reserved; want a refusal naming the carve-out, got %v", err)
	}
	if err := f.create(t, vmApplication(t, "three", "tenant-acme-dev", map[string]any{"instanceType": "u1.large"})); !apierrors.IsInvalid(err) {
		t.Fatalf("a bounded sub-tenant's own pool is 12Gi; 16Gi must be refused, got %v", err)
	}
	if err := f.create(t, vmApplication(t, "four", "tenant-acme-dev", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("a bounded sub-tenant draws from its own budget: %v", err)
	}
}

func TestReservationGateFailsClosedOnTheObjectWritten(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "x9.huge"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "x9.huge") {
		t.Fatalf("an unknown instance type must be refused with the evaluator's reason, got %v", err)
	}
}

func TestReservationGateKeepsAStaleSiblingsLastFigure(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "old", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	// The sibling's values stop evaluating behind the gate's back: an
	// instance type deleted from the cluster, here an edit made with the
	// gate off.
	hr := &helmv2.HelmRelease{}
	key := client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-old"}
	if err := f.client.Get(context.Background(), key, hr); err != nil {
		t.Fatal(err)
	}
	hr.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"gone.type"}`)}
	if err := f.client.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}

	if err := f.create(t, vmApplication(t, "new", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("a sibling that no longer evaluates must not block an unrelated write: %v", err)
	}
	err := f.create(t, vmApplication(t, "third", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("the stale sibling must keep its 4Gi, leaving no room for a third VM, got %v", err)
	}
}

func TestReservationGateDistrustsAFigureWrittenForOtherValues(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	// Resized while the gate was off: the stored 4Gi no longer describes it.
	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, hr); err != nil {
		t.Fatal(err)
	}
	hr.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.large"}`)}
	if err := f.client.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}

	err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.large"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "16Gi already reserved") {
		t.Fatalf("the resized VM reserves 16Gi, not the 4Gi stored before the edit; got %v", err)
	}
}

func TestReservationGateLeavesUndeclaredKindsAlone(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "1Gi"}))
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.client, f.client
	tenants.SetReservation(f.gate)
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	if _, err := tenants.Create(ctx, childApplication(t, "dev", "tenant-acme", nil), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("a kind with no declaration is not gated yet: %v", err)
	}
}

func TestReservationGateDoesNotStoreWithoutTheGate(t *testing.T) {
	f := newGateFixture(t)
	f.vms.SetReservation(nil)
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, hr); err != nil {
		t.Fatal(err)
	}
	if _, ok := hr.Annotations[ReservedAnnotation]; ok {
		t.Fatal("with the gate off nothing is recorded")
	}
}

func TestReservationAnnotationIsNotWritableThroughTheApplication(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	app := vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})
	app.Annotations = map[string]string{ReservedAnnotation: `{"valuesHash":"x","resources":{}}`}
	if err := f.create(t, app); !apierrors.IsInvalid(err) {
		t.Fatalf("an Application annotation must not stand in for the gate's figure, got %v", err)
	}
}

func TestReservationGateSerialisesWritesIntoOnePool(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.create(t, vmApplication(t, fmt.Sprintf("vm%d", i), "tenant-acme", map[string]any{"instanceType": "u1.small"}))
		}(i)
	}
	wg.Wait()
	admitted := 0
	for _, err := range errs {
		if err == nil {
			admitted++
		} else if !apierrors.IsInvalid(err) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 2 {
		t.Fatalf("an 8Gi pool admitted %d concurrent 4Gi VMs, want 2", admitted)
	}
}

func TestTenantQuotaDeclarationCountsReservationsWithTheGate(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "16Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.client, f.client
	tenants.SetReservation(f.gate)

	errs := tenants.validateTenantResourceQuotas(context.Background(), childApplication(t, "dev", "tenant-acme", map[string]string{"memory": "1Gi"}))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "16Gi currently in use by the parent pool") {
		t.Fatalf("the parent pool's 16Gi is reserved by its VM, so a 1Gi child does not fit; got %v", errs)
	}
}

func TestReservationAnnotationIsBounded(t *testing.T) {
	huge := reservation.ResourceList{}
	for i := 0; i < 2000; i++ {
		huge[fmt.Sprintf("example.com/resource-%04d", i)] = resource.MustParse("1")
	}
	adm := &reservationAdmission{stored: &storedReservation{ValuesHash: "x", Resources: huge}}
	hr := &helmv2.HelmRelease{}
	if err := adm.annotate(hr); err == nil {
		t.Fatal("an oversized reservation must be refused, not stored")
	}
	if _, ok := hr.Annotations[ReservedAnnotation]; ok {
		t.Fatal("an oversized reservation was written")
	}

	adm.stored.Resources = reservation.ResourceList{"memory": resource.MustParse("4Gi")}
	if err := adm.annotate(hr); err != nil {
		t.Fatalf("an ordinary reservation must be stored: %v", err)
	}
}

func TestReservationKeepsTheStoredFigureOfAKindWhoseDeclarationBroke(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	f.gate.evaluators["VMInstance"] = kindEvaluator{err: fmt.Errorf("consumption: does not compile")}

	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, hr); err != nil {
		t.Fatal(err)
	}
	rl, stale, err := f.gate.reservedBy(hr)
	mem := rl["memory"]
	if err != nil || !stale || mem.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatalf("a kind whose declaration broke must keep charging what it was admitted with: %v stale=%v err=%v", rl, stale, err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"})); !apierrors.IsInvalid(err) {
		t.Fatalf("a kind whose declaration broke must not be admitted, got %v", err)
	}
}

func TestReservationSettlesOnTheFigureWrittenNotOnlyItsValues(t *testing.T) {
	adm := &reservationAdmission{stored: &storedReservation{ValuesHash: "same", Resources: reservation.ResourceList{"memory": resource.MustParse("16Gi")}}}
	hr := &helmv2.HelmRelease{}
	hr.ResourceVersion = "7"
	if err := adm.annotate(hr); err != nil {
		t.Fatal(err)
	}
	written := writtenState(hr)
	cached := &helmv2.HelmRelease{}
	cached.ResourceVersion = "6"
	cached.Annotations = map[string]string{ReservedAnnotation: `{"valuesHash":"same","resources":{"memory":"4Gi"}}`}
	if written.visible(cached) {
		t.Fatal("a cached figure with the same values but another size is not the write")
	}
	statusOnly := hr.DeepCopy()
	statusOnly.ResourceVersion = "8"
	if !written.visible(hr) || !written.visible(statusOnly) {
		t.Fatal("the write, and a status update on top of it, show the written figure")
	}
}

func TestReservationOwesAPoolAWriteTheCacheHasNotShown(t *testing.T) {
	hidden := true
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if hidden && key.Name == "vm-instance-one" {
				return apierrors.NewNotFound(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.gate.settleTimeout = 100 * time.Millisecond
	// The API server has the release; only the cache does not.
	f.vms.w = f.client

	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("the write itself succeeds even if the cache is late: %v", err)
	}
	err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsTooManyRequests(err) {
		t.Fatalf("while a write is not visible the pool admits no growth; got %v", err)
	}

	hidden = false
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("once the cache shows the write the pool admits again: %v", err)
	}
	if err := f.create(t, vmApplication(t, "three", "tenant-acme", map[string]any{"instanceType": "u1.small"})); !apierrors.IsInvalid(err) {
		t.Fatalf("and counts it: the third 4Gi VM does not fit 8Gi, got %v", err)
	}
}

func TestReservationRefusesARetryOverAReleaseThatChanged(t *testing.T) {
	var f gateFixture
	conflicted := false
	f = newGateFixtureWith(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if !conflicted && obj.GetName() == "vm-instance-web" {
				conflicted = true
				// Another writer shrinks the VM between this update's
				// decision and its write.
				live := &helmv2.HelmRelease{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
					return err
				}
				live.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.small"}`)}
				if err := c.Update(ctx, live); err != nil {
					return err
				}
				return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), obj.GetName(), fmt.Errorf("stale"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large", "external": false}))
	if !apierrors.IsConflict(err) {
		t.Fatalf("an update decided against values that changed before its write must come back as a conflict, got %v", err)
	}
}

func TestReservationRetriesAStatusOnlyConflict(t *testing.T) {
	conflicted := false
	f := newGateFixtureWith(t, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if !conflicted && obj.GetName() == "vm-instance-web" {
				conflicted = true
				return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), obj.GetName(), fmt.Errorf("stale"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatalf("a conflict that left the values alone is retried as before: %v", err)
	}
}

func TestTenantCarveOutAndAnOrderIntoTheSamePoolAreSerialised(t *testing.T) {
	tenantChecked := make(chan struct{})
	f := newGateFixtureWith(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetName() == "tenant-dev" {
				// The carve-out has been checked and not stored yet: the
				// window in which an order into the same pool would see the
				// headroom the carve-out is about to take.
				close(tenantChecked)
				time.Sleep(200 * time.Millisecond)
			}
			return c.Create(ctx, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.vms.c, f.vms.w
	tenants.SetReservation(f.gate)

	var tenantErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := request.WithNamespace(context.Background(), "tenant-acme")
		_, tenantErr = tenants.Create(ctx, childApplication(t, "dev", "tenant-acme", map[string]string{"memory": "4Gi"}), nil, &metav1.CreateOptions{})
	}()
	<-tenantChecked
	vmErr := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	<-done

	if tenantErr != nil {
		t.Fatalf("the carve-out checked first must be admitted: %v", tenantErr)
	}
	if !apierrors.IsInvalid(vmErr) || !strings.Contains(vmErr.Error(), "4Gi carved out by sub-tenants") {
		t.Fatalf("the order must wait for the carve-out and then find no room, got %v", vmErr)
	}
}

func TestReservationDryRunLeavesNothingOwedToThePool(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.gate.settleTimeout = 300 * time.Millisecond
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	dryRun := []string{metav1.DryRunAll}

	start := time.Now()
	if _, err := f.vms.Create(ctx, vmApplication(t, "probe", "tenant-acme", map[string]any{"instanceType": "u1.small"}), nil, &metav1.CreateOptions{DryRun: dryRun}); err != nil {
		t.Fatalf("a dry run that fits is admitted: %v", err)
	}
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.vms.Update(ctx, "web", rest.DefaultUpdatedObjectInfo(vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})), nil, nil, false, &metav1.UpdateOptions{DryRun: dryRun}); !apierrors.IsInvalid(err) {
		t.Fatalf("a dry run that does not fit is refused like the write, got %v", err)
	}
	if _, _, err := f.vms.Update(ctx, "web", rest.DefaultUpdatedObjectInfo(vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small", "external": false})), nil, nil, false, &metav1.UpdateOptions{DryRun: dryRun}); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("a dry run stores nothing, so it must not leave the pool waiting for a write: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= f.gate.settleTimeout {
		t.Fatalf("dry runs waited for a write that never comes: %s", elapsed)
	}
}

// The delta of an update is computed from the release as the cache served
// it, before the pool lock. If that copy is stale, the write carries its
// stale resourceVersion and conflicts, and the retry finds the values moved.
func TestReservationAStaleBaselineCannotPassAGrowthAsAShrink(t *testing.T) {
	var snapshot *helmv2.HelmRelease
	staleReads := 0
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if snapshot != nil && staleReads > 0 && key.Name == "vm-instance-web" {
				staleReads--
				snapshot.DeepCopyInto(obj.(*helmv2.HelmRelease))
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	snapshot = &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, snapshot); err != nil {
		t.Fatal(err)
	}
	// web shrinks to 4Gi and a sibling takes the 16Gi it freed: the pool is full.
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}

	// The next update of web reads the 16Gi copy: 8Gi looks like a shrink.
	staleReads = 2
	err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.medium"}))
	if !apierrors.IsConflict(err) {
		t.Fatalf("an update decided against a stale copy must come back as a conflict, got %v", err)
	}
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.medium"})); !apierrors.IsInvalid(err) {
		t.Fatalf("sent again against the live release, 4Gi to 8Gi does not fit a full pool; got %v", err)
	}
}

// hideable serves VMInstance through a client that reports the named
// release missing while hidden is set, as a cache that has not seen it yet.
func hideable(t *testing.T, name string, hidden *bool, objects ...client.Object) gateFixture {
	t.Helper()
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if *hidden && key.Name == name {
				return apierrors.NewNotFound(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, objects...)
	f.gate.settleTimeout = 100 * time.Millisecond
	return f
}

func TestReservationDebtClearsOnAShrinkTheCacheShows(t *testing.T) {
	hidden := true
	f := hideable(t, "vm-instance-one", &hidden, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	hidden = false
	// The release is shrunk before the cache ever showed the 16Gi write:
	// the cache will only ever show the 4Gi one.
	if err := f.update(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatalf("the cache shows a later state of the owed release, so the pool admits again: %v", err)
	}
}

func TestReservationDebtClearsOnADeleteTheCacheShows(t *testing.T) {
	hidden := true
	f := hideable(t, "vm-instance-one", &hidden, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	hidden = false
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	if _, _, err := f.vms.Delete(ctx, "one", nil, &metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("the cache no longer has the release this server deleted, so the pool admits again: %v", err)
	}
}

func TestReservationDebtStillRefusesWhileTheWriteIsHidden(t *testing.T) {
	hidden := true
	f := hideable(t, "vm-instance-one", &hidden, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	// The API server has the release; only the cache does not.
	f.vms.w = f.client
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"})); !apierrors.IsTooManyRequests(err) {
			t.Fatalf("a release the API server has and the cache does not is still owed; want 429, got %v", err)
		}
	}
}

func TestReservationDebtClearsWhenAnotherClientDeletedTheRelease(t *testing.T) {
	hidden := true
	f := hideable(t, "vm-instance-one", &hidden, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.vms.w = f.client
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	// Deleted behind this server's back, before the cache ever showed it.
	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-one"}, hr); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("the API server and the cache agree the release is gone, so the pool admits again: %v", err)
	}
}

func TestReservationStaleSeesAFigureChangedUnderTheSameValues(t *testing.T) {
	cur := &helmv2.HelmRelease{}
	cur.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.small"}`)}
	cur.Annotations = map[string]string{ReservedAnnotation: `{"valuesHash":"h","resources":{"memory":"4Gi"}}`}
	adm := &reservationAdmission{}
	if err := adm.base(cur); err != nil {
		t.Fatal(err)
	}
	if adm.stale(cur) {
		t.Fatal("the release the decision was made against is not stale")
	}
	readmitted := cur.DeepCopy()
	readmitted.Annotations[ReservedAnnotation] = `{"valuesHash":"h","resources":{"memory":"2Gi"}}`
	if !adm.stale(readmitted) {
		t.Fatal("the same values readmitted with another figure change what the delta subtracted")
	}
}

func TestReservationBaselineIsTheReleaseTheClientNames(t *testing.T) {
	var snapshot *helmv2.HelmRelease
	staleReads := 0
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if snapshot != nil && staleReads > 0 && key.Name == "vm-instance-web" {
				staleReads--
				snapshot.DeepCopyInto(obj.(*helmv2.HelmRelease))
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	snapshot = &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatal(err)
	}
	live := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, live); err != nil {
		t.Fatal(err)
	}

	// The client names the live 4Gi release; the cache still serves 16Gi.
	staleReads = 2
	app := vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.medium"})
	app.ResourceVersion = live.ResourceVersion
	if err := f.update(t, app); !apierrors.IsInvalid(err) {
		t.Fatalf("4Gi to 8Gi in a full pool is a growth whatever the cache says; got %v", err)
	}
}

func TestTenantCreateDoesNotHoldThePoolAcrossTheAdmissionChain(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.vms.c, f.vms.w
	tenants.SetReservation(f.gate)

	orderDone := make(chan error, 1)
	slowWebhook := func(context.Context, runtime.Object) error {
		go func() {
			orderDone <- f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
		}()
		select {
		case err := <-orderDone:
			orderDone <- err
		case <-time.After(time.Second):
			t.Error("an order into the pool waited on a Tenant's admission chain")
		}
		return nil
	}
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	if _, err := tenants.Create(ctx, childApplication(t, "dev", "tenant-acme", map[string]string{"memory": "2Gi"}), slowWebhook, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := <-orderDone; err != nil {
		t.Fatal(err)
	}
}

func TestValuesHashKeepsLargeIntegersApart(t *testing.T) {
	a, err := valuesHash([]byte(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := valuesHash([]byte(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two integers a float64 cannot tell apart hashed alike, so a figure stored for one would be trusted for the other")
	}
	reordered, err := valuesHash([]byte(`{"b":1,"a":{"d":2,"c":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := valuesHash([]byte(`{"a":{"c":3,"d":2},"b":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if reordered != ordered {
		t.Fatal("the same values in another key order must hash alike")
	}
	if _, err := valuesHash([]byte(`{"a":1} {"a":2}`)); err == nil {
		t.Fatal("values carrying a second document must not hash as the first alone")
	}
}

// cacheBlind serves VMInstance through a client whose informer-side reads
// (the gate's waits, which carry no request namespace, and pool listings)
// do not see the named release while blind is set. Request reads still do,
// and the live client is the store itself.
func cacheBlind(t *testing.T, name string, blind *bool, objects ...client.Object) gateFixture {
	t.Helper()
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, fromRequest := request.NamespaceFrom(ctx); *blind && !fromRequest && key.Name == name {
				return apierrors.NewNotFound(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if hrs, ok := list.(*helmv2.HelmReleaseList); ok && *blind {
				kept := hrs.Items[:0]
				for _, hr := range hrs.Items {
					if hr.Name != name {
						kept = append(kept, hr)
					}
				}
				hrs.Items = kept
			}
			return nil
		},
	}, objects...)
	f.vms.w = f.client
	return f
}

func TestReservationDeleteDebtWaitsForTheAPIServerToDropTheRelease(t *testing.T) {
	blind := true
	f := cacheBlind(t, "vm-instance-one", &blind, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "4Gi"}))
	f.gate.settleTimeout = 100 * time.Millisecond
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{}
	key := client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-one"}
	if err := f.client.Get(context.Background(), key, hr); err != nil {
		t.Fatal(err)
	}
	hr.Finalizers = append(hr.Finalizers, "example.com/hold")
	if err := f.client.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	if _, _, err := f.vms.Delete(ctx, "one", nil, &metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsTooManyRequests(err) {
		t.Fatalf("a release terminating behind its finalizers is still stored and still owed; want 429, got %v", err)
	}
}

func TestReservationShrinkDuringTheWaitReplacesWhatIsWaitedFor(t *testing.T) {
	blind := true
	f := cacheBlind(t, "vm-instance-one", &blind, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	f.gate.settleTimeout = 400 * time.Millisecond

	created := make(chan error, 1)
	go func() {
		created <- f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.large"}))
	}()
	key := client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-one"}
	if err := wait.PollUntilContextTimeout(context.Background(), 5*time.Millisecond, time.Second, true, func(ctx context.Context) (bool, error) {
		return f.client.Get(ctx, key, &helmv2.HelmRelease{}) == nil, nil
	}); err != nil {
		t.Fatal("the growing write was never stored")
	}
	// The shrink lands while the growing write still waits for the cache,
	// and the cache then shows the shrink, never the 16Gi write.
	if err := f.update(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	blind = false
	shown := time.Now()
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	// The pool lock is held until the wait ends: it has to end when the
	// cache shows the shrink, not run out its timeout on the 16Gi write.
	if held := time.Since(shown); held >= f.gate.settleTimeout/2 {
		t.Fatalf("the growing write held the pool %s after the cache showed the shrink", held)
	}
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.large"})); err != nil {
		t.Fatalf("4Gi + 16Gi fits 20Gi once the cache shows the shrink: %v", err)
	}
}
