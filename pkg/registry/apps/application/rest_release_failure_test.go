package application

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConvertReleaseFailure_StalledAndCountersExposed(t *testing.T) {
	r := newTestRESTWithSchemes()
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Stalled", Status: metav1.ConditionTrue, Reason: "RetriesExceeded", Message: "Failed to install after 1 attempt(s)"},
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InstallFailed", Message: "install failed"},
		{Type: "Released", Status: metav1.ConditionFalse, Reason: "InstallFailed", Message: "install failed"},
	}
	hr.Status.Failures = 7
	hr.Status.InstallFailures = 1
	hr.Status.UpgradeFailures = 3
	hr.Status.LastAttemptedReleaseAction = helmv2.ReleaseActionInstall

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sc := findCondition(app.GetConditions(), "Stalled")
	if sc == nil {
		t.Fatal("expected Stalled condition to be copied from the HelmRelease")
	}
	if sc.Status != metav1.ConditionTrue || sc.Reason != "RetriesExceeded" {
		t.Errorf("expected Stalled=True/RetriesExceeded, got %s/%s", sc.Status, sc.Reason)
	}
	if app.Status.InstallFailures != 1 {
		t.Errorf("expected installFailures=1, got %d", app.Status.InstallFailures)
	}
	if app.Status.UpgradeFailures != 3 {
		t.Errorf("expected upgradeFailures=3, got %d", app.Status.UpgradeFailures)
	}
	if app.Status.LastAttemptedReleaseAction != "install" {
		t.Errorf("expected lastAttemptedReleaseAction=install, got %q", app.Status.LastAttemptedReleaseAction)
	}
}

// helm-controller clears the counters on success only under the RetryOnFailure
// strategy, so a Ready release can still carry them; they must not be hidden.
func TestConvertReleaseFailure_CountersKeptOnReadyRelease(t *testing.T) {
	r := newTestRESTWithSchemes()
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "UpgradeSucceeded", Message: "ok"},
		{Type: "Released", Status: metav1.ConditionTrue, Reason: "UpgradeSucceeded", Message: "ok"},
	}
	hr.Status.InstallFailures = 2
	hr.Status.UpgradeFailures = 1
	hr.Status.LastAttemptedReleaseAction = helmv2.ReleaseActionUpgrade

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if findCondition(app.GetConditions(), "Stalled") != nil {
		t.Error("expected no Stalled condition on a release that does not carry one")
	}
	if app.Status.InstallFailures != 2 || app.Status.UpgradeFailures != 1 {
		t.Errorf("expected install=2 upgrade=1, got install=%d upgrade=%d", app.Status.InstallFailures, app.Status.UpgradeFailures)
	}
	if app.Status.LastAttemptedReleaseAction != "upgrade" {
		t.Errorf("expected lastAttemptedReleaseAction=upgrade, got %q", app.Status.LastAttemptedReleaseAction)
	}
}

func TestConvertReleaseFailure_ReconcilingNotCopied(t *testing.T) {
	r := newTestRESTWithSchemes()
	hr := makeHelmRelease("postgresql-mydb", "default")
	hr.Status.Conditions = []metav1.Condition{
		{Type: "Reconciling", Status: metav1.ConditionTrue, Reason: "Progressing", Message: "Running 'install' action"},
		{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Running 'install' action"},
	}

	app, err := r.convertHelmReleaseToApplication(context.TODO(), hr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if findCondition(app.GetConditions(), "Reconciling") != nil {
		t.Error("expected Reconciling not to be copied")
	}
	if findCondition(app.GetConditions(), "Ready") == nil {
		t.Error("expected Ready to be copied")
	}
}
