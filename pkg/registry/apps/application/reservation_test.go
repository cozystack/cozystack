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
			"u1.small": {"cpu": resource.MustParse("1"), "memory": resource.MustParse("4Gi")},
			"u1.large": {"cpu": resource.MustParse("4"), "memory": resource.MustParse("16Gi")},
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
	gate := NewReservation([]config.Resource{res}, testEnvironment())
	vms := NewREST(c, c, &res)
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
