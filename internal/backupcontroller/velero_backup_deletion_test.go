package backupcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

const (
	failedJobNamespace = "tenant-acme"
	failedJobName      = "vm-backup"
)

func newVeleroCleanupReconciler(t *testing.T, objs ...client.Object) (*BackupJobReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(newReconcilerScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}).
		Build()
	return &BackupJobReconciler{Client: c, Recorder: record.NewFakeRecorder(16)}, c
}

func veleroCleanupJob(phase backupsv1alpha1.BackupJobPhase) *backupsv1alpha1.BackupJob {
	started := metav1.Now()
	return &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: failedJobNamespace, Name: failedJobName},
		Status: backupsv1alpha1.BackupJobStatus{
			Phase:     phase,
			StartedAt: &started,
		},
	}
}

func ownedVeleroBackup(name string, phase velerov1.BackupPhase) *velerov1.Backup {
	return &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: veleroNamespace,
			Name:      name,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      failedJobName,
				backupsv1alpha1.OwningJobNamespaceLabel: failedJobNamespace,
			},
		},
		Status: velerov1.BackupStatus{Phase: phase, FailureReason: "object store unreachable"},
	}
}

func veleroCleanupResolved() *ResolvedBackupConfig {
	apiGroup := strategyv1alpha1.GroupVersion.Group
	return &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		APIGroup: &apiGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "velero",
	}}
}

func veleroCleanupStrategy() *strategyv1alpha1.Velero {
	return &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "velero"}}
}

func listDeleteBackupRequests(t *testing.T, c client.Client) []velerov1.DeleteBackupRequest {
	t.Helper()
	l := &velerov1.DeleteBackupRequestList{}
	if err := c.List(context.Background(), l, client.InNamespace(veleroNamespace)); err != nil {
		t.Fatalf("list DeleteBackupRequests: %v", err)
	}
	return l.Items
}

func reconcileFailedJob(t *testing.T, r *BackupJobReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: failedJobNamespace, Name: failedJobName},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getCleanupJob(t *testing.T, c client.Client) *backupsv1alpha1.BackupJob {
	t.Helper()
	j := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: failedJobNamespace, Name: failedJobName}, j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	return j
}

// A Velero backup that ends Failed or PartiallyFailed produces no Backup, so
// the strategy must request its deletion itself, keep the failure message, and
// converge on a single request across reconciles.
func TestReconcileVelero_FailedBackupRequestsDeletion(t *testing.T) {
	for _, phase := range []velerov1.BackupPhase{velerov1.BackupPhaseFailed, velerov1.BackupPhasePartiallyFailed} {
		t.Run(string(phase), func(t *testing.T) {
			vb := ownedVeleroBackup("tenant-acme.vm-backup-x7k2p", phase)
			j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
			r, c := newVeleroCleanupReconciler(t, j, vb, veleroCleanupStrategy())

			if _, err := r.reconcileVelero(context.Background(), j, veleroCleanupResolved()); err != nil {
				t.Fatalf("reconcileVelero: %v", err)
			}

			got := getCleanupJob(t, c)
			if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
				t.Fatalf("expected BackupJob Failed, got %q", got.Status.Phase)
			}
			wantMsg := "Velero Backup failed with phase " + string(phase) + ": object store unreachable"
			if got.Status.Message != wantMsg {
				t.Errorf("message = %q, want %q", got.Status.Message, wantMsg)
			}

			dbrs := listDeleteBackupRequests(t, c)
			if len(dbrs) != 1 || dbrs[0].Spec.BackupName != vb.Name {
				t.Fatalf("expected one DeleteBackupRequest for %s, got %+v", vb.Name, dbrs)
			}

			reconcileFailedJob(t, r)
			if n := len(listDeleteBackupRequests(t, c)); n != 1 {
				t.Fatalf("a second reconcile must not add a request, got %d", n)
			}
		})
	}
}

func TestReconcileVelero_CompletedBackupRequestsNoDeletion(t *testing.T) {
	vb := ownedVeleroBackup("tenant-acme.vm-backup-x7k2p", velerov1.BackupPhaseCompleted)
	j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
	r, c := newVeleroCleanupReconciler(t, j, vb, veleroCleanupStrategy())

	if _, err := r.reconcileVelero(context.Background(), j, veleroCleanupResolved()); err != nil {
		t.Fatalf("reconcileVelero: %v", err)
	}
	if got := getCleanupJob(t, c); got.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded {
		t.Fatalf("expected BackupJob Succeeded, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	reconcileFailedJob(t, r)
	if dbrs := listDeleteBackupRequests(t, c); len(dbrs) != 0 {
		t.Fatalf("a succeeded run must not request deletion, got %+v", dbrs)
	}
}

func TestReconcileVelero_SeveralVeleroBackupsFailWithMessage(t *testing.T) {
	j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
	r, c := newVeleroCleanupReconciler(t, j, veleroCleanupStrategy(),
		ownedVeleroBackup("vb-b", velerov1.BackupPhaseFailed),
		ownedVeleroBackup("vb-a", velerov1.BackupPhaseCompleted))

	if _, err := r.reconcileVelero(context.Background(), j, veleroCleanupResolved()); err != nil {
		t.Fatalf("reconcileVelero: %v", err)
	}
	got := getCleanupJob(t, c)
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed || got.Status.CompletedAt == nil {
		t.Fatalf("expected BackupJob Failed with completedAt, got %+v", got.Status)
	}
	if want := "more than one Velero Backup references this BackupJob: vb-a, vb-b"; got.Status.Message != want {
		t.Errorf("message = %q, want %q", got.Status.Message, want)
	}

	reconcileFailedJob(t, r)
	if n := len(listDeleteBackupRequests(t, c)); n != 2 {
		t.Fatalf("expected a request for each Velero backup, got %d", n)
	}
}

// A job that failed under a controller version without the cleanup still
// holds its Velero backup; reconciling it must request the deletion.
func TestReconcile_FailedJobLeftoverVeleroBackup(t *testing.T) {
	vbName := "tenant-acme.vm-backup-x7k2p"
	cases := []struct {
		name        string
		veleroPhase velerov1.BackupPhase
		jobPhase    backupsv1alpha1.BackupJobPhase
		existing    []client.Object
		wantNames   []string
		wantRequeue bool
	}{
		{
			name:        "no request yet",
			veleroPhase: velerov1.BackupPhasePartiallyFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			wantNames:   []string{vbName},
		},
		{
			name:        "pending request from elsewhere",
			veleroPhase: velerov1.BackupPhaseFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			existing: []client.Object{&velerov1.DeleteBackupRequest{
				ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: vbName + "-abcde"},
				Spec:       velerov1.DeleteBackupRequestSpec{BackupName: vbName},
				Status:     velerov1.DeleteBackupRequestStatus{Phase: velerov1.DeleteBackupRequestPhaseInProgress},
			}},
			wantNames: []string{vbName + "-abcde"},
		},
		{
			name:        "own request processed with errors is replaced",
			veleroPhase: velerov1.BackupPhaseFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			existing: []client.Object{&velerov1.DeleteBackupRequest{
				ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: vbName},
				Spec:       velerov1.DeleteBackupRequestSpec{BackupName: vbName},
				Status: velerov1.DeleteBackupRequestStatus{
					Phase:  velerov1.DeleteBackupRequestPhaseProcessed,
					Errors: []string{"backup is still in progress"},
				},
			}},
			wantNames: []string{vbName},
		},
		{
			name:        "completed backup owned by a Backup",
			veleroPhase: velerov1.BackupPhaseCompleted,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			existing: []client.Object{&backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Namespace: failedJobNamespace, Name: failedJobName},
				Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
					veleroBackupNameMetadataKey:      vbName,
					veleroBackupNamespaceMetadataKey: veleroNamespace,
				}},
			}},
		},
		{
			name:        "backup still running",
			veleroPhase: velerov1.BackupPhaseInProgress,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			wantRequeue: true,
		},
		{
			name:        "succeeded job",
			veleroPhase: velerov1.BackupPhaseCompleted,
			jobPhase:    backupsv1alpha1.BackupJobPhaseSucceeded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]client.Object{veleroCleanupJob(tc.jobPhase), ownedVeleroBackup(vbName, tc.veleroPhase)}, tc.existing...)
			r, c := newVeleroCleanupReconciler(t, objs...)

			res := reconcileFailedJob(t, r)
			if (res.RequeueAfter > 0) != tc.wantRequeue {
				t.Errorf("RequeueAfter = %v, want requeue %v", res.RequeueAfter, tc.wantRequeue)
			}

			dbrs := listDeleteBackupRequests(t, c)
			if len(dbrs) != len(tc.wantNames) {
				t.Fatalf("expected requests %v, got %+v", tc.wantNames, dbrs)
			}
			for i, want := range tc.wantNames {
				if dbrs[i].Name != want || dbrs[i].Spec.BackupName != vbName {
					t.Errorf("request %d = %s for %s, want %s for %s", i, dbrs[i].Name, dbrs[i].Spec.BackupName, want, vbName)
				}
				if dbrs[i].Status.Phase == velerov1.DeleteBackupRequestPhaseProcessed {
					t.Errorf("request %s is the processed one, expected a fresh request", dbrs[i].Name)
				}
			}
		})
	}
}
