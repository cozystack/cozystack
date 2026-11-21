package backupcontroller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
)

const pendingTargetEvent = "Backup target pod not ready: postgres-app-1"

// newPendingBoundFixture is a Cluster that already runs the barman-cloud
// plugin, so the driver goes straight to the job's cnpg.io/Backup, which the
// test creates in the given phase. stalledSince, when set, seeds the
// condition an earlier pass recorded when it first saw the Backup stalled.
func newPendingBoundFixture(t *testing.T, phase string, stalledSince *time.Time) (*cnpgRolloutFixture, *cnpgtypes.Backup) {
	t.Helper()
	j := newRolloutBackupJob(time.Now().Add(-2 * cnpgDefaultBackupDeadline))
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	if stalledSince != nil {
		j.Status.Conditions = []metav1.Condition{{
			Type:               backupCondCNPGBackupStalled,
			Status:             metav1.ConditionTrue,
			Reason:             "NotStarted",
			Message:            "cnpg.io/Backup tenant/bj-cnpg is pending and CloudNativePG has not started it: " + pendingTargetEvent,
			LastTransitionTime: metav1.NewTime(*stalledSince),
		}}
	}
	f := newCNPGRolloutFixture(t, j,
		newRolloutCluster(cnpgClusterHealthyPhase, buildBarmanPlugin(rolloutCluster, rolloutCluster)),
		newRolloutInstancePod(time.Now().Add(-time.Hour)))
	b := newPendingBoundCNPGBackup("bj-cnpg", time.Now().Add(-time.Hour), phase, map[string]string{
		backupsv1alpha1.OwningJobNameLabel:      "bj",
		backupsv1alpha1.OwningJobNamespaceLabel: rolloutNS,
	})
	f.create(b)
	return f, b
}

func newPendingBoundCNPGBackup(name string, created time.Time, phase string, labels map[string]string) *cnpgtypes.Backup {
	return &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         rolloutNS,
			Name:              name,
			UID:               types.UID(name + "-uid"),
			CreationTimestamp: metav1.NewTime(created),
			Labels:            labels,
		},
		Spec:   cnpgtypes.BackupSpec{Cluster: cnpgtypes.ClusterReference{Name: rolloutCluster}},
		Status: cnpgtypes.BackupStatus{Phase: phase},
	}
}

func (f *cnpgRolloutFixture) create(obj client.Object) {
	f.t.Helper()
	if err := f.c.Create(context.Background(), obj); err != nil {
		f.t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
}

// recordCNPGEvent stands in for the event CloudNativePG records on a Backup it
// keeps pending because the target instance is not ready.
func (f *cnpgRolloutFixture) recordCNPGEvent(b *cnpgtypes.Backup, eventType, reason, message string, at time.Time) {
	f.t.Helper()
	f.create(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: rolloutNS, Name: b.Name + "." + reason + "." + at.Format("150405")},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: cnpgtypes.GroupVersion.String(),
			Kind:       "Backup",
			Namespace:  b.Namespace,
			Name:       b.Name,
			UID:        b.UID,
		},
		Type:          eventType,
		Reason:        reason,
		Message:       message,
		LastTimestamp: metav1.NewTime(at),
	})
}

func (f *cnpgRolloutFixture) setCNPGPhase(name, phase string) {
	f.t.Helper()
	b := &cnpgtypes.Backup{}
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: rolloutNS, Name: name}, b); err != nil {
		f.t.Fatalf("get cnpg.io/Backup %s: %v", name, err)
	}
	b.Status.Phase = phase
	if err := f.c.Update(context.Background(), b); err != nil {
		f.t.Fatalf("update cnpg.io/Backup %s: %v", name, err)
	}
}

func cnpgBackupNames(backups []cnpgtypes.Backup) []string {
	names := make([]string, 0, len(backups))
	for _, b := range backups {
		names = append(names, b.Name)
	}
	return names
}

// TestReconcileCNPG_PendingBackupCarriesCNPGReason: a Backup CloudNativePG
// keeps pending because its target instance is not ready is not failed before
// the bound, and the BackupJob says why CloudNativePG holds it, which
// CloudNativePG records only as an event on the Backup.
func TestReconcileCNPG_PendingBackupCarriesCNPGReason(t *testing.T) {
	f, b := newPendingBoundFixture(t, "pending", nil)
	f.recordCNPGEvent(b, corev1.EventTypeWarning, "FindingPod", "Couldn't find target pod postgres-app-1, will retry in 30 seconds", time.Now().Add(-time.Hour))
	f.recordCNPGEvent(b, corev1.EventTypeWarning, "BackupPending", pendingTargetEvent, time.Now().Add(-2*time.Minute))
	f.recordCNPGEvent(b, corev1.EventTypeNormal, "ReStarting", "Restarted backup for cluster postgres-app on instance postgres-app-1", time.Now().Add(-time.Minute))

	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Fatalf("BackupJob phase = %q (%s), want Running before the bound", j.Status.Phase, j.Status.Message)
	}
	if len(backups) != 1 {
		t.Fatalf("cnpg.io/Backups = %v, want the job's one kept", cnpgBackupNames(backups))
	}
	stalled := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if stalled == nil || stalled.Status != metav1.ConditionTrue {
		t.Fatalf("%s condition = %+v, want True", backupCondCNPGBackupStalled, stalled)
	}
	if time.Since(stalled.LastTransitionTime.Time) > time.Minute {
		t.Errorf("%s started at %s, want now: the bound counts from when the stall was first seen", backupCondCNPGBackupStalled, stalled.LastTransitionTime)
	}
	ready := apimeta.FindStatusCondition(j.Status.Conditions, "Ready")
	if ready == nil || ready.Reason != "WaitingForCNPGBackup" {
		t.Fatalf("Ready condition = %+v, want WaitingForCNPGBackup", ready)
	}
	for _, want := range []string{"bj-cnpg", "BackupPending", pendingTargetEvent} {
		if !strings.Contains(ready.Message, want) {
			t.Errorf("Ready message %q does not mention %q", ready.Message, want)
		}
	}
	for _, unwanted := range []string{"FindingPod", "Restarted backup"} {
		if strings.Contains(ready.Message, unwanted) {
			t.Errorf("Ready message %q carries %q instead of the newest Warning", ready.Message, unwanted)
		}
	}
}

// TestReconcileCNPG_StalledPendingBackupFailsAtBound: once the Backup has been
// stalled for the bound, the BackupJob fails with the cause and the Backup is
// deleted, so CloudNativePG does not run it later with no Backup recording it.
func TestReconcileCNPG_StalledPendingBackupFailsAtBound(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, b := newPendingBoundFixture(t, "pending", &since)
	f.recordCNPGEvent(b, corev1.EventTypeWarning, "BackupPending", pendingTargetEvent, time.Now().Add(-time.Minute))

	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob phase = %q, want Failed once the Backup is stalled past the bound", j.Status.Phase)
	}
	if len(backups) != 0 {
		t.Fatalf("cnpg.io/Backups = %v, want the abandoned one deleted", cnpgBackupNames(backups))
	}
	for _, want := range []string{"bj-cnpg", pendingTargetEvent, cnpgDefaultBackupDeadline.String(), "deleted"} {
		if !strings.Contains(j.Status.Message, want) {
			t.Errorf("failure message %q does not mention %q", j.Status.Message, want)
		}
	}

	// A further pass, as after a lost status write, must not create a new
	// cnpg.io/Backup for a job that gave up.
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	if err := f.c.Status().Update(context.Background(), j); err != nil {
		t.Fatalf("roll the BackupJob back to Running: %v", err)
	}
	j, backups = f.pass()
	if len(backups) != 0 {
		t.Fatalf("cnpg.io/Backups = %v after the job gave up, want none recreated", cnpgBackupNames(backups))
	}
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("BackupJob phase = %q, want Failed", j.Status.Phase)
	}
	if !strings.Contains(j.Status.Message, pendingTargetEvent) {
		t.Errorf("failure message %q after a lost status write does not carry the recorded reason", j.Status.Message)
	}
}

// TestReconcileCNPG_StalledBackupThatStartsIsNotDeleted: CloudNativePG may
// start the Backup between the driver's read and its delete. The delete is
// conditional on the Backup it read, so a Backup that moved on is kept and
// the job is not failed.
func TestReconcileCNPG_StalledBackupThatStartsIsNotDeleted(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*cnpgtypes.Backup); ok {
				f.setCNPGPhase(obj.GetName(), "started")
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})

	ctx := context.Background()
	j := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	_, err := f.r.reconcileCNPG(ctx, j, f.resolved)
	if !apierrors.IsConflict(err) {
		t.Fatalf("reconcileCNPG error = %v, want the Conflict from the conditional delete", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, persisted); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob failed although its Backup started: %s", persisted.Status.Message)
	}
	b := &cnpgtypes.Backup{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj-cnpg"}, b); err != nil {
		t.Fatalf("the Backup CloudNativePG started was deleted: %v", err)
	}
}

// TestReconcileCNPG_QueuedBackupIsNotBounded: CloudNativePG keeps a Backup
// pending while another backup of the same Cluster runs. That wait is healthy
// and does not count toward the bound, however long the job has been stalled
// before it.
func TestReconcileCNPG_QueuedBackupIsNotBounded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aheadPhase string
		aheadAge   time.Duration
	}{
		{name: "another backup runs", aheadPhase: "running", aheadAge: time.Minute},
		{name: "another backup started", aheadPhase: "started", aheadAge: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
			f, _ := newPendingBoundFixture(t, "pending", &since)
			f.create(newPendingBoundCNPGBackup("scheduled", time.Now().Add(-tc.aheadAge), tc.aheadPhase, nil))

			j, backups := f.pass()
			if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
				t.Fatalf("BackupJob phase = %q (%s), want Running while queued", j.Status.Phase, j.Status.Message)
			}
			if len(backups) != 2 {
				t.Fatalf("cnpg.io/Backups = %v, want both kept", cnpgBackupNames(backups))
			}
			if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
				t.Fatalf("%s condition = %+v, want False while queued, so the next stall counts afresh", backupCondCNPGBackupStalled, cond)
			}
			ready := apimeta.FindStatusCondition(j.Status.Conditions, "Ready")
			if ready == nil || !strings.Contains(ready.Message, "scheduled") {
				t.Errorf("Ready condition = %+v, want it to name the backup it waits behind", ready)
			}
		})
	}
}

// TestReconcileCNPG_QueueIsPerCluster: backups of another Cluster in the same
// namespace, and finished backups of this one, do not hold the Backup back.
func TestReconcileCNPG_QueueIsPerCluster(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	other := newPendingBoundCNPGBackup("other-cluster", time.Now().Add(-2*time.Hour), "running", nil)
	other.Spec.Cluster.Name = "postgres-other"
	f.create(other)
	f.create(newPendingBoundCNPGBackup("done", time.Now().Add(-2*time.Hour), cnpgBackupPhaseComplete, nil))
	f.create(newPendingBoundCNPGBackup("broken", time.Now().Add(-2*time.Hour), cnpgBackupPhaseFailed, nil))

	j, _ := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob phase = %q (%s), want Failed: nothing of this Cluster is ahead of the stalled Backup", j.Status.Phase, j.Status.Message)
	}
	for _, unwanted := range []string{"other-cluster", "done", "broken"} {
		if strings.Contains(j.Status.Message, unwanted) {
			t.Errorf("failure message %q names %s, which does not hold the Backup", j.Status.Message, unwanted)
		}
	}
}

// TestReconcileCNPG_StallCountsAfresh: CloudNativePG puts a Backup back into
// pending when it restarts it on another instance. A stall that ended, here
// by the Backup starting, does not carry its time into the next one.
func TestReconcileCNPG_StallCountsAfresh(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "started", &since)

	j, _ := f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want False once the Backup started", backupCondCNPGBackupStalled, cond)
	}

	f.setCNPGPhase("bj-cnpg", "pending")
	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Fatalf("BackupJob phase = %q (%s), want Running: the new stall has just begun", j.Status.Phase, j.Status.Message)
	}
	if len(backups) != 1 {
		t.Fatalf("cnpg.io/Backups = %v, want the restarted one kept", cnpgBackupNames(backups))
	}
	cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if cond == nil || cond.Status != metav1.ConditionTrue || time.Since(cond.LastTransitionTime.Time) > time.Minute {
		t.Fatalf("%s condition = %+v, want True from now", backupCondCNPGBackupStalled, cond)
	}
}

// TestReconcileCNPG_UnreadableEventsDoNotHideTheStall: without the event the
// BackupJob still reports the stall and still fails at the bound, and says
// the reason could not be read rather than omitting it.
func TestReconcileCNPG_UnreadableEventsDoNotHideTheStall(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.EventList); ok {
				return apierrors.NewForbidden(corev1.Resource("events"), "", errors.New("no list grant"))
			}
			return cl.List(ctx, list, opts...)
		},
	})

	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob phase = %q (%s), want Failed at the bound", j.Status.Phase, j.Status.Message)
	}
	if len(backups) != 0 {
		t.Fatalf("cnpg.io/Backups = %v, want the abandoned one deleted", cnpgBackupNames(backups))
	}
	if !strings.Contains(j.Status.Message, "forbidden") {
		t.Errorf("failure message %q does not say why the CloudNativePG reason is missing", j.Status.Message)
	}
}

// TestReconcileCNPG_StalledBackupAlreadyGoneStillFails: a Backup that is gone
// by the time the driver deletes it, for example removed by hand, still ends
// the job, which otherwise would retry the delete forever.
func TestReconcileCNPG_StalledBackupAlreadyGoneStillFails(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if err := cl.Delete(ctx, obj); err != nil {
				return err
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})

	j, _ := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob phase = %q (%s), want Failed", j.Status.Phase, j.Status.Message)
	}
}

// TestReconcileCNPG_EndedStallDoesNotFailTheJob: only a stall still going on
// counts. A job whose Backup was queued, and then removed, gets a new Backup
// rather than failing on the time of a stall that is over.
func TestReconcileCNPG_EndedStallDoesNotFailTheJob(t *testing.T) {
	j := newRolloutBackupJob(time.Now().Add(-2 * cnpgDefaultBackupDeadline))
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	j.Status.Conditions = []metav1.Condition{{
		Type:               backupCondCNPGBackupStalled,
		Status:             metav1.ConditionFalse,
		Reason:             "Queued",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)),
	}}
	f := newCNPGRolloutFixture(t, j,
		newRolloutCluster(cnpgClusterHealthyPhase, buildBarmanPlugin(rolloutCluster, rolloutCluster)),
		newRolloutInstancePod(time.Now().Add(-time.Hour)))

	got, backups := f.pass()
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Fatalf("BackupJob phase = %q (%s), want Running", got.Status.Phase, got.Status.Message)
	}
	if len(backups) != 1 {
		t.Fatalf("cnpg.io/Backups = %v, want a new one", cnpgBackupNames(backups))
	}
}

// TestReconcileCNPG_ProgressingBackupGetsNoStallCondition: a Backup that runs
// without ever stalling leaves no stall condition on the job.
func TestReconcileCNPG_ProgressingBackupGetsNoStallCondition(t *testing.T) {
	f, _ := newPendingBoundFixture(t, "running", nil)

	j, _ := f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond != nil {
		t.Fatalf("%s condition = %+v on a job whose Backup never stalled", backupCondCNPGBackupStalled, cond)
	}
}

// TestReconcileCNPG_BackupAmongPendingOnesIsBounded: other pending backups of
// the Cluster do not make the wait healthy, whichever of them CloudNativePG
// would let through first. With no warning on its own Backup, the job fails at
// the bound with the reason recorded on another pending one, and deletes its
// own Backup only: the other may belong to another job, or to a
// ScheduledBackup.
func TestReconcileCNPG_BackupAmongPendingOnesIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		otherPhase string
		otherAge   time.Duration
		otherName  string
	}{
		{name: "an older backup is pending", otherPhase: "pending", otherAge: 2 * time.Hour, otherName: "scheduled"},
		{name: "an older backup not yet seen by CloudNativePG", otherPhase: "", otherAge: 2 * time.Hour, otherName: "scheduled"},
		{name: "a newer backup is pending", otherPhase: "pending", otherAge: -time.Minute, otherName: "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
			f, _ := newPendingBoundFixture(t, "pending", &since)
			other := newPendingBoundCNPGBackup(tc.otherName, time.Now().Add(-tc.otherAge), tc.otherPhase, nil)
			f.create(other)
			f.recordCNPGEvent(other, corev1.EventTypeWarning, "BackupPending", pendingTargetEvent, time.Now().Add(-time.Minute))

			j, backups := f.pass()
			if j.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
				t.Fatalf("BackupJob phase = %q (%s), want Failed", j.Status.Phase, j.Status.Message)
			}
			if names := cnpgBackupNames(backups); len(names) != 1 || names[0] != tc.otherName {
				t.Fatalf("cnpg.io/Backups = %v, want only %s, which the job does not own", names, tc.otherName)
			}
			for _, want := range []string{tc.otherName, pendingTargetEvent} {
				if !strings.Contains(j.Status.Message, want) {
					t.Errorf("failure message %q does not mention %q", j.Status.Message, want)
				}
			}
		})
	}
}

// TestReconcileCNPG_OwnReasonComesFirst: a warning on the job's own Backup is
// its reason, even when another pending backup of the Cluster carries a newer
// one.
func TestReconcileCNPG_OwnReasonComesFirst(t *testing.T) {
	f, b := newPendingBoundFixture(t, "pending", nil)
	other := newPendingBoundCNPGBackup("other", b.CreationTimestamp.Time, "pending", nil)
	f.create(other)
	f.recordCNPGEvent(b, corev1.EventTypeWarning, "BackupPending", "own reason", time.Now().Add(-time.Hour))
	f.recordCNPGEvent(other, corev1.EventTypeWarning, "BackupPending", "other reason", time.Now().Add(-time.Minute))

	j, _ := f.pass()
	stalled := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if stalled == nil || !strings.Contains(stalled.Message, "own reason") || strings.Contains(stalled.Message, "other reason") {
		t.Fatalf("%s condition = %+v, want the reason of the job's own Backup only", backupCondCNPGBackupStalled, stalled)
	}
}

// TestCNPGEventTime: the time an event was last seen, whichever field the
// recorder filled in. core/v1 recorders set LastTimestamp, events.k8s.io ones
// EventTime and, for a repeated event, Series.
func TestCNPGEventTime(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		e    corev1.Event
		want time.Time
	}{
		{name: "series", want: base.Add(3 * time.Hour), e: corev1.Event{
			Series:    &corev1.EventSeries{LastObservedTime: metav1.NewMicroTime(base.Add(3 * time.Hour))},
			EventTime: metav1.NewMicroTime(base.Add(time.Hour)),
		}},
		{name: "last timestamp", want: base.Add(2 * time.Hour), e: corev1.Event{
			LastTimestamp: metav1.NewTime(base.Add(2 * time.Hour)),
			EventTime:     metav1.NewMicroTime(base.Add(time.Hour)),
		}},
		{name: "event time", want: base.Add(time.Hour), e: corev1.Event{
			EventTime:  metav1.NewMicroTime(base.Add(time.Hour)),
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(base)},
		}},
		{name: "creation", want: base, e: corev1.Event{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(base)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cnpgEventTime(&tc.e); !got.Equal(tc.want) {
				t.Errorf("cnpgEventTime = %s, want %s", got, tc.want)
			}
		})
	}
}

// recordEvent records a Warning on obj under an explicit name, which is the
// order the fake client lists events in.
func (f *cnpgRolloutFixture) recordEvent(name string, obj *cnpgtypes.Backup, message string, at time.Time) {
	f.t.Helper()
	f.create(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: rolloutNS, Name: name},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: cnpgtypes.GroupVersion.String(),
			Kind:       "Backup",
			Namespace:  obj.Namespace,
			Name:       obj.Name,
			UID:        obj.UID,
		},
		Type:          corev1.EventTypeWarning,
		Reason:        "BackupPending",
		Message:       message,
		LastTimestamp: metav1.NewTime(at),
	})
}

// TestReconcileCNPG_ReasonIsTheNewestWarningOfTheBackup: the reason is the
// Warning last recorded on the Backup itself, whatever order the events are
// listed in, and never one recorded on another object of the namespace.
func TestReconcileCNPG_ReasonIsTheNewestWarningOfTheBackup(t *testing.T) {
	f, b := newPendingBoundFixture(t, "pending", nil)
	f.recordEvent("a-old", b, "old reason", time.Now().Add(-time.Hour))
	f.recordEvent("m-new", b, "new reason", time.Now().Add(-2*time.Minute))
	unrelated := newPendingBoundCNPGBackup("other-cluster", time.Now(), "pending", nil)
	unrelated.Spec.Cluster.Name = "postgres-other"
	f.create(unrelated)
	f.recordEvent("z-unrelated", unrelated, "unrelated reason", time.Now().Add(-time.Minute))

	j, _ := f.pass()
	stalled := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if stalled == nil || !strings.Contains(stalled.Message, "new reason") {
		t.Fatalf("%s condition = %+v, want the newest Warning of the Backup", backupCondCNPGBackupStalled, stalled)
	}
	for _, unwanted := range []string{"old reason", "unrelated reason"} {
		if strings.Contains(stalled.Message, unwanted) {
			t.Errorf("%s message %q carries %q", backupCondCNPGBackupStalled, stalled.Message, unwanted)
		}
	}
}

// TestReconcileCNPG_FinishedBackupEndsTheStall: a Backup can go from pending
// straight to a terminal phase between two passes. The finished job then
// reports the stall as over rather than a Backup CloudNativePG has not
// started.
func TestReconcileCNPG_FinishedBackupEndsTheStall(t *testing.T) {
	for _, tc := range []struct {
		phase     string
		wantPhase backupsv1alpha1.BackupJobPhase
	}{
		{phase: cnpgBackupPhaseComplete, wantPhase: backupsv1alpha1.BackupJobPhaseSucceeded},
		{phase: cnpgBackupPhaseFailed, wantPhase: backupsv1alpha1.BackupJobPhaseFailed},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			since := time.Now().Add(-time.Minute)
			f, _ := newPendingBoundFixture(t, tc.phase, &since)

			j, _ := f.pass()
			if j.Status.Phase != tc.wantPhase {
				t.Fatalf("BackupJob phase = %q (%s), want %q", j.Status.Phase, j.Status.Message, tc.wantPhase)
			}
			if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
				t.Fatalf("%s condition = %+v, want False once the Backup finished", backupCondCNPGBackupStalled, cond)
			}
		})
	}
}

// TestReconcileCNPG_QueueReadErrorIsRetried: unlike the reason, which the job
// can do without, the queue decides whether the wait is healthy. A failed read
// of it is retried; read as empty it would make a Backup queued behind a
// running one look stalled, and fail the job and delete its Backup.
func TestReconcileCNPG_QueueReadErrorIsRetried(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.create(newPendingBoundCNPGBackup("scheduled", time.Now().Add(-time.Minute), "running", nil))
	readErr := errors.New("cache not synced")
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lo := &client.ListOptions{}
			lo.ApplyOptions(opts)
			if _, ok := list.(*cnpgtypes.BackupList); ok && lo.LabelSelector == nil {
				return readErr
			}
			return cl.List(ctx, list, opts...)
		},
	})

	ctx := context.Background()
	j := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if _, err := f.r.reconcileCNPG(ctx, j, f.resolved); !errors.Is(err, readErr) {
		t.Fatalf("reconcileCNPG error = %v, want the queue read error", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, persisted); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob failed on a queue read error: %s", persisted.Status.Message)
	}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj-cnpg"}, &cnpgtypes.Backup{}); err != nil {
		t.Fatalf("the queued Backup was deleted on a queue read error: %v", err)
	}
}

// TestReconcileCNPG_ReasonIsReadUncached: the events are read through the
// uncached reader. The cached client would start a cluster-wide informer on
// events, which the controller has no watch grant for and no UID index in.
func TestReconcileCNPG_ReasonIsReadUncached(t *testing.T) {
	f, b := newPendingBoundFixture(t, "pending", nil)
	f.recordCNPGEvent(b, corev1.EventTypeWarning, "BackupPending", pendingTargetEvent, time.Now().Add(-time.Minute))
	f.r.APIReader = f.c
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.EventList); ok {
				return errors.New("events read through the cache")
			}
			return cl.List(ctx, list, opts...)
		},
	})

	j, _ := f.pass()
	stalled := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if stalled == nil || !strings.Contains(stalled.Message, pendingTargetEvent) {
		t.Fatalf("%s condition = %+v, want the reason read through the uncached reader", backupCondCNPGBackupStalled, stalled)
	}
}

// TestReconcileCNPG_GiveUpRechecksTheQueueUncached: a backup that started is
// seen as such before the job gives up, even while the cache still shows it
// pending, so the job does not delete a Backup queued behind it.
func TestReconcileCNPG_GiveUpRechecksTheQueueUncached(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.create(newPendingBoundCNPGBackup("scheduled", time.Now().Add(-time.Minute), "started", nil))
	f.r.APIReader = f.c
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := cl.List(ctx, list, opts...); err != nil {
				return err
			}
			if backups, ok := list.(*cnpgtypes.BackupList); ok {
				for i := range backups.Items {
					backups.Items[i].Status.Phase = "pending"
				}
			}
			return nil
		},
	})

	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Fatalf("BackupJob phase = %q (%s), want Running behind the started backup", j.Status.Phase, j.Status.Message)
	}
	if len(backups) != 2 {
		t.Fatalf("cnpg.io/Backups = %v, want both kept", cnpgBackupNames(backups))
	}
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want the stall ended by the recheck, so a later delete by hand is not taken for a give-up", backupCondCNPGBackupStalled, cond)
	}

	f.r.Client = f.c
	j, _ = f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want False once the cache shows the started backup", backupCondCNPGBackupStalled, cond)
	}
}

// TestReconcileCNPG_QueueIsReadFromTheCache: passes that do not give up read
// the queue from the cache, so a job polling behind a long backup does not
// list every cnpg.io/Backup of the namespace from the apiserver.
func TestReconcileCNPG_QueueIsReadFromTheCache(t *testing.T) {
	f, _ := newPendingBoundFixture(t, "pending", nil)
	f.create(newPendingBoundCNPGBackup("scheduled", time.Now().Add(-time.Minute), "running", nil))
	f.r.APIReader = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lo := &client.ListOptions{}
			lo.ApplyOptions(opts)
			if _, ok := list.(*cnpgtypes.BackupList); ok && lo.LabelSelector == nil {
				return errors.New("queue read uncached")
			}
			return cl.List(ctx, list, opts...)
		},
	})

	j, _ := f.pass()
	if ready := apimeta.FindStatusCondition(j.Status.Conditions, "Ready"); ready == nil || !strings.Contains(ready.Message, "queued behind") {
		t.Fatalf("Ready condition = %+v, want the job queued behind the running backup", ready)
	}
}

// TestReconcileCNPG_StalledBackupDeletedByHandCountsAfresh: a stalled Backup
// deleted outside the driver is replaced, and the replacement's stall counts
// from its own start rather than failing the job on the time of the old one.
func TestReconcileCNPG_StalledBackupDeletedByHandCountsAfresh(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, b := newPendingBoundFixture(t, "pending", &since)
	if err := f.c.Delete(context.Background(), b); err != nil {
		t.Fatalf("delete cnpg.io/Backup: %v", err)
	}

	j, backups := f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning {
		t.Fatalf("BackupJob phase = %q (%s), want Running with a new Backup", j.Status.Phase, j.Status.Message)
	}
	if len(backups) != 1 {
		t.Fatalf("cnpg.io/Backups = %v, want a new one", cnpgBackupNames(backups))
	}
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want False for the deleted Backup's stall", backupCondCNPGBackupStalled, cond)
	}

	f.setCNPGPhase(backups[0].Name, "pending")
	j, backups = f.pass()
	if j.Status.Phase != backupsv1alpha1.BackupJobPhaseRunning || len(backups) != 1 {
		t.Fatalf("BackupJob phase = %q (%s), cnpg.io/Backups = %v, want Running on the new Backup's fresh stall", j.Status.Phase, j.Status.Message, cnpgBackupNames(backups))
	}
	cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled)
	if cond == nil || cond.Status != metav1.ConditionTrue || time.Since(cond.LastTransitionTime.Time) > time.Minute {
		t.Fatalf("%s condition = %+v, want True from now", backupCondCNPGBackupStalled, cond)
	}
}

// TestReconcileCNPG_GivingUpIsRecordedBeforeTheDelete: the job records that it
// gives up before it deletes its Backup, so a pass that finds the Backup gone
// fails the job rather than mistaking the delete for one made by hand.
func TestReconcileCNPG_GivingUpIsRecordedBeforeTheDelete(t *testing.T) {
	since := time.Now().Add(-cnpgDefaultBackupDeadline - time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	deleteErr := errors.New("apiserver unavailable")
	f.r.Client = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*cnpgtypes.Backup); ok {
				return deleteErr
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})

	ctx := context.Background()
	j := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, j); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if _, err := f.r.reconcileCNPG(ctx, j, f.resolved); !errors.Is(err, deleteErr) {
		t.Fatalf("reconcileCNPG error = %v, want the delete error", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: rolloutNS, Name: "bj"}, persisted); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	cond := apimeta.FindStatusCondition(persisted.Status.Conditions, backupCondCNPGBackupStalled)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != cnpgStallAbandoningReason {
		t.Fatalf("persisted %s condition = %+v, want True with reason %s", backupCondCNPGBackupStalled, cond, cnpgStallAbandoningReason)
	}
}

// TestReconcileCNPG_EndedStallIsPersistedAlone: the end of a stall is written
// even when the Ready condition it comes with is unchanged.
func TestReconcileCNPG_EndedStallIsPersistedAlone(t *testing.T) {
	since := time.Now().Add(-time.Minute)
	f, _ := newPendingBoundFixture(t, "pending", &since)
	f.create(newPendingBoundCNPGBackup("scheduled", time.Now().Add(-time.Minute), "running", nil))

	j, _ := f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want False while queued", backupCondCNPGBackupStalled, cond)
	}
	apimeta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{
		Type:    backupCondCNPGBackupStalled,
		Status:  metav1.ConditionTrue,
		Reason:  "NotStarted",
		Message: "stalled",
	})
	if err := f.c.Status().Update(context.Background(), j); err != nil {
		t.Fatalf("seed a True stall: %v", err)
	}

	j, _ = f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("%s condition = %+v, want False persisted although Ready did not change", backupCondCNPGBackupStalled, cond)
	}
}

// newAttachedByAnotherJobFixture is a Cluster another backup job attached the
// plugin to ten minutes ago, with one instance pod created at podCreated that
// is not ready.
func newAttachedByAnotherJobFixture(t *testing.T, fenced string, podCreated time.Time) *cnpgRolloutFixture {
	t.Helper()
	j := newRolloutBackupJob(time.Now())
	j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	cluster := newRolloutCluster("Cluster is fenced", buildBarmanPlugin(rolloutCluster, rolloutCluster))
	cluster.Annotations = map[string]string{
		cnpgPluginAttachedAtAnnotation: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
	}
	if fenced != "" {
		cluster.Annotations[cnpgFencedInstancesAnnotation] = fenced
	}
	if fenced != `["*"]` {
		cluster.Status.Phase = "Waiting for the instances to become active"
	}
	pod := newRolloutInstancePod(podCreated)
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	return newCNPGRolloutFixture(t, j, cluster, pod)
}

// TestReconcileCNPG_FencedClusterIsNotHeldByAnotherJobsRollout: a fenced
// Cluster stays unhealthy until the fence is lifted, so a job is not held on
// a rollout another job started once every instance pod postdates it. Its
// Backup is created and stalls, which starts the bound.
func TestReconcileCNPG_FencedClusterIsNotHeldByAnotherJobsRollout(t *testing.T) {
	f := newAttachedByAnotherJobFixture(t, `["*"]`, time.Now().Add(-5*time.Minute))

	j, backups := f.pass()
	if len(backups) != 1 {
		t.Fatalf("cnpg.io/Backups = %v, want the job's Backup created on the fenced Cluster (Ready: %+v)",
			cnpgBackupNames(backups), apimeta.FindStatusCondition(j.Status.Conditions, "Ready"))
	}
	f.setCNPGPhase(backups[0].Name, "pending")
	j, _ = f.pass()
	if cond := apimeta.FindStatusCondition(j.Status.Conditions, backupCondCNPGBackupStalled); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("%s condition = %+v, want True", backupCondCNPGBackupStalled, cond)
	}
}

// TestReconcileCNPG_RolloutGateStillHoldsWhenItMust: an instance pod older
// than another job's attach means that rollout is unfinished, fenced or not,
// and an unfenced Cluster whose pods are not ready is still waited on.
func TestReconcileCNPG_RolloutGateStillHoldsWhenItMust(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fenced     string
		podCreated time.Time
	}{
		{name: "fenced, pod predates the attach", fenced: `["*"]`, podCreated: time.Now().Add(-20 * time.Minute)},
		{name: "not fenced, pod not ready", podCreated: time.Now().Add(-5 * time.Minute)},
		{name: "fence lifted to an empty list, pod not ready", fenced: `[]`, podCreated: time.Now().Add(-5 * time.Minute)},
		{name: "fence lifted to an empty list spelled with a space, pod not ready", fenced: `[ ]`, podCreated: time.Now().Add(-5 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttachedByAnotherJobFixture(t, tc.fenced, tc.podCreated)

			j, backups := f.pass()
			if len(backups) != 0 {
				t.Fatalf("cnpg.io/Backups = %v, want none while the rollout is waited on", cnpgBackupNames(backups))
			}
			if ready := apimeta.FindStatusCondition(j.Status.Conditions, "Ready"); ready == nil || ready.Reason != "WaitingForPluginRollout" {
				t.Fatalf("Ready condition = %+v, want WaitingForPluginRollout", ready)
			}
		})
	}
}
