package application

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

func newTestRESTWithSchemes(objs ...runtime.Object) *REST {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = helmv2.AddToScheme(scheme)

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range objs {
		builder = builder.WithRuntimeObjects(obj)
	}
	c := builder.Build()

	return NewREST(c, nil, &config.Resource{
		Application: config.ApplicationConfig{
			Kind:     "PostgreSQL",
			Plural:   "postgresqls",
			Singular: "postgresql",
		},
		Release: config.ReleaseConfig{
			Prefix: "postgresql-",
		},
	})
}

func TestGetWorkloadsOperational_NoMonitors(t *testing.T) {
	r := newTestRESTWithSchemes()
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws.found {
		t.Error("expected found=false when no monitors exist")
	}
	if !ws.operational {
		t.Error("expected operational=true when no monitors exist")
	}
}

func TestGetWorkloadsOperational_AllOperational(t *testing.T) {
	m1 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(true),
		},
	}
	m2 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-2",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(true),
		},
	}

	r := newTestRESTWithSchemes(m1, m2)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ws.found {
		t.Error("expected found=true")
	}
	if !ws.operational {
		t.Error("expected operational=true when all monitors are operational")
	}
}

func TestGetWorkloadsOperational_SomeNotOperational(t *testing.T) {
	m1 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(true),
		},
	}
	m2 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-2",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(false),
		},
	}

	r := newTestRESTWithSchemes(m1, m2)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ws.found {
		t.Error("expected found=true")
	}
	if ws.operational {
		t.Error("expected operational=false when at least one monitor is not operational")
	}
}

func TestGetWorkloadsOperational_OperationalNil(t *testing.T) {
	m := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: nil, // Not yet reconciled
		},
	}

	r := newTestRESTWithSchemes(m)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ws.found {
		t.Error("expected found=true")
	}
	if !ws.unknown {
		t.Error("expected unknown=true when Operational is nil")
	}
}

func TestGetWorkloadsOperational_MixedNilAndOperational(t *testing.T) {
	m1 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(true),
		},
	}
	m2 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-2",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: nil,
		},
	}

	r := newTestRESTWithSchemes(m1, m2)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ws.unknown {
		t.Error("expected unknown=true when at least one monitor has nil Operational")
	}
}

func TestGetWorkloadsOperational_MixedFailedAndPending(t *testing.T) {
	m1 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(false), // Confirmed failure
		},
	}
	m2 := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-2",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: nil, // Not yet reconciled
		},
	}

	r := newTestRESTWithSchemes(m1, m2)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws.operational {
		t.Error("expected operational=false when at least one monitor is explicitly failed")
	}
	if !ws.unknown {
		t.Error("expected unknown=true when at least one monitor has nil Operational")
	}
}

func TestConvertConditions_MixedFailedAndPendingShowsFalse(t *testing.T) {
	mFailed := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-failed",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(false),
		},
	}
	mPending := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-pending",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "mydb",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: nil,
		},
	}

	r := newTestRESTWithSchemes(mFailed, mPending)
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "postgresql-mydb",
			Namespace: "default",
			Labels: map[string]string{
				ApplicationKindLabel:  "PostgreSQL",
				ApplicationGroupLabel: "apps.cozystack.io",
				ApplicationNameLabel:  "mydb",
			},
		},
	}
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "ok"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Concrete failure should take priority over unknown
	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition")
	}
	if wc.Status != metav1.ConditionFalse {
		t.Errorf("expected WorkloadsReady=False (failure takes priority over unknown), got %s", wc.Status)
	}
}

func TestGetWorkloadsOperational_DifferentApp_NotFound(t *testing.T) {
	m := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon-1",
			Namespace: "default",
			Labels: map[string]string{
				appsv1alpha1.ApplicationKindLabel:  "PostgreSQL",
				appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
				appsv1alpha1.ApplicationNameLabel:  "other-db",
			},
		},
		Status: cozyv1alpha1.WorkloadMonitorStatus{
			Operational: ptr.To(false),
		},
	}

	r := newTestRESTWithSchemes(m)
	ws, err := r.getWorkloadsOperational(context.TODO(), "default", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws.found {
		t.Error("expected found=false for different app name")
	}
	if !ws.operational {
		t.Error("expected operational=true when no matching monitors found")
	}
}

