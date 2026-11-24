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
	"sync/atomic"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "4Gi already reserved by 1 applications, 16Gi requested") {
		t.Fatalf("db's 4Gi + web's new 16Gi does not fit 16Gi; want the growth refused, got %v", err)
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

	err := tenants.checkTenantQuota(context.Background(), childApplication(t, "dev", "tenant-acme", map[string]string{"memory": "1Gi"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "16Gi reserved by 1 applications") {
		t.Fatalf("the parent pool's 16Gi is reserved by its VM, so a 1Gi child does not fit; got %v", err)
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
	rl, stale, err := f.gate.reservedBy(hr, f.gate.env)
	mem := rl["memory"]
	if err != nil || !stale || mem.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatalf("a kind whose declaration broke must keep charging what it was admitted with: %v stale=%v err=%v", rl, stale, err)
	}
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"})); !apierrors.IsInvalid(err) {
		t.Fatalf("a kind whose declaration broke must not be admitted, got %v", err)
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
	select {
	case <-tenantChecked:
	case <-done:
		t.Fatalf("the Tenant was refused before its write: %v", tenantErr)
	}
	vmErr := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	<-done

	if tenantErr != nil {
		t.Fatalf("the carve-out checked first must be admitted: %v", tenantErr)
	}
	if !apierrors.IsInvalid(vmErr) || !strings.Contains(vmErr.Error(), "4Gi carved out by sub-tenants") {
		t.Fatalf("the order must wait for the carve-out and then find no room, got %v", vmErr)
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
	for _, trailing := range []string{`{"n":1} {"n":2}`, `{"n":1}]`, `{"n":1}}`, `{"n":1} x`} {
		if _, err := valuesHash([]byte(trailing)); err == nil {
			t.Fatalf("%s must not hash as its first document alone", trailing)
		}
	}
	if _, err := valuesHash([]byte("{\"n\":1} \n")); err != nil {
		t.Fatalf("trailing whitespace is not data: %v", err)
	}
}

func TestTenantPoolRootIsCheckedAgainUnderTheLock(t *testing.T) {
	var armed atomic.Bool
	chose := make(chan struct{})
	var once sync.Once
	f := newGateFixtureWith(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			err := c.List(ctx, list, opts...)
			if armed.Load() {
				// The cached tree the root is chosen from has been read.
				once.Do(func() { close(chose) })
			}
			return err
		},
	},
		tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}),
		tenantHelmRelease(t, "dev", "tenant-acme", nil),
	)
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.vms.c, f.client
	tenants.SetReservation(f.gate)

	// Hold tenant-acme's lock while tenant-acme-dev becomes bounded: a child
	// of dev chose tenant-acme as its root, which is no longer its pool.
	var releaseOnce sync.Once
	held := f.gate.acquire(context.Background(), "tenant-acme")
	unlock := func() { releaseOnce.Do(held) }
	// A failed step below must not leave the worker blocked on the lock.
	t.Cleanup(unlock)
	armed.Store(true)
	done := make(chan error, 1)
	go func() {
		ctx := request.WithNamespace(context.Background(), "tenant-acme-dev")
		_, err := tenants.Create(ctx, childApplication(t, "qa", "tenant-acme-dev", map[string]string{"memory": "1Gi"}), nil, &metav1.CreateOptions{})
		done <- err
	}()
	select {
	case <-chose:
	case err := <-done:
		unlock()
		t.Fatalf("the Tenant never chose its pool: %v", err)
	}
	dev := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "tenant-dev"}, dev); err != nil {
		t.Fatal(err)
	}
	dev.Spec.Values = tenantValuesJSON(t, map[string]string{"memory": "8Gi"})
	if err := f.client.Update(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; !apierrors.IsConflict(err) {
		t.Fatalf("a Tenant whose pool root changed while it waited must be asked to retry, got %v", err)
	}
}

func TestReservationExcludesTheReleaseWrittenFromItsPool(t *testing.T) {
	var snapshot *helmv2.HelmRelease
	staleList := false
	f := newGateFixtureWith(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if hrs, ok := list.(*helmv2.HelmReleaseList); ok && staleList && snapshot != nil {
				for i := range hrs.Items {
					if hrs.Items[i].Name == snapshot.Name {
						snapshot.DeepCopyInto(&hrs.Items[i])
					}
				}
			}
			return nil
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
	if err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}

	// The pool listing still carries web at 16Gi; web itself is 4Gi.
	staleList = true
	if err := f.update(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.medium"})); err != nil {
		t.Fatalf("db's 4Gi + web's new 8Gi fits 20Gi whatever the listing says about web: %v", err)
	}
}

type unavailableEnvironment struct{ reservation.Environment }

func (unavailableEnvironment) Instancetype(string) (reservation.ResourceList, error) {
	return nil, fmt.Errorf("list instance types: %w", reservation.ErrUnavailable)
}

func TestReservationRefusesToSumAPoolItCannotEvaluate(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	// A sibling stored without the gate's figure, so it has to be evaluated.
	sibling := &helmv2.HelmRelease{}
	sibling.Name, sibling.Namespace = "vm-instance-old", "tenant-acme"
	sibling.Labels = map[string]string{ApplicationKindLabel: "VMInstance", ApplicationGroupLabel: appsv1alpha1.GroupName, ApplicationNameLabel: "old"}
	sibling.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.small"}`)}
	if err := f.client.Create(context.Background(), sibling); err != nil {
		t.Fatal(err)
	}
	f.gate.env = unavailableEnvironment{testEnvironment()}

	err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"resources": map[string]any{"cpu": 1, "sockets": 1, "memory": "4Gi"}}))
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("a pool whose members cannot be evaluated right now is not summed short; want 503, got %v", err)
	}
}

func TestReservationDryRunIsAdmittedAndStoresNothing(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	dryRun := &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	if _, err := f.vms.Create(ctx, vmApplication(t, "probe", "tenant-acme", map[string]any{"instanceType": "u1.small"}), nil, dryRun); err != nil {
		t.Fatalf("a dry run that fits is admitted: %v", err)
	}
	if _, err := f.vms.Create(ctx, vmApplication(t, "big", "tenant-acme", map[string]any{"instanceType": "u1.large"}), nil, dryRun); !apierrors.IsInvalid(err) {
		t.Fatalf("a dry run that does not fit is refused like the write, got %v", err)
	}
	for _, name := range []string{"one", "two"} {
		if err := f.create(t, vmApplication(t, name, "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
			t.Fatalf("a dry run reserves nothing, so the pool still holds two 4Gi VMs: %v", err)
		}
	}
}

// The gate reads the pool from the API server once it holds the lock, so a
// write the informer cache has not shown yet is counted all the same.
func TestReservationCountsWritesTheCacheHasNotShown(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	var hidden atomic.Bool
	f.vms.c = interceptor.NewClient(f.client, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if hidden.Load() && key.Name == "vm-instance-one" {
				return apierrors.NewNotFound(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if hrs, ok := list.(*helmv2.HelmReleaseList); ok && hidden.Load() {
				kept := hrs.Items[:0]
				for _, hr := range hrs.Items {
					if hr.Name != "vm-instance-one" {
						kept = append(kept, hr)
					}
				}
				hrs.Items = kept
			}
			return nil
		},
	})
	f.vms.w = f.client
	hidden.Store(true)

	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.large"})); !apierrors.IsInvalid(err) {
		t.Fatalf("16Gi does not fit 8Gi, got %v", err)
	}
	if err := f.create(t, vmApplication(t, "one", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	// The cache still does not have "one".
	if err := f.create(t, vmApplication(t, "two", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("4Gi + 4Gi fits 8Gi: %v", err)
	}
	err := f.create(t, vmApplication(t, "three", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "8Gi already reserved by 2 applications") {
		t.Fatalf("the pool counts the write the cache has not shown; want the third VM refused, got %v", err)
	}
}

// A carve-out the cache has not shown yet is counted by an order into the
// same pool, since the tenant tree is read from the API server too.
func TestReservationCountsACarveOutTheCacheHasNotShown(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.vms.c = interceptor.NewClient(f.client, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if hrs, ok := list.(*helmv2.HelmReleaseList); ok {
				kept := hrs.Items[:0]
				for _, hr := range hrs.Items {
					if hr.Name != "tenant-dev" {
						kept = append(kept, hr)
					}
				}
				hrs.Items = kept
			}
			return nil
		},
	})
	f.vms.w = f.client
	if err := f.client.Create(context.Background(), tenantHelmRelease(t, "dev", "tenant-acme", map[string]string{"memory": "6Gi"})); err != nil {
		t.Fatal(err)
	}
	err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "6Gi carved out by sub-tenants") {
		t.Fatalf("8Gi - 6Gi carved out leaves no room for 4Gi; got %v", err)
	}
}

type countingEnvironment struct {
	reservation.Environment
	calls map[string]int
}

func (e *countingEnvironment) Instancetype(name string) (reservation.ResourceList, error) {
	e.calls[name]++
	if e.calls[name] > 1 {
		// Redefined between two lookups of the same admission.
		return reservation.ResourceList{"cpu": resource.MustParse("1"), "memory": resource.MustParse("2Gi")}, nil
	}
	return e.Environment.Instancetype(name)
}

func TestReservationResolvesEachInstancetypeOncePerAdmission(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"resources": map[string]any{"cpu": 1, "sockets": 1, "memory": "2Gi"}})); err != nil {
		t.Fatal(err)
	}
	// web is changed behind the gate to u1.small: its stored 2Gi no longer
	// matches its values, so the pool has to evaluate it, in the same
	// admission that evaluates db.
	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, hr); err != nil {
		t.Fatal(err)
	}
	hr.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.small"}`)}
	if err := f.client.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	env := &countingEnvironment{Environment: testEnvironment(), calls: map[string]int{}}
	f.gate.env = env
	lowerBudget(t, f, "6Gi")

	// web and db both reserve u1.small's 4Gi: 8Gi exceeds 6Gi. Leaving web
	// out, trusting its stale 2Gi, or reading the redefined 2Gi for either
	// would fit.
	err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "4Gi already reserved by 1 applications, 4Gi requested") {
		t.Fatalf("both VMs read from one definition of u1.small: want db refused at 4Gi + 4Gi, got %v", err)
	}
	if env.calls["u1.small"] != 1 {
		t.Fatalf("u1.small was resolved %d times in one admission", env.calls["u1.small"])
	}
}

func lowerBudget(t *testing.T, f gateFixture, memory string) {
	t.Helper()
	tenant := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: rootTenantNamespace, Name: "tenant-acme"}, tenant); err != nil {
		t.Fatal(err)
	}
	tenant.Spec.Values = tenantValuesJSON(t, map[string]string{"memory": memory})
	if err := f.client.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
}

func gatedTenants(t *testing.T, f gateFixture) *REST {
	t.Helper()
	tenants := newTenantREST(t)
	tenants.c, tenants.w = f.vms.c, f.vms.w
	tenants.SetReservation(f.gate)
	return tenants
}

func TestTenantCarveOutIsCheckedAgainstTheNearestBoundedAncestor(t *testing.T) {
	f := newGateFixture(t,
		tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}),
		tenantHelmRelease(t, "dev", "tenant-acme", nil),
	)
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	ctx := request.WithNamespace(context.Background(), "tenant-acme-dev")
	_, err := gatedTenants(t, f).Create(ctx, childApplication(t, "qa", "tenant-acme-dev", map[string]string{"memory": "6Gi"}), nil, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), `tenant pool "tenant-acme"`) {
		t.Fatalf("dev declares nothing, so qa's 6Gi is carved out of acme's 8Gi with 4Gi reserved; want it refused, got %v", err)
	}
}

func TestTenantCarveOutComparesQuotaKeysAsTheBudgetNamesThem(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatal(err)
	}
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	tenants := gatedTenants(t, f)
	_, err := tenants.Create(ctx, childApplication(t, "dev", "tenant-acme", map[string]string{"requests.memory": "6Gi"}), nil, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), `remaining "memory" budget of tenant pool "tenant-acme"`) || !strings.Contains(err.Error(), "4Gi reserved by 1 applications") {
		t.Fatalf("requests.memory draws on the parent's memory budget; want 6Gi refused on it next to 4Gi reserved in 8Gi, got %v", err)
	}
	if _, err := tenants.Create(ctx, childApplication(t, "qa", "tenant-acme", map[string]string{"requests.memory": "4Gi"}), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("4Gi of requests.memory fits the 4Gi left of the memory budget: %v", err)
	}
}

func TestTenantCarveOutAnswers503WhenThePoolCannotBeSummed(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	sibling := &helmv2.HelmRelease{}
	sibling.Name, sibling.Namespace = "vm-instance-old", "tenant-acme"
	sibling.Labels = map[string]string{ApplicationKindLabel: "VMInstance", ApplicationGroupLabel: appsv1alpha1.GroupName, ApplicationNameLabel: "old"}
	sibling.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.small"}`)}
	if err := f.client.Create(context.Background(), sibling); err != nil {
		t.Fatal(err)
	}
	f.gate.env = unavailableEnvironment{testEnvironment()}
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	_, err := gatedTenants(t, f).Create(ctx, childApplication(t, "dev", "tenant-acme", map[string]string{"memory": "1Gi"}), nil, &metav1.CreateOptions{})
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("a pool that cannot be summed right now is a 503, not an invalid spec; got %v", err)
	}
}

// A budget the cache has not seen yet still gates the write: the pool root
// is looked up live when the cache knows none.
func TestReservationFindsABudgetTheCacheHasNotSeen(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.vms.c = interceptor.NewClient(f.client, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if hrs, ok := list.(*helmv2.HelmReleaseList); ok {
				for i := range hrs.Items {
					if hrs.Items[i].Name == "tenant-acme" {
						hrs.Items[i].Spec.Values = tenantValuesJSON(t, nil)
					}
				}
			}
			return nil
		},
	})
	f.vms.w = f.client
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.large"})); !apierrors.IsInvalid(err) {
		t.Fatalf("the cache shows acme unbounded but the API server has 8Gi; want 16Gi refused, got %v", err)
	}
}

func TestTenantDroppingItsQuotaWaitsForOrdersInFlightInItsPool(t *testing.T) {
	f := newGateFixture(t,
		tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}),
		tenantHelmRelease(t, "dev", "tenant-acme", map[string]string{"memory": "8Gi"}),
	)
	tenants := gatedTenants(t, f)
	// An order admitted into dev's pool, not written yet, holds its lock.
	var releaseOnce sync.Once
	held := f.gate.acquire(context.Background(), "tenant-acme-dev")
	unlock := func() { releaseOnce.Do(held) }
	t.Cleanup(unlock)
	waiting := make(chan struct{})
	var once sync.Once
	f.gate.waiting = func(root string) {
		if root == "tenant-acme-dev" {
			once.Do(func() { close(waiting) })
		}
	}
	done := make(chan error, 1)
	go func() {
		ctx := request.WithNamespace(context.Background(), "tenant-acme")
		_, _, err := tenants.Update(ctx, "dev", rest.DefaultUpdatedObjectInfo(childApplication(t, "dev", "tenant-acme", nil)), nil, nil, false, &metav1.UpdateOptions{})
		done <- err
	}()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("dropping dev's quota did not wait for dev's pool: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("dropping dev's quota never asked for dev's pool")
	}
	select {
	case err := <-done:
		t.Fatalf("dropping dev's quota finished while an order into dev was in flight: %v", err)
	default:
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("once the order is written, dev may drop its quota: %v", err)
	}
}

func TestReservationDoesNotTrustAStaleFigureWhenTheEnvironmentIsUnreadable(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	if err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"resources": map[string]any{"cpu": 1, "sockets": 1, "memory": "2Gi"}})); err != nil {
		t.Fatal(err)
	}
	// Resized behind the gate: the stored 2Gi is stale, and what web
	// reserves now depends on an instance type that cannot be read.
	hr := &helmv2.HelmRelease{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "tenant-acme", Name: "vm-instance-web"}, hr); err != nil {
		t.Fatal(err)
	}
	hr.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"instanceType":"u1.large"}`)}
	if err := f.client.Update(context.Background(), hr); err != nil {
		t.Fatal(err)
	}
	f.gate.env = unavailableEnvironment{testEnvironment()}

	err := f.create(t, vmApplication(t, "db", "tenant-acme", map[string]any{"resources": map[string]any{"cpu": 1, "sockets": 1, "memory": "4Gi"}}))
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("a sibling whose current size cannot be read is not counted at its stale 2Gi; want 503, got %v", err)
	}
}

func TestRootTenantWithAQuotaCanBeUpdated(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "root", rootTenantNamespace, map[string]string{"memory": "20Gi"}))
	tenants := gatedTenants(t, f)
	done := make(chan error, 1)
	go func() {
		ctx := request.WithNamespace(context.Background(), rootTenantNamespace)
		_, _, err := tenants.Update(ctx, "root", rest.DefaultUpdatedObjectInfo(childApplication(t, "root", rootTenantNamespace, map[string]string{"memory": "30Gi"})), nil, nil, false, &metav1.UpdateOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("updating the root Tenant locked its own pool twice and never returned")
	}
	if err := f.create(t, vmApplication(t, "web", rootTenantNamespace, map[string]any{"instanceType": "u1.small"})); err != nil {
		t.Fatalf("the root pool is usable after the update: %v", err)
	}
}

func TestReservationAnswers503WhenThePoolCannotBeRead(t *testing.T) {
	f := newGateFixture(t, tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "8Gi"}))
	f.vms.w = interceptor.NewClient(f.client, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fmt.Errorf("connection refused")
		},
	})
	err := f.create(t, vmApplication(t, "web", "tenant-acme", map[string]any{"instanceType": "u1.small"}))
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("a pool the API server cannot list right now is a 503, not an invalid spec; got %v", err)
	}
}

func TestTenantUpdateRefusesALockChosenFromAStaleCopy(t *testing.T) {
	stale := true
	f := newGateFixtureWith(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if stale && key.Name == "tenant-dev" {
				// The first read, which chooses the locks, predates dev's quota.
				stale = false
				obj.(*helmv2.HelmRelease).Spec.Values = tenantValuesJSON(t, nil)
			}
			return nil
		},
	},
		tenantHelmRelease(t, "acme", rootTenantNamespace, map[string]string{"memory": "20Gi"}),
		tenantHelmRelease(t, "dev", "tenant-acme", map[string]string{"memory": "8Gi"}),
	)
	ctx := request.WithNamespace(context.Background(), "tenant-acme")
	_, _, err := gatedTenants(t, f).Update(ctx, "dev", rest.DefaultUpdatedObjectInfo(childApplication(t, "dev", "tenant-acme", nil)), nil, nil, false, &metav1.UpdateOptions{})
	if !apierrors.IsConflict(err) {
		t.Fatalf("dev's quota appeared after its locks were chosen; want the drop refused as a conflict, got %v", err)
	}
}
