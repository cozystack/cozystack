package backupcontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const (
	servedGroup   = "strategy.example.test"
	servedKind    = "Dump"
	unservedGroup = "absent.example.test"
)

var dispatchEpoch = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

func dispatchScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := backupsv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// dispatchMapper serves exactly one strategy kind, as an API server with one
// driver's CRD installed would.
func dispatchMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: servedGroup, Version: "v1alpha1"}})
	m.Add(schema.GroupVersionKind{Group: servedGroup, Version: "v1alpha1", Kind: servedKind}, meta.RESTScopeRoot)
	return m
}

func strategyRef(group string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{APIGroup: new(group), Kind: servedKind, Name: "nightly"}
}

func classBinding(appKind, strategyGroup string) *backupsv1alpha1.BackupClass {
	return &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: backupsv1alpha1.BackupClassSpec{Strategies: []backupsv1alpha1.BackupClassStrategy{{
			Application: backupsv1alpha1.ApplicationSelector{Kind: appKind},
			StrategyRef: strategyRef(strategyGroup),
		}}},
	}
}

func backupJob(created time.Time) *backupsv1alpha1.BackupJob {
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "job", CreationTimestamp: metav1.NewTime(created)},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  corev1.TypedLocalObjectReference{Kind: "Widget", Name: "w"},
			BackupClassName: "default",
		},
	}
}

func readyCondition(conditions []metav1.Condition) metav1.Condition {
	if c := meta.FindStatusCondition(conditions, conditionReady); c != nil {
		return *c
	}
	return metav1.Condition{}
}

func TestBackupJobDispatcher(t *testing.T) {
	started := metav1.NewTime(dispatchEpoch)

	tests := []struct {
		name        string
		class       *backupsv1alpha1.BackupClass
		job         *backupsv1alpha1.BackupJob
		wantPhase   backupsv1alpha1.BackupJobPhase
		wantReason  string
		wantMessage string
		wantRequeue bool
	}{
		{
			name:        "class binds no strategy to the kind",
			class:       classBinding("Gadget", servedGroup),
			job:         backupJob(dispatchEpoch),
			wantPhase:   backupsv1alpha1.BackupJobPhaseFailed,
			wantReason:  reasonBackupFailed,
			wantMessage: `does not bind a strategy for Kind "Widget"`,
		},
		{
			name:        "class does not exist yet",
			job:         backupJob(dispatchEpoch),
			wantReason:  reasonBackupClassNotFound,
			wantMessage: `BackupClass "default" not found`,
			wantRequeue: true,
		},
		{
			name:        "strategy kind is not served, within the grace",
			class:       classBinding("Widget", unservedGroup),
			job:         backupJob(dispatchEpoch.Add(-DriverInstallGrace + time.Minute)),
			wantReason:  reasonDriverNotInstalled,
			wantMessage: "no backup driver is installed for strategy absent.example.test/Dump",
			wantRequeue: true,
		},
		{
			name:        "strategy kind is not served, past the grace",
			class:       classBinding("Widget", unservedGroup),
			job:         backupJob(dispatchEpoch.Add(-DriverInstallGrace - time.Minute)),
			wantPhase:   backupsv1alpha1.BackupJobPhaseFailed,
			wantReason:  reasonBackupFailed,
			wantMessage: "no backup driver is installed for strategy absent.example.test/Dump",
		},
		{
			name:  "strategy kind is served: the job is the driver's",
			class: classBinding("Widget", servedGroup),
			job:   backupJob(dispatchEpoch.Add(-24 * time.Hour)),
		},
		{
			name:  "a job a driver has started is left alone whatever it resolves to",
			class: classBinding("Gadget", servedGroup),
			job: func() *backupsv1alpha1.BackupJob {
				j := backupJob(dispatchEpoch.Add(-24 * time.Hour))
				j.Status.StartedAt = &started
				j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
				return j
			}(),
			wantPhase: backupsv1alpha1.BackupJobPhaseRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{tt.job}
			if tt.class != nil {
				objects = append(objects, tt.class)
			}
			c := fake.NewClientBuilder().WithScheme(dispatchScheme(t)).
				WithObjects(objects...).WithStatusSubresource(&backupsv1alpha1.BackupJob{}).Build()
			r := &BackupJobDispatcher{Client: c, RESTMapper: dispatchMapper(), now: func() time.Time { return dispatchEpoch }}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tt.job)})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got := res.RequeueAfter > 0; got != tt.wantRequeue {
				t.Errorf("requeue = %v, want %v", got, tt.wantRequeue)
			}

			got := &backupsv1alpha1.BackupJob{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(tt.job), got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			cond := readyCondition(got.Status.Conditions)
			if cond.Reason != tt.wantReason {
				t.Errorf("Ready reason = %q, want %q", cond.Reason, tt.wantReason)
			}
			if !strings.Contains(cond.Message, tt.wantMessage) {
				t.Errorf("Ready message = %q, want it to contain %q", cond.Message, tt.wantMessage)
			}
			if failed := tt.wantPhase == backupsv1alpha1.BackupJobPhaseFailed; failed != (got.Status.CompletedAt != nil) {
				t.Errorf("completedAt set = %v, want %v", got.Status.CompletedAt != nil, failed)
			}
		})
	}
}

func TestRestoreJobDispatcher(t *testing.T) {
	started := metav1.NewTime(dispatchEpoch)

	restoreJob := func(created time.Time) *backupsv1alpha1.RestoreJob {
		return &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "restore", CreationTimestamp: metav1.NewTime(created)},
			Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "backup"}},
		}
	}
	backup := func(strategyGroup string) *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "backup"},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{Kind: "Widget", Name: "w"},
				StrategyRef:    strategyRef(strategyGroup),
			},
		}
	}

	tests := []struct {
		name        string
		backup      *backupsv1alpha1.Backup
		job         *backupsv1alpha1.RestoreJob
		wantPhase   backupsv1alpha1.RestoreJobPhase
		wantReason  string
		wantMessage string
		wantRequeue bool
	}{
		{
			name:        "the Backup is gone",
			job:         restoreJob(dispatchEpoch),
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantReason:  reasonRestoreFailed,
			wantMessage: "failed to get Backup",
		},
		{
			name:        "strategy kind is not served, within the grace",
			backup:      backup(unservedGroup),
			job:         restoreJob(dispatchEpoch.Add(-time.Minute)),
			wantReason:  reasonDriverNotInstalled,
			wantMessage: "no backup driver is installed for strategy absent.example.test/Dump",
			wantRequeue: true,
		},
		{
			name:        "strategy kind is not served, past the grace",
			backup:      backup(unservedGroup),
			job:         restoreJob(dispatchEpoch.Add(-DriverInstallGrace - time.Minute)),
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantReason:  reasonRestoreFailed,
			wantMessage: "no backup driver is installed for strategy absent.example.test/Dump",
		},
		{
			name:   "strategy kind is served: the job is the driver's",
			backup: backup(servedGroup),
			job:    restoreJob(dispatchEpoch.Add(-24 * time.Hour)),
		},
		{
			name: "a job a driver has started is left alone though its Backup is gone",
			job: func() *backupsv1alpha1.RestoreJob {
				j := restoreJob(dispatchEpoch.Add(-24 * time.Hour))
				j.Status.StartedAt = &started
				j.Status.Phase = backupsv1alpha1.RestoreJobPhaseRunning
				return j
			}(),
			wantPhase: backupsv1alpha1.RestoreJobPhaseRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{tt.job}
			if tt.backup != nil {
				objects = append(objects, tt.backup)
			}
			c := fake.NewClientBuilder().WithScheme(dispatchScheme(t)).
				WithObjects(objects...).WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).Build()
			r := &RestoreJobDispatcher{Client: c, RESTMapper: dispatchMapper(), now: func() time.Time { return dispatchEpoch }}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tt.job)})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got := res.RequeueAfter > 0; got != tt.wantRequeue {
				t.Errorf("requeue = %v, want %v", got, tt.wantRequeue)
			}

			got := &backupsv1alpha1.RestoreJob{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(tt.job), got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			cond := readyCondition(got.Status.Conditions)
			if cond.Reason != tt.wantReason {
				t.Errorf("Ready reason = %q, want %q", cond.Reason, tt.wantReason)
			}
			if !strings.Contains(cond.Message, tt.wantMessage) {
				t.Errorf("Ready message = %q, want it to contain %q", cond.Message, tt.wantMessage)
			}
		})
	}
}
