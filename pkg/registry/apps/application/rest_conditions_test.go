package application

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func makeHelmRelease(name, namespace string) *helmv2.HelmRelease {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				ApplicationKindLabel:  "PostgreSQL",
				ApplicationGroupLabel: "apps.cozystack.io",
				ApplicationNameLabel:  "mydb",
			},
		},
	}
	return hr
}

func TestConvertConditions_WorkloadsReadyAdded(t *testing.T) {
	monitor := &cozyv1alpha1.WorkloadMonitor{
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

	r := newTestRESTWithSchemes(monitor)
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Release applied"},
		{Type: "Released", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Released"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition to be present")
	}
	if wc.Status != metav1.ConditionTrue {
		t.Errorf("expected WorkloadsReady=True, got %s", wc.Status)
	}

	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition to be present")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True, got %s", rc.Status)
	}
}

func TestConvertConditions_ReadyNotOverriddenWhenWorkloadsNotReady(t *testing.T) {
	// Ready must reflect HelmRelease state only. WorkloadsReady is a separate
	// signal that users/dashboards can observe independently.
	monitor := &cozyv1alpha1.WorkloadMonitor{
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
			Operational: ptr.To(false),
		},
	}

	r := newTestRESTWithSchemes(monitor)
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Release applied"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition to be present")
	}
	if wc.Status != metav1.ConditionFalse {
		t.Errorf("expected WorkloadsReady=False, got %s", wc.Status)
	}

	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition to be present")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True (reflects HelmRelease only), got %s", rc.Status)
	}
	if rc.Reason != "Succeeded" {
		t.Errorf("expected Ready.Reason=Succeeded (unchanged), got %s", rc.Reason)
	}
}

func TestConvertConditions_NoOverrideWhenNoMonitors(t *testing.T) {
	r := newTestRESTWithSchemes()
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Release applied"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc != nil {
		t.Error("expected no WorkloadsReady condition when no monitors exist")
	}

	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition to be present")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True unchanged, got %s", rc.Status)
	}
}

func TestConvertConditions_ReadyStaysTrue_WhenAllOperational(t *testing.T) {
	monitor := &cozyv1alpha1.WorkloadMonitor{
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

	r := newTestRESTWithSchemes(monitor)
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Release applied"},
		{Type: "Released", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "Released"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True when all workloads operational, got %s", rc.Status)
	}
	if rc.Reason != "Succeeded" {
		t.Errorf("expected Ready.Reason=Succeeded (unchanged), got %s", rc.Reason)
	}
}

func TestConvertConditions_WorkloadsReadyTimestampIsNonZero(t *testing.T) {
	monitor := &cozyv1alpha1.WorkloadMonitor{
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

	r := newTestRESTWithSchemes(monitor)
	hr := makeHelmRelease("postgresql-mydb", "default")
	// No Ready condition — HR still being reconciled
	hr.Status.Conditions = []metav1.Condition{}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition")
	}
	if wc.LastTransitionTime.IsZero() {
		t.Error("expected non-zero LastTransitionTime")
	}
}

func TestConvertConditions_WorkloadsReadyUnknownWhenNilOperational(t *testing.T) {
	monitor := &cozyv1alpha1.WorkloadMonitor{
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

	r := newTestRESTWithSchemes(monitor)
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "ok"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition")
	}
	if wc.Status != metav1.ConditionUnknown {
		t.Errorf("expected WorkloadsReady=Unknown for nil Operational, got %s", wc.Status)
	}

	// Ready should NOT be overridden for unknown — prefer availability during startup
	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True when workloads unknown (pending), got %s", rc.Status)
	}
}

func TestConvertConditions_WorkloadsReadyUnknownOnError(t *testing.T) {
	// Create a client with only HelmRelease scheme — WorkloadMonitor List will fail
	scheme := runtime.NewScheme()
	_ = helmv2.AddToScheme(scheme)
	// Deliberately NOT registering cozyv1alpha1 so that List returns an error

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := NewREST(c, nil, &config.Resource{
		Application: config.ApplicationConfig{
			Kind:     "PostgreSQL",
			Plural:   "postgresqls",
			Singular: "postgresql",
		},
		Release: config.ReleaseConfig{
			Prefix: "postgresql-",
		},
	})

	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.CreationTimestamp = metav1.Now()
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", Message: "ok"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition with Unknown status on error")
	}
	if wc.Status != metav1.ConditionUnknown {
		t.Errorf("expected WorkloadsReady=Unknown, got %s", wc.Status)
	}
	if wc.Reason != "Error" {
		t.Errorf("expected reason=Error, got %s", wc.Reason)
	}

	// Ready should NOT be overridden on error (fail-open: prefer availability)
	rc := findCondition(app.GetConditions(), "Ready")
	if rc == nil {
		t.Fatal("expected Ready condition")
	}
	if rc.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True (fail-open on error), got %s", rc.Status)
	}
}


