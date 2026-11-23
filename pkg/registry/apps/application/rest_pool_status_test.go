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
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/reservation"
)

const testPoolStatus = `{"reserved":{"cpu":"3","memory":"12Gi"},"carvedOut":{"cpu":"2"},"budget":{"cpu":"4","memory":"64Gi"},` +
	`"conditions":[{"type":"Overcommitted","status":"True","reason":"BudgetExceeded","message":"reserved and carved out exceed the budget by 1 cpu","lastTransitionTime":"2026-11-01T00:00:00Z"},` +
	`{"type":"ReservationIncomplete","status":"False","reason":"AllEvaluated","message":"every application in the pool evaluates","lastTransitionTime":"2026-11-01T00:00:00Z"}]}`

// newTenantUpdateREST serves Tenant over a fake client seeded with objects,
// with every type Update needs registered.
func newTenantUpdateREST(t *testing.T, objects ...client.Object) (*REST, client.Client) {
	t.Helper()
	r := newTenantREST(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{helmv2.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{
		{Application: config.ApplicationConfig{Kind: "Tenant"}},
	}}); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r.c, r.w = c, c
	return r, c
}

// TestUpdate_TenantKeepsThePoolStatus: the figures the controller published
// on a Tenant's release survive an update of that Tenant, rather than
// vanishing until the next sweep, and an Application annotation spelled like
// them lands under the Application prefix instead of overwriting them.
func TestUpdate_TenantKeepsThePoolStatus(t *testing.T) {
	live := tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "4"})
	live.Annotations = map[string]string{
		reservation.PoolAnnotation:     testPoolStatus,
		reservation.ReservedAnnotation: `{"valuesHash":"h","resources":{}}`,
	}
	r, c := newTenantUpdateREST(t, live)

	app := childApplication(t, "foo", "tenant-root", map[string]string{"cpu": "6"})
	app.Annotations = map[string]string{reservation.PoolAnnotation: `{"budget":{"cpu":"1000"}}`}
	ctx := request.WithNamespace(context.Background(), "tenant-root")
	if _, _, err := r.Update(ctx, "foo", newDefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(live), got); err != nil {
		t.Fatal(err)
	}
	if v := got.Annotations[reservation.PoolAnnotation]; v != testPoolStatus {
		t.Errorf("pool status after a tenant update = %q, want the controller's %q", v, testPoolStatus)
	}
	if _, ok := got.Annotations[reservation.ReservedAnnotation]; !ok {
		t.Error("the stored reservation was dropped by a tenant update")
	}
	if v := got.Annotations[AnnotationPrefix+reservation.PoolAnnotation]; v != `{"budget":{"cpu":"1000"}}` {
		t.Errorf("the Application's own annotation = %q, want it kept under the prefix", v)
	}
}

// TestConvert_ProjectsThePoolStatusIntoTenantStatus: a Tenant's status
// carries the pool figures and conditions published on its release.
func TestConvert_ProjectsThePoolStatusIntoTenantStatus(t *testing.T) {
	hr := tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "4"})
	hr.Annotations = map[string]string{reservation.PoolAnnotation: testPoolStatus}
	r, _ := newTenantUpdateREST(t, hr)

	app, err := r.ConvertHelmReleaseToApplication(context.Background(), hr)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		got  corev1.ResourceList
		want map[string]string
	}{
		"reserved":  {app.Status.Reserved, map[string]string{"cpu": "3", "memory": "12Gi"}},
		"carvedOut": {app.Status.CarvedOut, map[string]string{"cpu": "2"}},
		"budget":    {app.Status.Budget, map[string]string{"cpu": "4", "memory": "64Gi"}},
	} {
		if len(tc.got) != len(tc.want) {
			t.Errorf("status.%s = %v, want %v", name, tc.got, tc.want)
		}
		for k, v := range tc.want {
			if q := tc.got[corev1.ResourceName(k)]; q.Cmp(resource.MustParse(v)) != 0 {
				t.Errorf("status.%s[%s] = %s, want %s", name, k, q.String(), v)
			}
		}
	}
	over := meta.FindStatusCondition(app.Status.Conditions, reservation.ConditionOvercommitted)
	if over == nil || over.Status != metav1.ConditionTrue || over.Reason != "BudgetExceeded" {
		t.Errorf("Overcommitted condition = %+v, want True/BudgetExceeded", over)
	}
	if meta.FindStatusCondition(app.Status.Conditions, reservation.ConditionReservationIncomplete) == nil {
		t.Error("ReservationIncomplete condition not projected")
	}
	if _, ok := app.Annotations[reservation.PoolAnnotation]; ok {
		t.Error("the pool status leaked into the Tenant's metadata")
	}
}

// TestConvert_NoPoolStatusProjectsNothing: a Tenant whose release carries no
// pool status, as with the reservation off, has none in its status.
func TestConvert_NoPoolStatusProjectsNothing(t *testing.T) {
	hr := tenantHelmRelease(t, "foo", "tenant-root", map[string]string{"cpu": "4"})
	r, _ := newTenantUpdateREST(t, hr)

	app, err := r.ConvertHelmReleaseToApplication(context.Background(), hr)
	if err != nil {
		t.Fatal(err)
	}
	if app.Status.Reserved != nil || app.Status.Budget != nil || app.Status.CarvedOut != nil {
		t.Errorf("figures projected without a pool status: %+v", app.Status)
	}
	if meta.FindStatusCondition(app.Status.Conditions, reservation.ConditionOvercommitted) != nil {
		t.Error("Overcommitted projected without a pool status")
	}
}
