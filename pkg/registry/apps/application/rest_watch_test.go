package application

import (
	"context"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWmResultChan_NilWatcher(t *testing.T) {
	ch := wmResultChan(nil)
	if ch != nil {
		t.Error("expected nil channel for nil watcher")
	}
}

func TestWmResultChan_ValidWatcher(t *testing.T) {
	fw := watch.NewFake()
	ch := wmResultChan(fw)
	if ch == nil {
		t.Error("expected non-nil channel for valid watcher")
	}
	fw.Stop()
}

// TestWatchIntegration_WorkloadMonitorTriggersModifiedEvent verifies the
// full path: WM event → label lookup → HelmRelease Get → Application conversion.
func TestWatchIntegration_WorkloadMonitorTriggersModifiedEvent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = helmv2.AddToScheme(scheme)

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

	wm := &cozyv1alpha1.WorkloadMonitor{
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

	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(hr, wm).Build()

	r := newTestRESTWithSchemesFromClient(c)

	// Simulate the Watch goroutine path: extract app name from WM labels,
	// construct HelmRelease name, look up HR, convert to Application
	wmAppName := wm.Labels[ApplicationNameLabel]
	hrName := r.releaseConfig.Prefix + wmAppName

	foundHR := &helmv2.HelmRelease{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: "default", Name: hrName}, foundHR); err != nil {
		t.Fatalf("failed to get HelmRelease: %v", err)
	}
	app, err := r.ConvertHelmReleaseToApplication(context.TODO(), foundHR)
	if err != nil {
		t.Fatalf("failed to convert: %v", err)
	}
	if app.Name != "mydb" {
		t.Errorf("expected app name 'mydb', got %q", app.Name)
	}

	// Verify WorkloadsReady is False due to non-operational WM
	wc := findCondition(app.GetConditions(), "WorkloadsReady")
	if wc == nil {
		t.Fatal("expected WorkloadsReady condition")
	}
	if wc.Status != metav1.ConditionFalse {
		t.Errorf("expected WorkloadsReady=False, got %s", wc.Status)
	}
}

// TestWatchIntegration_MonitorDeletionDropsWorkloadsReady verifies that when a
// WorkloadMonitor is deleted, the Application's WorkloadsReady condition
// disappears. Ready condition always reflects HelmRelease state regardless.
func TestWatchIntegration_MonitorDeletionDropsWorkloadsReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = helmv2.AddToScheme(scheme)

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

	nonOpMonitor := &cozyv1alpha1.WorkloadMonitor{
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

	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(hr, nonOpMonitor).Build()
	r := newTestRESTWithSchemesFromClient(c)

	// Step 1: With non-operational monitor, WorkloadsReady=False, Ready=True
	app1, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wc1 := findCondition(app1.GetConditions(), "WorkloadsReady")
	if wc1 == nil || wc1.Status != metav1.ConditionFalse {
		t.Fatalf("expected WorkloadsReady=False with non-operational monitor, got %v", wc1)
	}
	rc1 := findCondition(app1.GetConditions(), "Ready")
	if rc1 == nil || rc1.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True (reflects HelmRelease), got %v", rc1)
	}

	// Step 2: Delete the monitor
	if err := c.Delete(context.TODO(), nonOpMonitor); err != nil {
		t.Fatalf("failed to delete monitor: %v", err)
	}

	// Step 3: WorkloadsReady should disappear, Ready stays True
	app2, err := r.convertHelmReleaseToApplication(context.TODO(), hr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wc2 := findCondition(app2.GetConditions(), "WorkloadsReady")
	if wc2 != nil {
		t.Error("expected no WorkloadsReady condition after monitor deletion")
	}
	rc2 := findCondition(app2.GetConditions(), "Ready")
	if rc2 == nil {
		t.Fatal("expected Ready condition")
	}
	if rc2.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True after monitor deletion, got %s", rc2.Status)
	}
}

// TestWatchIntegration_WMWatcherCloseProducesNilChannel verifies that
// after wmWatcher is set to nil, wmResultChan returns nil channel.
func TestWatchIntegration_WMWatcherCloseProducesNilChannel(t *testing.T) {
	fw := watch.NewFake()
	ch := wmResultChan(fw)
	if ch == nil {
		t.Fatal("expected non-nil channel before close")
	}

	fw.Stop()

	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				var nilWatcher watch.Interface
				nilCh := wmResultChan(nilWatcher)
				if nilCh != nil {
					t.Error("expected nil channel after watcher set to nil")
				}
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for watcher channel to close")
		}
	}
}

func newTestRESTWithSchemesFromClient(c client.Client) *REST {
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
