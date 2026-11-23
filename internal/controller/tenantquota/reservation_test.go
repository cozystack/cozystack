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

package tenantquota

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// vmInstanceDefinition is the shipped VMInstance ApplicationDefinition,
// consumption included.
func vmInstanceDefinition(t *testing.T) *cozyv1alpha1.ApplicationDefinition {
	t.Helper()
	raw, err := os.ReadFile("../../../packages/system/vm-instance-rd/cozyrds/vm-instance.yaml")
	if err != nil {
		t.Fatal(err)
	}
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := yaml.Unmarshal(raw, def); err != nil {
		t.Fatal(err)
	}
	if def.Spec.Consumption == nil {
		t.Fatal("the shipped vm-instance definition declares no consumption")
	}
	return def
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

func jsonValues(t *testing.T, values map[string]any) *apiextv1.JSON {
	t.Helper()
	if values == nil {
		return nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return &apiextv1.JSON{Raw: raw}
}

// tenantWithQuotas is a tenant HelmRelease whose values declare quotas, the
// budget the reservation oracle reads. nil quotas is an unbounded tenant.
func tenantWithQuotas(t *testing.T, appName, namespace string, quotas map[string]string) *helmv2.HelmRelease {
	t.Helper()
	hr := tenantHR(appName, namespace)
	hr.Labels[appsv1alpha1.ApplicationGroupLabel] = appsv1alpha1.GroupName
	if quotas != nil {
		q := map[string]any{}
		for k, v := range quotas {
			q[k] = v
		}
		hr.Spec.Values = jsonValues(t, map[string]any{"resourceQuotas": q})
	}
	return hr
}

// vm is a VMInstance release, as the apiserver stores it, with no stored
// reservation: what a release the gate never admitted looks like.
func vm(t *testing.T, name, namespace, instanceType string) *helmv2.HelmRelease {
	t.Helper()
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-instance-" + name,
			Namespace: namespace,
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "VMInstance",
				appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
				appsv1alpha1.ApplicationNameLabel:  name,
			},
		},
		Spec: helmv2.HelmReleaseSpec{Values: jsonValues(t, map[string]any{"instanceType": instanceType})},
	}
}

// withStored stores a figure on hr. A matching figure is hashed from hr's
// values; otherwise it is tied to values hr no longer has.
func withStored(t *testing.T, hr *helmv2.HelmRelease, matching bool, rl map[string]string) *helmv2.HelmRelease {
	t.Helper()
	hash := "computed-from-other-values"
	if matching {
		var err error
		if hash, err = reservation.ValuesHash(hr.Spec.Values.Raw); err != nil {
			t.Fatal(err)
		}
	}
	s := &reservation.Stored{ValuesHash: hash, Resources: reservation.ResourceList{}}
	for k, v := range rl {
		s.Resources[k] = resource.MustParse(v)
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[reservation.ReservedAnnotation] = raw
	return hr
}

func newReservationReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, helmv2.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	objs = append(objs, vmInstanceDefinition(t))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	rec := record.NewFakeRecorder(16)
	return &Reconciler{Client: c, Scheme: scheme, Recorder: rec, Reservation: true, Env: testEnvironment()}, c, rec
}

func sweep(t *testing.T, r *Reconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), sweepKey); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getRelease(t *testing.T, c client.Client, namespace, name string) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, hr); err != nil {
		t.Fatal(err)
	}
	return hr
}

// poolStatusOf reads the pool status published on a tenant's release, or
// nil when it carries none.
func poolStatusOf(t *testing.T, c client.Client, namespace, name string) *reservation.PoolStatus {
	t.Helper()
	s, err := reservation.ReadPoolStatus(getRelease(t, c, namespace, name).Annotations)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertFigure(t *testing.T, what string, rl reservation.ResourceList, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got := rl[k]
		if got.Cmp(resource.MustParse(v)) != 0 {
			t.Errorf("%s[%s] = %s, want %s", what, k, got.String(), v)
		}
	}
	for k, got := range rl {
		if _, ok := want[k]; !ok && !got.IsZero() {
			t.Errorf("%s[%s] = %s, want none", what, k, got.String())
		}
	}
}

func conditionOf(t *testing.T, s *reservation.PoolStatus, condType string) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(s.Conditions, condType)
	if c == nil {
		t.Fatalf("pool status has no %s condition: %+v", condType, s.Conditions)
	}
	return c
}

// TestReservation_PoolFigures: a pool's reserved figure sums what the
// applications in all its member namespaces reserve, its budget is the
// root's resourceQuotas values, and a bounded sub-tenant's quota is carved
// out of it instead of being a member.
func TestReservation_PoolFigures(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10", "memory": "64Gi"}),
		tenantWithQuotas(t, "bar", "tenant-foo", nil),
		tenantWithQuotas(t, "baz", "tenant-foo", map[string]string{"cpu": "2"}),
		vm(t, "a", "tenant-foo", "u1.small"),
		vm(t, "b", "tenant-foo-bar", "u1.large"),
		// baz is its own pool: its VM is not foo's.
		vm(t, "c", "tenant-foo-baz", "u1.small"),
	)
	sweep(t, r)

	foo := poolStatusOf(t, c, "tenant-root", "tenant-foo")
	if foo == nil {
		t.Fatal("tenant foo roots a pool but carries no pool status")
	}
	assertFigure(t, "reserved", foo.Reserved, map[string]string{"cpu": "5", "memory": "20Gi"})
	assertFigure(t, "carvedOut", foo.CarvedOut, map[string]string{"cpu": "2"})
	assertFigure(t, "budget", foo.Budget, map[string]string{"cpu": "10", "memory": "64Gi"})
	if cond := conditionOf(t, foo, reservation.ConditionOvercommitted); cond.Status != metav1.ConditionFalse {
		t.Errorf("Overcommitted = %s, want False: %s", cond.Status, cond.Message)
	}
	if cond := conditionOf(t, foo, reservation.ConditionReservationIncomplete); cond.Status != metav1.ConditionFalse {
		t.Errorf("ReservationIncomplete = %s, want False: %s", cond.Status, cond.Message)
	}

	baz := poolStatusOf(t, c, "tenant-foo", "tenant-baz")
	if baz == nil {
		t.Fatal("tenant baz roots a pool but carries no pool status")
	}
	assertFigure(t, "baz reserved", baz.Reserved, map[string]string{"cpu": "1", "memory": "4Gi"})

	if s := poolStatusOf(t, c, "tenant-foo", "tenant-bar"); s != nil {
		t.Errorf("unbounded tenant bar roots no pool, yet carries %+v", s)
	}
}

// TestReservation_StoresTheFigureOfAReleaseTheGateNeverAdmitted: a release
// with no stored figure, as a platform chart renders it, is evaluated and
// the figure is stored on it, tied to its values.
func TestReservation_StoresTheFigureOfAReleaseTheGateNeverAdmitted(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		vm(t, "a", "tenant-foo", "u1.large"),
	)
	sweep(t, r)

	hr := getRelease(t, c, "tenant-foo", "vm-instance-a")
	s, err := reservation.ReadStored(hr.Annotations)
	if err != nil || s == nil {
		t.Fatalf("no figure stored on a release the gate never admitted (err %v)", err)
	}
	if !s.Matches(hr.Spec.Values.Raw) {
		t.Errorf("the stored figure is not tied to the release's values")
	}
	assertFigure(t, "stored", s.Resources, map[string]string{"cpu": "4", "memory": "16Gi"})
}

// TestReservation_TrustsAStoredFigureOnlyWhileItMatches: a figure whose hash
// matches the values is used as stored, without re-evaluation; one computed
// from other values is re-evaluated and replaced.
func TestReservation_TrustsAStoredFigureOnlyWhileItMatches(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "100"}),
		withStored(t, vm(t, "kept", "tenant-foo", "u1.small"), true, map[string]string{"cpu": "7"}),
		withStored(t, vm(t, "stale", "tenant-foo", "u1.small"), false, map[string]string{"cpu": "30"}),
	)
	sweep(t, r)

	foo := poolStatusOf(t, c, "tenant-root", "tenant-foo")
	assertFigure(t, "reserved", foo.Reserved, map[string]string{"cpu": "8", "memory": "4Gi"})

	s, err := reservation.ReadStored(getRelease(t, c, "tenant-foo", "vm-instance-stale").Annotations)
	if err != nil {
		t.Fatal(err)
	}
	assertFigure(t, "re-stored", s.Resources, map[string]string{"cpu": "1", "memory": "4Gi"})
}

// TestReservation_BudgetComesFromValuesNotTheRenderedQuota: the rendered
// tenant-quota, in pod keys with allocation ratios applied, is not read; the
// tenant's own resourceQuotas are, mapped onto reservation keys.
func TestReservation_BudgetComesFromValuesNotTheRenderedQuota(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10", "requests.storage": "100Gi"}),
		chartQuota("tenant-foo", map[string]string{"limits.cpu": "40", "requests.cpu": "1"}, map[string]string{"requests.cpu": "1"}),
	)
	sweep(t, r)

	assertFigure(t, "budget", poolStatusOf(t, c, "tenant-root", "tenant-foo").Budget,
		map[string]string{"cpu": "10", "storage": "100Gi"})
}

// TestReservation_WritesNoAllocatedQuotaAndCollectsOldOnes: a shared pool,
// which the legacy path clamps with tenant-quota-allocated, gets no such
// object, and the ones the legacy path wrote, with their LimitRange copies,
// are deleted. The two never coexist.
func TestReservation_WritesNoAllocatedQuotaAndCollectsOldOnes(t *testing.T) {
	managed := map[string]string{managedByLabel: managedByValue}
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		tenantWithQuotas(t, "bar", "tenant-foo", nil),
		ns("tenant-foo"), ns("tenant-foo-bar"),
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: allocatedQuotaName, Namespace: "tenant-foo-bar", Labels: managed}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: allocatedLimitRangeName, Namespace: "tenant-foo-bar", Labels: managed}},
	)
	sweep(t, r)

	quotas := &corev1.ResourceQuotaList{}
	if err := c.List(context.Background(), quotas); err != nil {
		t.Fatal(err)
	}
	for _, q := range quotas.Items {
		if q.Name == allocatedQuotaName {
			t.Errorf("%s/%s exists with the reservation oracle on", q.Namespace, q.Name)
		}
	}
	ranges := &corev1.LimitRangeList{}
	if err := c.List(context.Background(), ranges); err != nil {
		t.Fatal(err)
	}
	for _, lr := range ranges.Items {
		if lr.Name == allocatedLimitRangeName {
			t.Errorf("%s/%s exists with the reservation oracle on", lr.Namespace, lr.Name)
		}
	}
}

// TestReservation_ReportsOvercommit: a pool whose applications and
// sub-tenant slices exceed its budget, which the gate cannot prevent when a
// parent lowers its quota, is reported Overcommitted, with an event, and
// nothing is evicted.
func TestReservation_ReportsOvercommit(t *testing.T) {
	r, c, rec := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "4"}),
		tenantWithQuotas(t, "baz", "tenant-foo", map[string]string{"cpu": "2"}),
		ns("tenant-foo"),
		vm(t, "a", "tenant-foo", "u1.large"),
	)
	sweep(t, r)

	cond := conditionOf(t, poolStatusOf(t, c, "tenant-root", "tenant-foo"), reservation.ConditionOvercommitted)
	if cond.Status != metav1.ConditionTrue || !strings.Contains(cond.Message, "2 cpu") {
		t.Errorf("Overcommitted = %s %q, want True by 2 cpu", cond.Status, cond.Message)
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "QuotaOvercommitted") {
			t.Errorf("event %q, want QuotaOvercommitted", ev)
		}
	default:
		t.Error("no event recorded for an overcommitted pool")
	}

	// A second sweep over the same state leaves the condition's transition
	// time alone.
	first := cond.LastTransitionTime
	sweep(t, r)
	again := conditionOf(t, poolStatusOf(t, c, "tenant-root", "tenant-foo"), reservation.ConditionOvercommitted)
	if !again.LastTransitionTime.Equal(&first) {
		t.Errorf("transition time moved from %v to %v with nothing changed", first, again.LastTransitionTime)
	}
}

// TestReservation_ReportsApplicationsThatDoNotEvaluate: an application that
// does not evaluate keeps its last stored figure, or is left out with none,
// and either way the pool says its figure is incomplete.
func TestReservation_ReportsApplicationsThatDoNotEvaluate(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "100"}),
		withStored(t, vm(t, "kept", "tenant-foo", "x1.gone"), false, map[string]string{"cpu": "3"}),
		vm(t, "lost", "tenant-foo", "x1.gone"),
	)
	sweep(t, r)

	foo := poolStatusOf(t, c, "tenant-root", "tenant-foo")
	assertFigure(t, "reserved", foo.Reserved, map[string]string{"cpu": "3"})
	cond := conditionOf(t, foo, reservation.ConditionReservationIncomplete)
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("ReservationIncomplete = %s, want True", cond.Status)
	}
	for _, name := range []string{"vm-instance-kept", "vm-instance-lost"} {
		if !strings.Contains(cond.Message, name) {
			t.Errorf("ReservationIncomplete message %q does not name %s", cond.Message, name)
		}
	}
	// The stale figure is kept as it was, not overwritten.
	s, _ := reservation.ReadStored(getRelease(t, c, "tenant-foo", "vm-instance-kept").Annotations)
	assertFigure(t, "kept", s.Resources, map[string]string{"cpu": "3"})
}

// TestReservation_SkipsReleasesThatAreNotApplications: a release without
// the application labels, or of a kind that declares no consumption, is not
// counted.
func TestReservation_SkipsReleasesThatAreNotApplications(t *testing.T) {
	unlabelled := vm(t, "raw", "tenant-foo", "u1.large")
	unlabelled.Labels = nil
	undeclared := vm(t, "pg", "tenant-foo", "u1.large")
	undeclared.Labels[appsv1alpha1.ApplicationKindLabel] = "Postgres"
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		unlabelled, undeclared,
	)
	sweep(t, r)

	assertFigure(t, "reserved", poolStatusOf(t, c, "tenant-root", "tenant-foo").Reserved, nil)
	if _, ok := getRelease(t, c, "tenant-foo", "vm-instance-pg").Annotations[reservation.ReservedAnnotation]; ok {
		t.Error("a figure was stored on a kind that declares no consumption")
	}
}

// TestReservation_ClearsPoolStatusWhereNoPoolIs: a tenant that no longer
// roots a pool loses its pool status, and so does every tenant once the
// oracle is turned off.
func TestReservation_ClearsPoolStatusWhereNoPoolIs(t *testing.T) {
	stale := `{"reserved":{"cpu":"1"}}`
	bar := tenantWithQuotas(t, "bar", "tenant-root", nil)
	bar.Annotations = map[string]string{reservation.PoolAnnotation: stale}
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"cpu": "10"}),
		bar,
	)
	sweep(t, r)
	if s := poolStatusOf(t, c, "tenant-root", "tenant-bar"); s != nil {
		t.Errorf("tenant bar roots no pool, yet keeps %+v", s)
	}
	if poolStatusOf(t, c, "tenant-root", "tenant-foo") == nil {
		t.Fatal("tenant foo roots a pool but carries no pool status")
	}

	r.Reservation = false
	sweep(t, r)
	if s := poolStatusOf(t, c, "tenant-root", "tenant-foo"); s != nil {
		t.Errorf("the pool status survives turning the oracle off: %+v", s)
	}
}

// TestReservation_UnrenderedChildIsCarvedOut: a bounded child whose
// namespace was never rendered still carves its declared quota out of its
// parent's pool, as admission counts it, rather than becoming an unbounded
// member of that pool for lack of a rendered tenant-quota.
func TestReservation_UnrenderedChildIsCarvedOut(t *testing.T) {
	r, c, _ := newReservationReconciler(t,
		tenantWithQuotas(t, "foo", "tenant-root", map[string]string{"storage": "6Ti"}),
		// Rendered: its namespace and tenant-quota exist.
		tenantWithQuotas(t, "up", "tenant-foo", map[string]string{"storage": "200Gi"}),
		ns("tenant-foo"), ns("tenant-foo-up"),
		chartQuota("tenant-foo-up", map[string]string{"requests.storage": "200Gi"}, nil),
		// Never rendered: no namespace, no tenant-quota.
		tenantWithQuotas(t, "down", "tenant-foo", map[string]string{"storage": "200Gi"}),
	)
	sweep(t, r)

	foo := poolStatusOf(t, c, "tenant-root", "tenant-foo")
	assertFigure(t, "carvedOut", foo.CarvedOut, map[string]string{"storage": "400Gi"})
	if poolStatusOf(t, c, "tenant-foo", "tenant-down") == nil {
		t.Error("the unrendered child roots no pool of its own")
	}
}
