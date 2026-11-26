package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

const (
	failedJobNamespace = "tenant-acme"
	failedJobName      = "vm-backup"
	failedVeleroBackup = "tenant-acme.vm-backup-x7k2p"
)

func newVeleroCleanupClient(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().
		WithScheme(newReconcilerScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func newVeleroCleanupReconciler(t *testing.T, objs ...client.Object) (*BackupJobReconciler, client.Client) {
	t.Helper()
	c := newVeleroCleanupClient(t, nil, objs...)
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

func referencingBackup(veleroBackupName string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: failedJobNamespace, Name: failedJobName},
		Spec: backupsv1alpha1.BackupSpec{DriverMetadata: map[string]string{
			veleroBackupNameMetadataKey:      veleroBackupName,
			veleroBackupNamespaceMetadataKey: veleroNamespace,
		}},
	}
}

func deleteRequest(name string, phase velerov1.DeleteBackupRequestPhase, errs ...string) *velerov1.DeleteBackupRequest {
	return &velerov1.DeleteBackupRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: name, UID: types.UID(name + "-uid")},
		Spec:       velerov1.DeleteBackupRequestSpec{BackupName: failedVeleroBackup},
		Status:     velerov1.DeleteBackupRequestStatus{Phase: phase, Errors: errs},
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

func reconcileCleanupJob(t *testing.T, r *BackupJobReconciler) ctrl.Result {
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

// A Velero backup that ends Failed or PartiallyFailed produces no Backup. The
// run keeps its failure message, and the deletion is requested by the
// reconcile that follows the persisted Failed phase, once.
func TestReconcileVelero_FailedBackupRequestsDeletionAfterFailedIsPersisted(t *testing.T) {
	for _, phase := range []velerov1.BackupPhase{velerov1.BackupPhaseFailed, velerov1.BackupPhasePartiallyFailed} {
		t.Run(string(phase), func(t *testing.T) {
			j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
			r, c := newVeleroCleanupReconciler(t, j, ownedVeleroBackup(failedVeleroBackup, phase), veleroCleanupStrategy())

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
			if dbrs := listDeleteBackupRequests(t, c); len(dbrs) != 0 {
				t.Fatalf("deletion must wait for the next reconcile, got %+v", dbrs)
			}

			if res := reconcileCleanupJob(t, r); res.RequeueAfter != failedJobSweepRequeue {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, failedJobSweepRequeue)
			}
			dbrs := listDeleteBackupRequests(t, c)
			if len(dbrs) != 1 || dbrs[0].Name != failedVeleroBackup || dbrs[0].Spec.BackupName != failedVeleroBackup {
				t.Fatalf("expected one DeleteBackupRequest for %s, got %+v", failedVeleroBackup, dbrs)
			}

			reconcileCleanupJob(t, r)
			if n := len(listDeleteBackupRequests(t, c)); n != 1 {
				t.Fatalf("a second reconcile must not add a request, got %d", n)
			}
		})
	}
}

// If the Failed phase cannot be written, the run is retried and must still
// find its Velero backup to report the failure from.
func TestReconcileVelero_FailedStatusWriteErrorRequestsNoDeletion(t *testing.T) {
	j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
	funcs := &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if bj, ok := obj.(*backupsv1alpha1.BackupJob); ok && bj.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
				return apierrors.NewServiceUnavailable("etcd timeout")
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	}
	c := newVeleroCleanupClient(t, funcs, j, ownedVeleroBackup(failedVeleroBackup, velerov1.BackupPhaseFailed), veleroCleanupStrategy())
	r := &BackupJobReconciler{Client: c, Recorder: record.NewFakeRecorder(16)}

	if _, err := r.reconcileVelero(context.Background(), j, veleroCleanupResolved()); err == nil {
		t.Fatal("expected the status write error to be returned")
	}
	if got := getCleanupJob(t, c); got.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("status write was rejected, got phase %q", got.Status.Phase)
	}
	if dbrs := listDeleteBackupRequests(t, c); len(dbrs) != 0 {
		t.Fatalf("no deletion may be requested before Failed is persisted, got %+v", dbrs)
	}
}

func TestReconcileVelero_CompletedBackupRequestsNoDeletion(t *testing.T) {
	j := veleroCleanupJob(backupsv1alpha1.BackupJobPhaseRunning)
	r, c := newVeleroCleanupReconciler(t, j, ownedVeleroBackup(failedVeleroBackup, velerov1.BackupPhaseCompleted), veleroCleanupStrategy())

	if _, err := r.reconcileVelero(context.Background(), j, veleroCleanupResolved()); err != nil {
		t.Fatalf("reconcileVelero: %v", err)
	}
	if got := getCleanupJob(t, c); got.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded {
		t.Fatalf("expected BackupJob Succeeded, got %q (%s)", got.Status.Phase, got.Status.Message)
	}
	reconcileCleanupJob(t, r)
	if dbrs := listDeleteBackupRequests(t, c); len(dbrs) != 0 {
		t.Fatalf("a succeeded run must not request deletion, got %+v", dbrs)
	}
}

func TestReconcileVelero_SeveralVeleroBackupsKeepCompletedOne(t *testing.T) {
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

	reconcileCleanupJob(t, r)
	dbrs := listDeleteBackupRequests(t, c)
	if len(dbrs) != 1 || dbrs[0].Spec.BackupName != "vb-b" {
		t.Fatalf("expected a request for the failed vb-b only, got %+v", dbrs)
	}
}

// Reconciling a Failed BackupJob, including one that failed under a version
// without the cleanup, requests deletion of its failed Velero backups only.
func TestReconcile_FailedJobVeleroBackupSweep(t *testing.T) {
	foreign := failedVeleroBackup + "-abcde"
	cases := []struct {
		name        string
		veleroPhase velerov1.BackupPhase
		jobPhase    backupsv1alpha1.BackupJobPhase
		existing    []client.Object
		// Backups visible to the uncached reader only.
		uncached     []client.Object
		wantRequests map[string]velerov1.DeleteBackupRequestPhase
		wantRequeue  time.Duration
		wantEvent    bool
	}{
		{
			name:         "PartiallyFailed without a request",
			veleroPhase:  velerov1.BackupPhasePartiallyFailed,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: ""},
			wantRequeue:  failedJobSweepRequeue,
		},
		{
			name:         "FailedValidation is deleted",
			veleroPhase:  velerov1.BackupPhaseFailedValidation,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: ""},
			wantRequeue:  failedJobSweepRequeue,
		},
		{
			name:         "pending request from elsewhere",
			veleroPhase:  velerov1.BackupPhaseFailed,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			existing:     []client.Object{deleteRequest(foreign, velerov1.DeleteBackupRequestPhaseInProgress)},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{foreign: velerov1.DeleteBackupRequestPhaseInProgress},
			wantRequeue:  failedJobSweepRequeue,
		},
		{
			name:        "errored request from elsewhere is left alone",
			veleroPhase: velerov1.BackupPhaseFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			existing:    []client.Object{deleteRequest(foreign, velerov1.DeleteBackupRequestPhaseProcessed, "backup storage location unavailable")},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{
				failedVeleroBackup: "",
				foreign:            velerov1.DeleteBackupRequestPhaseProcessed,
			},
			wantRequeue: failedJobSweepRequeue,
		},
		{
			name:         "own request processed with errors is replaced",
			veleroPhase:  velerov1.BackupPhaseFailed,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			existing:     []client.Object{deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseProcessed, "backup is still in progress")},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: ""},
			wantRequeue:  2 * failedJobSweepRequeue,
			wantEvent:    true,
		},
		{
			name:         "own request processed without errors while the backup exists is replaced",
			veleroPhase:  velerov1.BackupPhaseFailed,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			existing:     []client.Object{deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseProcessed)},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: ""},
			wantRequeue:  2 * failedJobSweepRequeue,
			wantEvent:    true,
		},
		{
			name:         "Deleting with a request in progress",
			veleroPhase:  velerov1.BackupPhaseDeleting,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			existing:     []client.Object{deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseInProgress)},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: velerov1.DeleteBackupRequestPhaseInProgress},
			wantRequeue:  failedJobSweepRequeue,
		},
		{
			name:         "Deleting left by an errored request",
			veleroPhase:  velerov1.BackupPhaseDeleting,
			jobPhase:     backupsv1alpha1.BackupJobPhaseFailed,
			existing:     []client.Object{deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseProcessed, "error deleting backup from object storage")},
			wantRequests: map[string]velerov1.DeleteBackupRequestPhase{failedVeleroBackup: ""},
			wantRequeue:  2 * failedJobSweepRequeue,
			wantEvent:    true,
		},
		{
			name:        "Completed is never deleted",
			veleroPhase: velerov1.BackupPhaseCompleted,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
		},
		{
			name:        "failed backup referenced by a Backup the cache has not seen",
			veleroPhase: velerov1.BackupPhaseFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			uncached:    []client.Object{referencingBackup(failedVeleroBackup)},
		},
		{
			name:        "running backup",
			veleroPhase: velerov1.BackupPhaseInProgress,
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			wantRequeue: failedJobRunningBackupRequeue,
		},
		{
			name:        "new backup",
			veleroPhase: "",
			jobPhase:    backupsv1alpha1.BackupJobPhaseFailed,
			wantRequeue: failedJobRunningBackupRequeue,
		},
		{
			name:        "succeeded job",
			veleroPhase: velerov1.BackupPhaseFailed,
			jobPhase:    backupsv1alpha1.BackupJobPhaseSucceeded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]client.Object{veleroCleanupJob(tc.jobPhase), ownedVeleroBackup(failedVeleroBackup, tc.veleroPhase)}, tc.existing...)
			r, c := newVeleroCleanupReconciler(t, objs...)
			if tc.uncached != nil {
				r.APIReader = newVeleroCleanupClient(t, nil, tc.uncached...)
			}

			if res := reconcileCleanupJob(t, r); res.RequeueAfter != tc.wantRequeue {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, tc.wantRequeue)
			}

			dbrs := listDeleteBackupRequests(t, c)
			if len(dbrs) != len(tc.wantRequests) {
				t.Fatalf("expected requests %v, got %+v", tc.wantRequests, dbrs)
			}
			for _, dbr := range dbrs {
				wantPhase, ok := tc.wantRequests[dbr.Name]
				if !ok || dbr.Spec.BackupName != failedVeleroBackup {
					t.Errorf("unexpected request %s for %s", dbr.Name, dbr.Spec.BackupName)
					continue
				}
				if dbr.Status.Phase != wantPhase {
					t.Errorf("request %s phase = %q, want %q", dbr.Name, dbr.Status.Phase, wantPhase)
				}
			}

			events := r.Recorder.(*record.FakeRecorder).Events
			if got := len(events); (got > 0) != tc.wantEvent {
				t.Errorf("got %d events, want an event: %v", got, tc.wantEvent)
			}
		})
	}
}

func markOwnRequestProcessed(t *testing.T, c client.Client, errs ...string) {
	t.Helper()
	dbr := &velerov1.DeleteBackupRequest{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: veleroNamespace, Name: failedVeleroBackup}, dbr); err != nil {
		t.Fatalf("get request: %v", err)
	}
	dbr.Status = velerov1.DeleteBackupRequestStatus{Phase: velerov1.DeleteBackupRequestPhaseProcessed, Errors: errs}
	if err := c.Update(context.Background(), dbr); err != nil {
		t.Fatalf("mark request processed: %v", err)
	}
}

// A deletion Velero keeps failing at once is retried only after a growing
// wait, even though recording each attempt triggers a new reconcile, and
// every retry is reported on the BackupJob with Velero's errors.
func TestReconcile_FailedJobVeleroDeletionBacksOff(t *testing.T) {
	const readOnly = "backup storage location default is currently in read-only mode"
	r, c := newVeleroCleanupReconciler(t,
		veleroCleanupJob(backupsv1alpha1.BackupJobPhaseFailed),
		ownedVeleroBackup(failedVeleroBackup, velerov1.BackupPhaseFailed))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	events := r.Recorder.(*record.FakeRecorder).Events
	expectEvent := func(step string, want bool) {
		t.Helper()
		select {
		case ev := <-events:
			if !want {
				t.Fatalf("%s: unexpected event %q", step, ev)
			}
			if !strings.Contains(ev, "Warning VeleroBackupDeletionFailed") || !strings.Contains(ev, readOnly) {
				t.Errorf("%s: unexpected event %q", step, ev)
			}
		default:
			if want {
				t.Fatalf("%s: no Warning event", step)
			}
		}
	}

	if res := reconcileCleanupJob(t, r); res.RequeueAfter != failedJobSweepRequeue {
		t.Fatalf("first request: RequeueAfter = %v, want %v", res.RequeueAfter, failedJobSweepRequeue)
	}
	expectEvent("first request", false)

	markOwnRequestProcessed(t, c, readOnly)
	if res := reconcileCleanupJob(t, r); res.RequeueAfter != failedJobSweepRequeue {
		t.Errorf("before the backoff: RequeueAfter = %v, want %v", res.RequeueAfter, failedJobSweepRequeue)
	}
	expectEvent("before the backoff", false)

	for i, wait := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute} {
		now = now.Add(wait)
		step := fmt.Sprintf("retry %d", i+1)
		if res := reconcileCleanupJob(t, r); res.RequeueAfter != 2*wait {
			t.Errorf("%s: RequeueAfter = %v, want %v", step, res.RequeueAfter, 2*wait)
		}
		expectEvent(step, true)

		markOwnRequestProcessed(t, c, readOnly)
		if res := reconcileCleanupJob(t, r); res.RequeueAfter != 2*wait {
			t.Errorf("%s, reconcile right after: RequeueAfter = %v, want %v", step, res.RequeueAfter, 2*wait)
		}
		expectEvent(step+", reconcile right after", false)
		if n := len(listDeleteBackupRequests(t, c)); n != 1 {
			t.Fatalf("%s: expected one request, got %d", step, n)
		}
	}

	if got := failedJobSweepBackoff(10); got != failedJobSweepMaxRequeue {
		t.Errorf("backoff after 10 retries = %v, want the %v cap", got, failedJobSweepMaxRequeue)
	}
}

// Requests and backups Velero has not acted on yet are checked less often the
// longer they wait, and waiting is not reported.
func TestReconcile_FailedJobWaitingOnVeleroBacksOff(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		veleroPhase velerov1.BackupPhase
		existing    []client.Object
		backupAge   time.Duration
		want        time.Duration
	}{
		{name: "request waiting 20m", veleroPhase: velerov1.BackupPhaseFailed, want: 20 * time.Minute,
			existing: []client.Object{agedRequest(now.Add(-20 * time.Minute))}},
		{name: "request waiting 3h", veleroPhase: velerov1.BackupPhaseFailed, want: failedJobSweepMaxRequeue,
			existing: []client.Object{agedRequest(now.Add(-3 * time.Hour))}},
		{name: "request just created", veleroPhase: velerov1.BackupPhaseFailed, want: failedJobSweepRequeue,
			existing: []client.Object{agedRequest(now)}},
		{name: "backup new for 3h", veleroPhase: velerov1.BackupPhaseNew, backupAge: 3 * time.Hour, want: failedJobSweepMaxRequeue},
		{name: "backup new for 10m", veleroPhase: "", backupAge: 10 * time.Minute, want: 10 * time.Minute},
		{name: "backup just started", veleroPhase: velerov1.BackupPhaseInProgress, want: failedJobRunningBackupRequeue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vb := ownedVeleroBackup(failedVeleroBackup, tc.veleroPhase)
			vb.CreationTimestamp = metav1.NewTime(now.Add(-tc.backupAge))
			r, c := newVeleroCleanupReconciler(t, append([]client.Object{veleroCleanupJob(backupsv1alpha1.BackupJobPhaseFailed), vb}, tc.existing...)...)
			r.now = func() time.Time { return now }

			if res := reconcileCleanupJob(t, r); res.RequeueAfter != tc.want {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, tc.want)
			}
			if n := len(listDeleteBackupRequests(t, c)); n != len(tc.existing) {
				t.Errorf("waiting must not create requests, got %d", n)
			}
			if events := r.Recorder.(*record.FakeRecorder).Events; len(events) != 0 {
				t.Errorf("waiting must not be reported, got %q", <-events)
			}
		})
	}
}

func agedRequest(created time.Time) *velerov1.DeleteBackupRequest {
	dbr := deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseInProgress)
	dbr.CreationTimestamp = metav1.NewTime(created)
	return dbr
}

// Recording the attempt can fail after the request was created: a job deleted
// meanwhile ends the sweep, any other error still keeps the backoff requeue.
func TestReconcile_FailedJobAttemptRecordErrors(t *testing.T) {
	for name, patchErr := range map[string]error{
		"job deleted": apierrors.NewNotFound(schema.GroupResource{Group: "backups.cozystack.io", Resource: "backupjobs"}, failedJobName),
		"apiserver":   apierrors.NewServiceUnavailable("etcd timeout"),
	} {
		t.Run(name, func(t *testing.T) {
			funcs := &interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return patchErr
				},
			}
			c := newVeleroCleanupClient(t, funcs,
				veleroCleanupJob(backupsv1alpha1.BackupJobPhaseFailed),
				ownedVeleroBackup(failedVeleroBackup, velerov1.BackupPhaseFailed))
			r := &BackupJobReconciler{Client: c, Recorder: record.NewFakeRecorder(16)}

			want := failedJobSweepRequeue
			if apierrors.IsNotFound(patchErr) {
				want = 0
			}
			if res := reconcileCleanupJob(t, r); res.RequeueAfter != want {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, want)
			}
			if n := len(listDeleteBackupRequests(t, c)); n != 1 {
				t.Errorf("expected the request to stay, got %d", n)
			}
		})
	}
}

func TestReconcile_FailedJobWithoutVeleroCRDs(t *testing.T) {
	funcs := &interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*velerov1.BackupList); ok {
				return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "velero.io", Kind: "Backup"}}
			}
			return c.List(ctx, list, opts...)
		},
	}
	c := newVeleroCleanupClient(t, funcs, veleroCleanupJob(backupsv1alpha1.BackupJobPhaseFailed))
	r := &BackupJobReconciler{Client: c, Recorder: record.NewFakeRecorder(16)}
	if res := reconcileCleanupJob(t, r); res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want none", res.RequeueAfter)
	}
}

func TestRequestVeleroBackupDeletion_ReplacesByUID(t *testing.T) {
	stale := deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseProcessed, "backup storage location unavailable")
	var preconditionUID types.UID
	funcs := &interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			do := &client.DeleteOptions{}
			do.ApplyOptions(opts)
			if do.Preconditions != nil && do.Preconditions.UID != nil {
				preconditionUID = *do.Preconditions.UID
			}
			return c.Delete(ctx, obj, opts...)
		},
	}
	c := newVeleroCleanupClient(t, funcs, stale)
	req, err := requestVeleroBackupDeletion(context.Background(), c, veleroNamespace, failedVeleroBackup, true)
	if err != nil || !req.created || req.replaced == nil {
		t.Fatalf("requestVeleroBackupDeletion = (%+v, %v), want a replaced and a created request", req, err)
	}
	if preconditionUID != stale.UID {
		t.Errorf("Delete precondition UID = %q, want %q", preconditionUID, stale.UID)
	}
}

func TestRequestVeleroBackupDeletion_ReturnsAPIErrors(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "velero.io", Resource: "deletebackuprequests"}, failedVeleroBackup, errors.New("denied"))
	cases := map[string]interceptor.Funcs{
		"list": {List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return forbidden
		}},
		"create": {Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return forbidden
		}},
		"delete": {Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return forbidden
		}},
	}
	for name, funcs := range cases {
		t.Run(name, func(t *testing.T) {
			c := newVeleroCleanupClient(t, &funcs,
				deleteRequest(failedVeleroBackup, velerov1.DeleteBackupRequestPhaseProcessed, "backup storage location unavailable"))
			req, err := requestVeleroBackupDeletion(context.Background(), c, veleroNamespace, failedVeleroBackup, true)
			if req.created || !apierrors.IsForbidden(err) {
				t.Fatalf("requestVeleroBackupDeletion = (%+v, %v), want a Forbidden error", req, err)
			}
		})
	}
}
