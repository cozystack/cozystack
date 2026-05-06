package controller

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newAppDefHelmScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add helmv2: %v", err)
	}
	return scheme
}

// TestAppDefHelm_MissingAppDefIgnored pins IgnoreNotFound: when the
// ApplicationDefinition referenced by the request has been deleted,
// Reconcile must not return an error.
func TestAppDefHelm_MissingAppDefIgnored(t *testing.T) {
	scheme := newAppDefHelmScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	res, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "missing"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestAppDefHelm_EmptyKindIsNoop pins the early-return: when
// Application.Kind is empty the reconciler must skip without listing
// HelmReleases or returning an error.
func TestAppDefHelm_EmptyKindIsNoop(t *testing.T) {
	scheme := newAppDefHelmScheme(t)
	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-kind"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: ""},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef).
		Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "empty-kind"},
	}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestAppDefHelm_ChartRefMismatchPatches pins the happy path: an
// ApplicationDefinition with a valid ChartRef + a HelmRelease carrying
// matching application labels but a different ChartRef → the HR's
// ChartRef is updated to match the appDef.
func TestAppDefHelm_ChartRefMismatchPatches(t *testing.T) {
	scheme := newAppDefHelmScheme(t)

	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "OCIRepository",
					Name:      "harbor-app",
					Namespace: "cozy-public",
				},
			},
		},
	}
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "harbor-release",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "OCIRepository",
				Name:      "harbor-app-stale",
				Namespace: "cozy-public",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef, hr).
		Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "harbor"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "harbor-release", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	if got.Spec.ChartRef == nil || got.Spec.ChartRef.Name != "harbor-app" {
		t.Fatalf("expected ChartRef.Name=harbor-app, got %+v", got.Spec.ChartRef)
	}
}

// TestAppDefHelm_ValuesFromMismatchPatches pins the valuesFrom drift
// path: HR has the right ChartRef but missing/wrong valuesFrom →
// reconciler must rewrite valuesFrom to the canonical [{Secret,
// cozystack-values}].
func TestAppDefHelm_ValuesFromMismatchPatches(t *testing.T) {
	scheme := newAppDefHelmScheme(t)

	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "OCIRepository",
					Name:      "harbor-app",
					Namespace: "cozy-public",
				},
			},
		},
	}
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "harbor-release",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "OCIRepository",
				Name:      "harbor-app",
				Namespace: "cozy-public",
			},
			ValuesFrom: []helmv2.ValuesReference{
				{Kind: "ConfigMap", Name: "wrong"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef, hr).
		Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "harbor"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "harbor-release", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	if len(got.Spec.ValuesFrom) != 1 {
		t.Fatalf("expected exactly one valuesFrom entry, got %d (%+v)", len(got.Spec.ValuesFrom), got.Spec.ValuesFrom)
	}
	if got.Spec.ValuesFrom[0].Kind != "Secret" || got.Spec.ValuesFrom[0].Name != "cozystack-values" {
		t.Fatalf("expected {Secret,cozystack-values}, got %+v", got.Spec.ValuesFrom[0])
	}
}

// TestAppDefHelm_LabelsApplied pins the label propagation: when the
// ApplicationDefinition declares Release.Labels, those are merged into
// HR labels.
func TestAppDefHelm_LabelsApplied(t *testing.T) {
	scheme := newAppDefHelmScheme(t)

	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "OCIRepository",
					Name:      "harbor-app",
					Namespace: "cozy-public",
				},
				Labels: map[string]string{
					"cozystack.io/managed-by": "applicationdefinition",
				},
			},
		},
	}
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "harbor-release",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Harbor",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "OCIRepository",
				Name:      "harbor-app",
				Namespace: "cozy-public",
			},
			ValuesFrom: []helmv2.ValuesReference{
				{Kind: "Secret", Name: "cozystack-values"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef, hr).
		Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "harbor"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "harbor-release", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	if got.Labels["cozystack.io/managed-by"] != "applicationdefinition" {
		t.Fatalf("expected appDef label propagated, got %q", got.Labels["cozystack.io/managed-by"])
	}
	// Original labels must still be present
	if got.Labels["apps.cozystack.io/application.kind"] != "Harbor" {
		t.Fatalf("original label dropped: %v", got.Labels)
	}
}

// TestAppDefHelm_NilChartRefSkipped pins the validation guard: when
// the ApplicationDefinition.Spec.Release.ChartRef is nil, the
// reconciler must log + skip without erroring.
func TestAppDefHelm_NilChartRefSkipped(t *testing.T) {
	scheme := newAppDefHelmScheme(t)

	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "broken"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Broken"},
			Release:     cozyv1alpha1.ApplicationDefinitionRelease{ChartRef: nil},
		},
	}
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broken-release",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "Broken",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appDef, hr).
		Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "broken"},
	}); err != nil {
		t.Fatalf("expected nil error on nil ChartRef, got %v", err)
	}

	// HR must remain untouched
	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "broken-release", Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	if got.Spec.ChartRef != nil {
		t.Fatalf("HR ChartRef should remain nil when appDef ChartRef is nil, got %+v", got.Spec.ChartRef)
	}
}

// TestValuesFromEqual pins the helper used to detect drift in the
// valuesFrom slice.
func TestValuesFromEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []helmv2.ValuesReference
		want bool
	}{
		{
			name: "both empty",
			a:    nil, b: nil,
			want: true,
		},
		{
			name: "different lengths",
			a:    []helmv2.ValuesReference{{Kind: "Secret", Name: "x"}},
			b:    nil,
			want: false,
		},
		{
			name: "same single entry",
			a:    []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}},
			b:    []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}},
			want: true,
		},
		{
			name: "same length, different Kind",
			a:    []helmv2.ValuesReference{{Kind: "Secret", Name: "x"}},
			b:    []helmv2.ValuesReference{{Kind: "ConfigMap", Name: "x"}},
			want: false,
		},
		{
			name: "same length, different ValuesKey",
			a:    []helmv2.ValuesReference{{Kind: "Secret", Name: "x", ValuesKey: "a"}},
			b:    []helmv2.ValuesReference{{Kind: "Secret", Name: "x", ValuesKey: "b"}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valuesFromEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("valuesFromEqual(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
