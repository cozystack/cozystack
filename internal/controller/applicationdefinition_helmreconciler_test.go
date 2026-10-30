package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

// vmInstanceRelease builds a VMInstance HelmRelease carrying the application
// labels this reconciler selects on, with the given wait configuration.
func vmInstanceRelease(install, upgrade *bool) *helmv2.HelmRelease {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-instance-web",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "VMInstance",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "ExternalArtifact",
				Name:      "vm-instance-app",
				Namespace: "cozy-system",
			},
		},
	}
	if install != nil {
		hr.Spec.Install = &helmv2.Install{DisableWait: *install}
	}
	if upgrade != nil {
		hr.Spec.Upgrade = &helmv2.Upgrade{DisableWait: *upgrade}
	}
	return hr
}

// vmInstanceAppDef builds the VMInstance ApplicationDefinition, optionally
// carrying the disable-wait annotation.
func vmInstanceAppDef(annotations map[string]string) *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-instance", Annotations: annotations},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "VMInstance"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "ExternalArtifact",
					Name:      "vm-instance-app",
					Namespace: "cozy-system",
				},
			},
		},
	}
}

func harborDef(name, artifact string, created time.Time) *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Harbor"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "OCIRepository", Name: artifact, Namespace: "cozy-public"},
			},
		},
	}
}

func reconcileVMInstance(t *testing.T, appDef *cozyv1alpha1.ApplicationDefinition, hr *helmv2.HelmRelease) *helmv2.HelmRelease {
	t.Helper()

	scheme := newAppDefHelmScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef, hr).Build()
	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}

	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: appDef.Name},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	return got
}

// TestAppDefHelm_DisableWaitApplied pins the drift this reconciler exists to
// close, for the one field a running release cannot pick up on its own: a
// HelmRelease created before the definition carried
// release.cozystack.io/helm-install-disable-wait keeps waiting on readiness
// until its Application is written through the apps API again. On a cluster
// where the wait is what broke (a KubeVirt VirtualMachine the kstatus poller
// reads as permanently InProgress), that is every existing VM, each one
// looping upgrade and timeout, and re-writing them by hand is the operator's
// only way out. Bring the field to what the definition asks for instead.
func TestAppDefHelm_DisableWaitApplied(t *testing.T) {
	no := false
	got := reconcileVMInstance(t,
		vmInstanceAppDef(map[string]string{config.HelmInstallDisableWaitAnnotation: "true"}),
		vmInstanceRelease(&no, &no))

	if got.Spec.Install == nil || !got.Spec.Install.DisableWait {
		t.Fatalf("expected Install.DisableWait=true, got %+v", got.Spec.Install)
	}
	if got.Spec.Upgrade == nil || !got.Spec.Upgrade.DisableWait {
		t.Fatalf("expected Upgrade.DisableWait=true, got %+v", got.Spec.Upgrade)
	}
}

// TestAppDefHelm_DisableWaitAppliedToAbsentBlocks pins the same on a release
// that carries no install/upgrade block at all: the field still has to land.
func TestAppDefHelm_DisableWaitAppliedToAbsentBlocks(t *testing.T) {
	got := reconcileVMInstance(t,
		vmInstanceAppDef(map[string]string{config.HelmInstallDisableWaitAnnotation: "true"}),
		vmInstanceRelease(nil, nil))

	if got.Spec.Install == nil || !got.Spec.Install.DisableWait {
		t.Fatalf("expected Install.DisableWait=true, got %+v", got.Spec.Install)
	}
	if got.Spec.Upgrade == nil || !got.Spec.Upgrade.DisableWait {
		t.Fatalf("expected Upgrade.DisableWait=true, got %+v", got.Spec.Upgrade)
	}
}

// TestAppDefHelm_DisableWaitRemoved pins the other direction, because the
// definition is the source of truth in both: cozystack-api rebuilds the whole
// spec on every write and emits DisableWait=false once the annotation is gone,
// so a release left at true would drift back the moment anyone touched it.
func TestAppDefHelm_DisableWaitRemoved(t *testing.T) {
	yes := true
	got := reconcileVMInstance(t, vmInstanceAppDef(nil), vmInstanceRelease(&yes, &yes))

	if got.Spec.Install == nil || got.Spec.Install.DisableWait {
		t.Fatalf("expected Install.DisableWait=false, got %+v", got.Spec.Install)
	}
	if got.Spec.Upgrade == nil || got.Spec.Upgrade.DisableWait {
		t.Fatalf("expected Upgrade.DisableWait=false, got %+v", got.Spec.Upgrade)
	}
}

// TestAppDefHelm_DisableWaitLeavesAbsentBlocksAlone pins that the false case
// does not materialise empty install/upgrade blocks on a release that has
// none: writing them would change nothing about the release's behaviour and
// would make every such reconcile an update.
func TestAppDefHelm_DisableWaitLeavesAbsentBlocksAlone(t *testing.T) {
	got := reconcileVMInstance(t, vmInstanceAppDef(nil), vmInstanceRelease(nil, nil))

	if got.Spec.Install != nil {
		t.Fatalf("expected Install left absent, got %+v", got.Spec.Install)
	}
	if got.Spec.Upgrade != nil {
		t.Fatalf("expected Upgrade left absent, got %+v", got.Spec.Upgrade)
	}
}

// TestAppDefHelm_DisableWaitInSyncSkipsUpdate pins idempotence: a release that
// already matches must not be written, or every reconcile of every definition
// bumps the resourceVersion of every release it owns and wakes helm-controller.
func TestAppDefHelm_DisableWaitInSyncSkipsUpdate(t *testing.T) {
	yes := true
	hr := vmInstanceRelease(&yes, &yes)
	hr.Spec.ValuesFrom = expectedValuesFrom()

	scheme := newAppDefHelmScheme(t)
	appDef := vmInstanceAppDef(map[string]string{config.HelmInstallDisableWaitAnnotation: "true"})
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef, hr).Build()

	before := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}, before); err != nil {
		t.Fatalf("get HR: %v", err)
	}

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: appDef.Name},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}, after); err != nil {
		t.Fatalf("get HR after reconcile: %v", err)
	}
	if after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("release rewritten with nothing to change: %q -> %q", before.ResourceVersion, after.ResourceVersion)
	}
}

// TestAppDefHelm_DisableWaitInvalidAnnotationLeavesReleaseAlone pins the
// invalid-annotation branch. The parser returns false alongside its error, so
// applying that value would switch the wait back on for every release of the
// kind; the release must keep what it has instead.
func TestAppDefHelm_DisableWaitInvalidAnnotationLeavesReleaseAlone(t *testing.T) {
	yes := true
	got := reconcileVMInstance(t,
		vmInstanceAppDef(map[string]string{config.HelmInstallDisableWaitAnnotation: "yes"}),
		vmInstanceRelease(&yes, &yes))

	if got.Spec.Install == nil || !got.Spec.Install.DisableWait {
		t.Fatalf("expected Install.DisableWait=true kept, got %+v", got.Spec.Install)
	}
	if got.Spec.Upgrade == nil || !got.Spec.Upgrade.DisableWait {
		t.Fatalf("expected Upgrade.DisableWait=true kept, got %+v", got.Spec.Upgrade)
	}
}

// TestAppDefHelm_DisableWaitScopedToKind pins the kind label in the selector:
// a release of another kind in the same group must not pick up the wait
// setting of the VMInstance definition.
func TestAppDefHelm_DisableWaitScopedToKind(t *testing.T) {
	no := false
	vm := vmInstanceRelease(&no, &no)
	other := vmInstanceRelease(&no, &no)
	other.Name = "harbor-registry"
	other.Labels["apps.cozystack.io/application.kind"] = "Harbor"

	scheme := newAppDefHelmScheme(t)
	appDef := vmInstanceAppDef(map[string]string{config.HelmInstallDisableWaitAnnotation: "true"})
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef, vm, other).Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: appDef.Name},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotVM := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: vm.Name, Namespace: vm.Namespace}, gotVM); err != nil {
		t.Fatalf("get VMInstance HR: %v", err)
	}
	if gotVM.Spec.Install == nil || !gotVM.Spec.Install.DisableWait {
		t.Fatalf("expected VMInstance Install.DisableWait=true, got %+v", gotVM.Spec.Install)
	}

	gotOther := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: other.Name, Namespace: other.Namespace}, gotOther); err != nil {
		t.Fatalf("get Harbor HR: %v", err)
	}
	if gotOther.Spec.Install == nil || gotOther.Spec.Install.DisableWait {
		t.Fatalf("expected Harbor Install.DisableWait=false, got %+v", gotOther.Spec.Install)
	}
	if gotOther.Spec.Upgrade == nil || gotOther.Spec.Upgrade.DisableWait {
		t.Fatalf("expected Harbor Upgrade.DisableWait=false, got %+v", gotOther.Spec.Upgrade)
	}
}

// TestAppDefHelm_NonOwnerDoesNotRewrite pins the ownership relation: a second
// definition declaring a kind that an older definition already owns must not
// repoint that kind's releases at its own artifact, whatever it is named.
func TestAppDefHelm_NonOwnerDoesNotRewrite(t *testing.T) {
	scheme := newAppDefHelmScheme(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	owner := harborDef("harbor", "harbor-app", base)
	// Sorts before the owner by name, so only creation order keeps it out.
	newcomer := harborDef("a-harbor", "other-artifact", base.Add(time.Hour))
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-release", Namespace: "tenant-foo", Labels: map[string]string{
			"apps.cozystack.io/application.kind":  "Harbor",
			"apps.cozystack.io/application.group": "apps.cozystack.io",
		}},
		Spec: helmv2.HelmReleaseSpec{ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "OCIRepository", Name: "harbor-app", Namespace: "cozy-public"}},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner, newcomer, hr).Build()
	rec := record.NewFakeRecorder(4)
	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}

	for _, name := range []string{"a-harbor", "harbor"} {
		if _, err := r.Reconcile(context.TODO(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatalf("reconcile %s: %v", name, err)
		}
		got := &helmv2.HelmRelease{}
		if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "harbor-release", Namespace: "tenant-foo"}, got); err != nil {
			t.Fatalf("get HR: %v", err)
		}
		if got.Spec.ChartRef == nil || got.Spec.ChartRef.Name != "harbor-app" {
			t.Fatalf("after reconciling %s the release points at %+v, want the owner's harbor-app", name, got.Spec.ChartRef)
		}
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "KindClaimed") || !strings.Contains(ev, "harbor") {
			t.Fatalf("unexpected event %q", ev)
		}
	default:
		t.Fatal("the non-owner must get a Warning event naming the owner")
	}
}

// TestAppDefHelm_SameKindRequests pins the watch mapping: an event on any
// definition enqueues every definition declaring the same kind, so when the
// owner is deleted the next one takes over without waiting for its own change.
func TestAppDefHelm_SameKindRequests(t *testing.T) {
	scheme := newAppDefHelmScheme(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	other := &cozyv1alpha1.ApplicationDefinition{ObjectMeta: metav1.ObjectMeta{Name: "pg"}}
	other.Spec.Application.Kind = "Postgres"
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(harborDef("harbor", "a", base), harborDef("tap-harbor", "b", base), other).Build()
	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}

	deleted := harborDef("harbor", "a", base)
	reqs := r.sameKindRequests(context.TODO(), deleted)
	names := map[string]bool{}
	for _, q := range reqs {
		names[q.Name] = true
	}
	if !names["harbor"] || !names["tap-harbor"] || names["pg"] || len(reqs) != 2 {
		t.Fatalf("requests = %v (%d), want harbor and tap-harbor once each", names, len(reqs))
	}
}

// dataVolumeCurrent is the expression the vm-disk definition carries, so that a
// disk CDI is still importing does not hold its release's wait.
var dataVolumeCurrent = kustomize.CustomHealthCheck{
	APIVersion:             "cdi.kubevirt.io/v1beta1",
	Kind:                   "DataVolume",
	HealthCheckExpressions: kustomize.HealthCheckExpressions{Current: "true"},
}

// vmDiskRelease builds a VMDisk HelmRelease the way cozystack-api wrote it
// before the definition declared any expression: chartRef and valuesFrom in
// sync, no healthCheckExprs, no waitStrategy.
func vmDiskRelease() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-disk-data",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				"apps.cozystack.io/application.kind":  "VMDisk",
				"apps.cozystack.io/application.group": "apps.cozystack.io",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "ExternalArtifact",
				Name:      "cozystack-vm-disk-application-kubevirt-vm-disk",
				Namespace: "cozy-system",
			},
			ValuesFrom: expectedValuesFrom(),
		},
	}
}

// vmDiskAppDef builds the VMDisk ApplicationDefinition with the given wait
// strategy and expressions.
func vmDiskAppDef(waitStrategy string, exprs ...kustomize.CustomHealthCheck) *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-disk"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "VMDisk"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "ExternalArtifact",
					Name:      "cozystack-vm-disk-application-kubevirt-vm-disk",
					Namespace: "cozy-system",
				},
				WaitStrategy:     waitStrategy,
				HealthCheckExprs: exprs,
			},
		},
	}
}

// TestAppDefHelm_HealthCheckExprsApplied pins the drift behind #4409:
// cozystack-api writes healthCheckExprs only when it builds a release, so a
// disk created before the definition exempted its DataVolume kept waiting on
// it at every upgrade, and one CDI never finished importing failed its upgrade
// for good. The expressions must land, with the poller strategy that is the
// only one evaluating them.
func TestAppDefHelm_HealthCheckExprsApplied(t *testing.T) {
	got := reconcileVMInstance(t, vmDiskAppDef("", dataVolumeCurrent), vmDiskRelease())

	if want := []kustomize.CustomHealthCheck{dataVolumeCurrent}; !reflect.DeepEqual(got.Spec.HealthCheckExprs, want) {
		t.Fatalf("expected HealthCheckExprs %+v, got %+v", want, got.Spec.HealthCheckExprs)
	}
	if got.Spec.WaitStrategy == nil || got.Spec.WaitStrategy.Name != helmv2.WaitStrategyPoller {
		t.Fatalf("expected WaitStrategy poller, got %+v", got.Spec.WaitStrategy)
	}
}

// TestAppDefHelm_HealthCheckExprsRemoved pins the other direction: the API
// rebuilds the whole spec on every write, so a release left carrying what the
// definition dropped would differ from one written a moment later.
func TestAppDefHelm_HealthCheckExprsRemoved(t *testing.T) {
	hr := vmDiskRelease()
	hr.Spec.HealthCheckExprs = []kustomize.CustomHealthCheck{dataVolumeCurrent}
	hr.Spec.WaitStrategy = &helmv2.WaitStrategy{Name: helmv2.WaitStrategyPoller}

	got := reconcileVMInstance(t, vmDiskAppDef(""), hr)

	if len(got.Spec.HealthCheckExprs) != 0 {
		t.Fatalf("expected HealthCheckExprs removed, got %+v", got.Spec.HealthCheckExprs)
	}
	if got.Spec.WaitStrategy != nil {
		t.Fatalf("expected WaitStrategy removed, got %+v", got.Spec.WaitStrategy)
	}
}

// TestAppDefHelm_WaitStrategyWithoutExprsApplied pins that an explicit strategy
// is carried on its own, not only as the default the expressions imply.
func TestAppDefHelm_WaitStrategyWithoutExprsApplied(t *testing.T) {
	got := reconcileVMInstance(t, vmDiskAppDef("legacy"), vmDiskRelease())

	if got.Spec.WaitStrategy == nil || got.Spec.WaitStrategy.Name != helmv2.WaitStrategyLegacy {
		t.Fatalf("expected WaitStrategy legacy, got %+v", got.Spec.WaitStrategy)
	}
	if len(got.Spec.HealthCheckExprs) != 0 {
		t.Fatalf("expected no HealthCheckExprs, got %+v", got.Spec.HealthCheckExprs)
	}
}

// TestAppDefHelm_HealthCheckExprsInSyncSkipsUpdate pins idempotence, like
// TestAppDefHelm_DisableWaitInSyncSkipsUpdate. A definition read with
// healthCheckExprs: [] decodes to an empty slice, while a release built without
// expressions carries none at all; comparing the two as different would
// rewrite every release of the kind on every reconcile. The fake client drops
// an empty slice on its JSON round trip, so the objects are handed to
// updateHelmReleaseChart directly.
func TestAppDefHelm_HealthCheckExprsInSyncSkipsUpdate(t *testing.T) {
	cases := []struct {
		name       string
		defExprs   []kustomize.CustomHealthCheck
		hrExprs    []kustomize.CustomHealthCheck
		hrStrategy *helmv2.WaitStrategy
	}{
		{
			name:       "expressions and poller already set",
			defExprs:   []kustomize.CustomHealthCheck{dataVolumeCurrent},
			hrExprs:    []kustomize.CustomHealthCheck{dataVolumeCurrent},
			hrStrategy: &helmv2.WaitStrategy{Name: helmv2.WaitStrategyPoller},
		},
		{
			name:     "empty on the definition, absent on the release",
			defExprs: []kustomize.CustomHealthCheck{},
		},
		{
			name:    "absent on the definition, empty on the release",
			hrExprs: []kustomize.CustomHealthCheck{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := vmDiskRelease()
			stored.Spec.HealthCheckExprs = tc.hrExprs
			stored.Spec.WaitStrategy = tc.hrStrategy

			scheme := newAppDefHelmScheme(t)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored).Build()

			before := &helmv2.HelmRelease{}
			if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: stored.Name, Namespace: stored.Namespace}, before); err != nil {
				t.Fatalf("get HR: %v", err)
			}
			hr := before.DeepCopy()
			hr.Spec.HealthCheckExprs = tc.hrExprs

			r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
			if err := r.updateHelmReleaseChart(context.TODO(), hr, vmDiskAppDef("", tc.defExprs...)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			after := &helmv2.HelmRelease{}
			if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: stored.Name, Namespace: stored.Namespace}, after); err != nil {
				t.Fatalf("get HR after update: %v", err)
			}
			if after.ResourceVersion != before.ResourceVersion {
				t.Fatalf("release rewritten with nothing to change: %q -> %q", before.ResourceVersion, after.ResourceVersion)
			}
		})
	}
}

// TestAppDefHelm_HealthCheckExprsLeaveRenderedReleaseAlone pins the scope of
// the sync. The tenant chart renders an Etcd HelmRelease carrying the same kind
// labels as one the API builds, and it sets that release's wait gate itself;
// rewriting it from the definition would strip the chart's gate until the
// parent's next upgrade put it back. Its chartRef still follows the definition.
func TestAppDefHelm_HealthCheckExprsLeaveRenderedReleaseAlone(t *testing.T) {
	chartGate := kustomize.CustomHealthCheck{
		APIVersion:             "etcd-operator.cozystack.io/v1alpha2",
		Kind:                   "EtcdCluster",
		HealthCheckExpressions: kustomize.HealthCheckExpressions{Current: "status.readyMembers == spec.replicas"},
	}
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd", Namespace: "tenant-foo", Labels: map[string]string{
			"apps.cozystack.io/application.kind":  "Etcd",
			"apps.cozystack.io/application.group": "apps.cozystack.io",
			"helm.toolkit.fluxcd.io/name":         "tenant-foo",
		}},
		Spec: helmv2.HelmReleaseSpec{
			ChartRef:         &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "stale", Namespace: "cozy-system"},
			ValuesFrom:       expectedValuesFrom(),
			HealthCheckExprs: []kustomize.CustomHealthCheck{chartGate},
			WaitStrategy:     &helmv2.WaitStrategy{Name: helmv2.WaitStrategyPoller},
		},
	}
	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{Kind: "Etcd"},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{
				ChartRef: &helmv2.CrossNamespaceSourceReference{Kind: "ExternalArtifact", Name: "cozystack-etcd-application-default-etcd", Namespace: "cozy-system"},
			},
		},
	}

	got := reconcileVMInstance(t, appDef, hr)

	if got.Spec.ChartRef == nil || got.Spec.ChartRef.Name != "cozystack-etcd-application-default-etcd" {
		t.Fatalf("expected ChartRef synced from the definition, got %+v", got.Spec.ChartRef)
	}
	if want := []kustomize.CustomHealthCheck{chartGate}; !reflect.DeepEqual(got.Spec.HealthCheckExprs, want) {
		t.Fatalf("expected the chart's HealthCheckExprs %+v kept, got %+v", want, got.Spec.HealthCheckExprs)
	}
	if got.Spec.WaitStrategy == nil || got.Spec.WaitStrategy.Name != helmv2.WaitStrategyPoller {
		t.Fatalf("expected the chart's WaitStrategy poller kept, got %+v", got.Spec.WaitStrategy)
	}
}

// TestAppDefHelm_HealthCheckExprsScopedToKind pins the kind label in the
// selector: a release of another kind must not pick up the VMDisk expressions.
func TestAppDefHelm_HealthCheckExprsScopedToKind(t *testing.T) {
	disk := vmDiskRelease()
	other := vmDiskRelease()
	other.Name = "harbor-registry"
	other.Labels["apps.cozystack.io/application.kind"] = "Harbor"

	scheme := newAppDefHelmScheme(t)
	appDef := vmDiskAppDef("", dataVolumeCurrent)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef, disk, other).Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: appDef.Name},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotDisk := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: disk.Name, Namespace: disk.Namespace}, gotDisk); err != nil {
		t.Fatalf("get VMDisk HR: %v", err)
	}
	if len(gotDisk.Spec.HealthCheckExprs) != 1 {
		t.Fatalf("expected VMDisk HealthCheckExprs synced, got %+v", gotDisk.Spec.HealthCheckExprs)
	}

	gotOther := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: other.Name, Namespace: other.Namespace}, gotOther); err != nil {
		t.Fatalf("get Harbor HR: %v", err)
	}
	if len(gotOther.Spec.HealthCheckExprs) != 0 {
		t.Fatalf("expected Harbor HealthCheckExprs untouched, got %+v", gotOther.Spec.HealthCheckExprs)
	}
	if gotOther.Spec.WaitStrategy != nil {
		t.Fatalf("expected Harbor WaitStrategy untouched, got %+v", gotOther.Spec.WaitStrategy)
	}
}

// TestAppDefHelm_UpdateFailureRequeues pins that a failed write is returned
// rather than logged and dropped: nothing else re-enqueues the definition for
// hours, so a release that lost a conflict to helm-controller would keep its
// old wait until then. The releases after it are still written.
func TestAppDefHelm_UpdateFailureRequeues(t *testing.T) {
	first := vmDiskRelease()
	second := vmDiskRelease()
	second.Name = "vm-disk-logs"

	failed := ""
	scheme := newAppDefHelmScheme(t)
	appDef := vmDiskAppDef("", dataVolumeCurrent)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef, first, second).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				// Fail whichever release is written first, so the assertion
				// below does not depend on the order List returns them in.
				if failed == "" {
					failed = obj.GetName()
					return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), obj.GetName(), errors.New("the object has been modified"))
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()

	r := &ApplicationDefinitionHelmReconciler{Client: fakeClient, Scheme: scheme}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: appDef.Name},
	}); !apierrors.IsConflict(err) {
		t.Fatalf("expected the conflict returned so the definition is requeued, got %v", err)
	}

	written := first.Name
	if failed == first.Name {
		written = second.Name
	}
	got := &helmv2.HelmRelease{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: written, Namespace: "tenant-foo"}, got); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	if len(got.Spec.HealthCheckExprs) != 1 {
		t.Fatalf("expected %s synced past the failed %s, got %+v", written, failed, got.Spec.HealthCheckExprs)
	}
}
