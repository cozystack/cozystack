// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const lockNS = "tenant-acme"

func lockTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}).
		Build()
}

func pgRef(name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: "Postgres", Name: name}
}

func lockBackup(name string, app corev1.TypedLocalObjectReference) *backupsv1alpha1.Backup {
	return lockBackupBy(name, app, strategyv1alpha1.CNPGStrategyKind)
}

func lockBackupBy(name string, app corev1.TypedLocalObjectReference, strategyKind string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: lockNS, Name: name},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: app,
			StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: new(strategyv1alpha1.GroupVersion.Group), Kind: strategyKind, Name: "s"},
		},
	}
}

func lockRestore(name, backup string, phase backupsv1alpha1.RestoreJobPhase, target *corev1.TypedLocalObjectReference, options string) *backupsv1alpha1.RestoreJob {
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: lockNS, Name: name},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef:            corev1.LocalObjectReference{Name: backup},
			TargetApplicationRef: target,
		},
		Status: backupsv1alpha1.RestoreJobStatus{Phase: phase},
	}
	if options != "" {
		rj.Spec.Options = &runtime.RawExtension{Raw: []byte(options)}
	}
	return rj
}

func TestActiveRestoreTargeting(t *testing.T) {
	other := pgRef("other")
	anotherKind := corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: "MariaDB", Name: "pg"}
	anotherGroup := corev1.TypedLocalObjectReference{APIGroup: new("example.com"), Kind: "Postgres", Name: "pg"}
	vm := corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: "VMInstance", Name: "vm"}
	vmInGroup := corev1.TypedLocalObjectReference{APIGroup: new("example.com"), Kind: "VMInstance", Name: "vm"}
	pg2InGroup := corev1.TypedLocalObjectReference{APIGroup: new("example.com"), Kind: "Postgres", Name: "pg2"}
	pg2Default := pgRef("pg2")
	for _, tc := range []struct {
		name    string
		app     *corev1.TypedLocalObjectReference // pgRef("pg") when nil
		objects []client.Object
		want    string
	}{
		{"no restore", nil, nil, ""},
		{"in-place restore of the same application", nil, []client.Object{
			lockBackup("pg-1", pgRef("pg")), lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, ""),
		}, "r1"},
		{"restore not picked up yet", nil, []client.Object{
			lockBackup("pg-1", pgRef("pg")), lockRestore("r1", "pg-1", "", nil, ""),
		}, "r1"},
		{"to-copy restore into this application", nil, []client.Object{
			lockBackup("other-1", other), lockRestore("r1", "other-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "pg"}, ""),
		}, "r1"},
		{"to-copy restore out of this application", nil, []client.Object{
			lockBackup("pg-1", pgRef("pg")), lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, &other, ""),
		}, ""},
		{"finished restores", nil, []client.Object{
			lockBackup("pg-1", pgRef("pg")),
			lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseSucceeded, nil, ""),
			lockRestore("r2", "pg-1", backupsv1alpha1.RestoreJobPhaseFailed, nil, ""),
		}, ""},
		{"same name, another kind", nil, []client.Object{
			lockBackup("m-1", anotherKind), lockRestore("r1", "m-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, ""),
		}, ""},
		{"same kind and name, another API group", nil, []client.Object{
			lockBackup("x-1", anotherGroup), lockRestore("r1", "x-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, ""),
		}, ""},
		// A target without an API group stays in the Backup's group.
		{"to-copy target inheriting the Backup's API group", &pg2InGroup, []client.Object{
			lockBackup("x-1", anotherGroup), lockRestore("r1", "x-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "pg2"}, ""),
		}, "r1"},
		{"to-copy target without an API group is not in the default one", &pg2Default, []client.Object{
			lockBackup("x-1", anotherGroup), lockRestore("r1", "x-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "pg2"}, ""),
		}, ""},
		// Only the Velero driver reads targetNamespace.
		{"Velero restore sent to another namespace", &vm, []client.Object{
			lockBackupBy("vm-1", vm, strategyv1alpha1.VeleroStrategyKind), lockRestore("r1", "vm-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, `{"targetNamespace":"tenant-other"}`),
		}, ""},
		{"Velero restore naming its own namespace", &vm, []client.Object{
			lockBackupBy("vm-1", vm, strategyv1alpha1.VeleroStrategyKind), lockRestore("r1", "vm-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, `{"targetNamespace":"`+lockNS+`"}`),
		}, "r1"},
		// The Velero driver drops the target's API group and restores the
		// Backup's resources, in the Backup's group.
		{"Velero target naming another API group restores in the Backup's", &vm, []client.Object{
			lockBackupBy("vm-1", vm, strategyv1alpha1.VeleroStrategyKind), lockRestore("r1", "vm-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{APIGroup: new("example.com"), Kind: "VMInstance", Name: "vm"}, ""),
		}, "r1"},
		{"Velero target naming another API group does not hold that group", &vmInGroup, []client.Object{
			lockBackupBy("vm-1", vm, strategyv1alpha1.VeleroStrategyKind), lockRestore("r1", "vm-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{APIGroup: new("example.com"), Kind: "VMInstance", Name: "vm"}, ""),
		}, ""},
		{"Postgres restore carrying targetNamespace, which CNPG ignores", nil, []client.Object{
			lockBackup("pg-1", pgRef("pg")), lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, `{"targetNamespace":"tenant-other"}`),
		}, "r1"},
		{"backup gone", nil, []client.Object{
			lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, ""),
		}, ""},
		// The restore controller fails it on its next pass for want of its
		// Backup, however complete its target.
		{"backup gone, target complete", nil, []client.Object{
			lockRestore("r1", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, &corev1.TypedLocalObjectReference{Kind: "Postgres", Name: "pg"}, ""),
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lockTestClient(t, tc.objects...)
			app := pgRef("pg")
			if tc.app != nil {
				app = *tc.app
			}
			got, err := activeRestoreTargeting(context.Background(), c, lockNS, app)
			if err != nil {
				t.Fatalf("activeRestoreTargeting: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func lockBackupClass() *backupsv1alpha1.BackupClass {
	return &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default"},
		Spec: backupsv1alpha1.BackupClassSpec{
			Strategies: []backupsv1alpha1.BackupClassStrategy{{
				Application: backupsv1alpha1.ApplicationSelector{APIGroup: new("apps.cozystack.io"), Kind: "Postgres"},
				StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: new(strategyv1alpha1.GroupVersion.Group), Kind: strategyv1alpha1.CNPGStrategyKind, Name: "cozy-default-cnpg",
				},
			}},
		},
	}
}

func lockBackupJob(started bool) *backupsv1alpha1.BackupJob {
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: lockNS, Name: "nightly-29842560"},
		Spec:       backupsv1alpha1.BackupJobSpec{BackupClassName: "cozy-default", ApplicationRef: pgRef("pg")},
	}
	if started {
		now := metav1.Now()
		j.Status.StartedAt = &now
		j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	}
	return j
}

// TestReconcile_HoldsABackupJobWhileItsApplicationIsRestored is the
// regression: a Plan-created BackupJob for a Postgres application under an
// in-place restore reached the CNPG driver, which re-applied the Cluster's
// barman plugin and put the restored cluster back on the source's WAL
// prefix. The job must wait, non-terminal, without reaching projection or
// dispatch; and once the restore is over, it must go ahead.
func TestReconcile_HoldsABackupJobWhileItsApplicationIsRestored(t *testing.T) {
	restore := lockRestore("restore-pg", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, "")
	c := lockTestClient(t, lockBackupJob(false), lockBackupClass(), lockBackup("pg-1", pgRef("pg")), restore, flatSourceSecret())
	r := &BackupJobReconciler{Client: c, CredentialsConfig: defaultCfg()}
	key := types.NamespacedName{Namespace: lockNS, Name: "nightly-29842560"}

	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != RestoreInProgressRequeue {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, RestoreInProgressRequeue)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != backupsv1alpha1.BackupJobPhasePending {
		t.Errorf("Phase = %q, want Pending", got.Status.Phase)
	}
	if got.Status.StartedAt != nil {
		t.Error("a held BackupJob must not be marked started")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Reason != ConditionReasonRestoreInProgress {
		t.Fatalf("Ready condition = %+v, want reason %s", cond, ConditionReasonRestoreInProgress)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: lockNS, Name: "cozy-backups-creds"}, secret); err == nil {
		t.Error("a held BackupJob went on to project credentials")
	}

	// The restore ends: the job is no longer held.
	fresh := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: lockNS, Name: "restore-pg"}, fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Status.Phase = backupsv1alpha1.RestoreJobPhaseSucceeded
	if err := c.Status().Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if held, err := activeRestoreTargeting(context.Background(), c, lockNS, pgRef("pg")); err != nil || held != "" {
		t.Errorf("after the restore ended: held by %q (%v), want none", held, err)
	}

	// The next reconcile starts the job without the hold's condition.
	_, _ = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	released := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), key, released); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(released.Status.Conditions, "Ready"); cond != nil && cond.Reason == ConditionReasonRestoreInProgress {
		t.Errorf("Ready = %+v: a job the restore released still says it is waiting for it", cond)
	}
}

// TestReconcile_DoesNotHoldARunningBackupJob: the gate is on starting. A run
// already moving data is left to its driver, whose own state it would
// otherwise strand.
func TestReconcile_DoesNotHoldARunningBackupJob(t *testing.T) {
	restore := lockRestore("restore-pg", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, "")
	// No source Secret: projection fails transiently, which stops the
	// reconcile before dispatch and shows the gate was passed.
	c := lockTestClient(t, lockBackupJob(true), lockBackupClass(), lockBackup("pg-1", pgRef("pg")), restore)
	r := &BackupJobReconciler{Client: c, CredentialsConfig: defaultCfg()}
	key := types.NamespacedName{Namespace: lockNS, Name: "nightly-29842560"}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "Ready"); cond != nil && cond.Reason == ConditionReasonRestoreInProgress {
		t.Error("a BackupJob already started was held")
	}
}

// TestReconcile_RetriesAHoldItCouldNotRecord: a hold whose status write fails
// is returned as an error, so the manager retries it, rather than requeued as
// though it had been recorded.
func TestReconcile_RetriesAHoldItCouldNotRecord(t *testing.T) {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	writeFailed := errors.New("apiserver unavailable")
	c := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(lockBackupJob(false), lockBackupClass(), lockBackup("pg-1", pgRef("pg")),
			lockRestore("restore-pg", "pg-1", backupsv1alpha1.RestoreJobPhaseRunning, nil, ""), flatSourceSecret()).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*backupsv1alpha1.BackupJob); ok {
					return writeFailed
				}
				return cl.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &BackupJobReconciler{Client: c, CredentialsConfig: defaultCfg()}

	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: lockNS, Name: "nightly-29842560"}})
	if !errors.Is(err, writeFailed) {
		t.Fatalf("Reconcile error = %v, want the failed status write", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v: a failed hold must be retried through the error, not requeued as recorded", res.RequeueAfter)
	}
}

// TestReleaseRestoreHold: once the restore has ended, the job starts without
// the "waiting for RestoreJob" condition, which the drivers would otherwise
// leave in place for the whole run. Any other Ready condition is not ours to
// remove.
func TestReleaseRestoreHold(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reason   string
		wantKept bool
	}{
		{"held by a restore", ConditionReasonRestoreInProgress, false},
		{"another reason", "CredentialsNotReady", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := lockBackupJob(false)
			j.Status.Phase = backupsv1alpha1.BackupJobPhasePending
			meta.SetStatusCondition(&j.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: tc.reason, Message: "x"})
			c := lockTestClient(t, j)
			r := &BackupJobReconciler{Client: c}
			live := &backupsv1alpha1.BackupJob{}
			key := types.NamespacedName{Namespace: lockNS, Name: j.Name}
			if err := c.Get(context.Background(), key, live); err != nil {
				t.Fatal(err)
			}
			if err := r.releaseRestoreHold(context.Background(), live); err != nil {
				t.Fatalf("releaseRestoreHold: %v", err)
			}
			got := &backupsv1alpha1.BackupJob{}
			if err := c.Get(context.Background(), key, got); err != nil {
				t.Fatal(err)
			}
			if kept := meta.FindStatusCondition(got.Status.Conditions, "Ready") != nil; kept != tc.wantKept {
				t.Errorf("Ready condition kept = %v, want %v", kept, tc.wantKept)
			}
		})
	}
}
