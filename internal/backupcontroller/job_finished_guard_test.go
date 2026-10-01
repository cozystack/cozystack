// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// apiServerWith returns a reader standing in for the apiserver, holding job
// at phase while the reconciler's Client, the cache, still holds the job it
// was seeded with.
func apiServerWith(t *testing.T, s *runtime.Scheme, job client.Object, phase string) client.Reader {
	t.Helper()
	onAPI := job.DeepCopyObject().(client.Object)
	switch j := onAPI.(type) {
	case *backupsv1alpha1.BackupJob:
		j.Status.Phase = backupsv1alpha1.BackupJobPhase(phase)
	case *backupsv1alpha1.RestoreJob:
		j.Status.Phase = backupsv1alpha1.RestoreJobPhase(phase)
	}
	onAPI.SetResourceVersion("")
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(onAPI).Build()
}

// sequenceReader answers its n-th Get from readers[n], and every later Get
// from the last one: a job that turns terminal partway through a pass.
type sequenceReader struct {
	readers []client.Reader
	gets    int
}

func (s *sequenceReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r := s.readers[min(s.gets, len(s.readers)-1)]
	s.gets++
	return r.Get(ctx, key, obj, opts...)
}

func (s *sequenceReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return s.readers[len(s.readers)-1].List(ctx, list, opts...)
}

func countObjects(t *testing.T, c client.Client, list client.ObjectList) int {
	t.Helper()
	if err := c.List(context.Background(), list); err != nil {
		t.Fatalf("list %T: %v", list, err)
	}
	return meta.LenList(list)
}

func backupJobPhase(t *testing.T, c client.Client, j *backupsv1alpha1.BackupJob) backupsv1alpha1.BackupJobPhase {
	t.Helper()
	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(j), got); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	return got.Status.Phase
}

func restoreJobPhase(t *testing.T, c client.Client, rj *backupsv1alpha1.RestoreJob) backupsv1alpha1.RestoreJobPhase {
	t.Helper()
	got := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), got); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	return got.Status.Phase
}

// TestBackupDriversCreateNothingForAFinishedJob: the informer cache still
// shows the BackupJob Running, the apiserver already holds it Failed. No
// driver may create its backup object, nor overwrite the Failed verdict. Each
// case first runs with the apiserver agreeing with the cache, to prove the
// pass does reach the Create the guard sits in front of.
func TestBackupDriversCreateNothingForAFinishedJob(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	strategyGroup := strategyv1alpha1.GroupVersion.Group

	cases := []struct {
		name string
		run  func(t *testing.T, apiPhase backupsv1alpha1.BackupJobPhase) (created int, cached backupsv1alpha1.BackupJobPhase)
	}{
		{
			name: "velero",
			run: func(t *testing.T, apiPhase backupsv1alpha1.BackupJobPhase) (int, backupsv1alpha1.BackupJobPhase) {
				s := runtime.NewScheme()
				_ = scheme.AddToScheme(s)
				_ = backupsv1alpha1.AddToScheme(s)
				_ = strategyv1alpha1.AddToScheme(s)
				_ = velerov1.AddToScheme(s)
				app := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "tenant"}}
				j := &backupsv1alpha1.BackupJob{
					ObjectMeta: metav1.ObjectMeta{Name: "bj", Namespace: "tenant"},
					Spec: backupsv1alpha1.BackupJobSpec{
						ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new(""), Kind: "Pod", Name: "app"},
					},
					Status: backupsv1alpha1.BackupJobStatus{StartedAt: &started, Phase: backupsv1alpha1.BackupJobPhaseRunning},
				}
				strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "velero"}}
				c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(j, strategy).
					WithStatusSubresource(&backupsv1alpha1.BackupJob{}).Build()
				r := &BackupJobReconciler{
					Client:    c,
					APIReader: apiServerWith(t, s, j, string(apiPhase)),
					Interface: dynamicfake.NewSimpleDynamicClient(s, app),
					RESTMapper: &mockRESTMapper{mapping: &meta.RESTMapping{
						Resource:         schema.GroupVersionResource{Version: "v1", Resource: "pods"},
						GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
						Scope:            meta.RESTScopeNamespace,
					}},
					Scheme:   s,
					Recorder: record.NewFakeRecorder(10),
				}
				resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: &strategyGroup, Kind: strategyv1alpha1.VeleroStrategyKind, Name: "velero",
				}}
				if _, err := r.reconcileVelero(context.Background(), j.DeepCopy(), resolved); err != nil {
					t.Fatalf("reconcileVelero: %v", err)
				}
				return countObjects(t, c, &velerov1.BackupList{}), backupJobPhase(t, c, j)
			},
		},
		{
			name: "cnpg",
			run: func(t *testing.T, apiPhase backupsv1alpha1.BackupJobPhase) (int, backupsv1alpha1.BackupJobPhase) {
				apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
				j := &backupsv1alpha1.BackupJob{
					ObjectMeta: metav1.ObjectMeta{Name: "bj", Namespace: "tenant"},
					Spec: backupsv1alpha1.BackupJobSpec{
						ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "app"},
					},
					Status: backupsv1alpha1.BackupJobStatus{StartedAt: &started, Phase: backupsv1alpha1.BackupJobPhaseRunning},
				}
				strategy := &strategyv1alpha1.CNPG{
					ObjectMeta: metav1.ObjectMeta{Name: "cnpg"},
					Spec: strategyv1alpha1.CNPGSpec{Template: strategyv1alpha1.CNPGTemplate{
						BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://bucket/"},
					}},
				}
				cluster := &cnpgtypes.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: cnpgClusterNameForApp("app"), UID: "cluster-uid"}}
				c := newCNPGStrategyTestClient(t, j, strategy, newPostgresApp("app", "tenant"), cluster)
				r := &BackupJobReconciler{
					Client:    c,
					APIReader: apiServerWith(t, c.Scheme(), j, string(apiPhase)),
					Interface: cnpgDynamicFor(t, cluster),
					Recorder:  record.NewFakeRecorder(10),
				}
				resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "cnpg",
				}}
				if _, err := r.reconcileCNPG(context.Background(), j.DeepCopy(), resolved); err != nil {
					t.Fatalf("reconcileCNPG: %v", err)
				}
				return countObjects(t, c, &cnpgtypes.BackupList{}), backupJobPhase(t, c, j)
			},
		},
		{
			name: "mariadb",
			run: func(t *testing.T, apiPhase backupsv1alpha1.BackupJobPhase) (int, backupsv1alpha1.BackupJobPhase) {
				j := newMariaDBBackupJob("bj", "tenant")
				j.Status.StartedAt = &started
				j.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
				strategy := &strategyv1alpha1.MariaDB{
					ObjectMeta: metav1.ObjectMeta{Name: "mariadb"},
					Spec:       strategyv1alpha1.MariaDBSpec{Template: *newRenderedMariaDBTemplate()},
				}
				operatorMariaDB := &mariadbtypes.MariaDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mariadbNameForApp("mariadb-src")}}
				c := newMariaDBStrategyTestClient(t, j, strategy, newMariaDBApp("mariadb-src", "tenant"), operatorMariaDB)
				r := &BackupJobReconciler{
					Client:    c,
					APIReader: apiServerWith(t, c.Scheme(), j, string(apiPhase)),
					Scheme:    c.Scheme(),
					Recorder:  record.NewFakeRecorder(10),
				}
				resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
					APIGroup: &strategyGroup, Kind: strategyv1alpha1.MariaDBStrategyKind, Name: "mariadb",
				}}
				if _, err := r.reconcileMariaDB(context.Background(), j.DeepCopy(), resolved); err != nil {
					t.Fatalf("reconcileMariaDB: %v", err)
				}
				return countObjects(t, c, &mariadbtypes.BackupList{}), backupJobPhase(t, c, j)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if created, _ := tc.run(t, backupsv1alpha1.BackupJobPhaseRunning); created != 1 {
				t.Fatalf("job Running on the apiserver: %d driver objects created, want 1", created)
			}
			created, cached := tc.run(t, backupsv1alpha1.BackupJobPhaseFailed)
			if created != 0 {
				t.Errorf("job Failed on the apiserver: %d driver objects created, want 0", created)
			}
			if cached != backupsv1alpha1.BackupJobPhaseRunning {
				t.Errorf("the driver wrote phase %q over the job; the actor that failed it owns the verdict", cached)
			}
		})
	}
}

// TestRestoreDriversCreateNothingForAFinishedJob is the restore-side
// counterpart. Beyond the driver object, a restore rewrites the target
// application first — the Velero driver halts the VM and suspends its
// HelmRelease, the CNPG driver deletes the target Cluster and its PVCs — and
// none of that may happen for a job the apiserver holds as Failed.
func TestRestoreDriversCreateNothingForAFinishedJob(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	running := backupsv1alpha1.RestoreJobPhaseRunning
	failed := backupsv1alpha1.RestoreJobPhaseFailed

	t.Run("velero", func(t *testing.T) {
		const ns = "tenant"
		hrName := vmNamePrefix + "vm"
		// apiPhases is the phase the apiserver reports on each successive
		// re-read of the job.
		run := func(t *testing.T, apiPhases ...backupsv1alpha1.RestoreJobPhase) (restores, configMaps int, hrSuspended bool, cached backupsv1alpha1.RestoreJobPhase) {
			s := runtime.NewScheme()
			_ = scheme.AddToScheme(s)
			_ = backupsv1alpha1.AddToScheme(s)
			_ = strategyv1alpha1.AddToScheme(s)
			_ = velerov1.AddToScheme(s)
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: vmInstanceKind, Name: "vm"},
					StrategyRef:    corev1.TypedLocalObjectReference{Kind: strategyv1alpha1.VeleroStrategyKind, Name: "velero"},
					DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "vb"},
				},
			}
			rj := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
				Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &started, Phase: running},
			}
			strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "velero"}}
			c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(rj, backup, strategy).
				WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).Build()
			readers := make([]client.Reader, 0, len(apiPhases))
			for _, p := range apiPhases {
				readers = append(readers, apiServerWith(t, s, rj, string(p)))
			}
			dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t), makeUnstructuredHelmRelease(hrName, ns, nil))
			r := &RestoreJobReconciler{
				Client:    c,
				APIReader: &sequenceReader{readers: readers},
				Interface: dyn,
				Scheme:    s,
				Recorder:  record.NewFakeRecorder(100),
			}
			if _, err := r.reconcileVeleroRestore(context.Background(), rj.DeepCopy(), backup); err != nil {
				t.Fatalf("reconcileVeleroRestore: %v", err)
			}
			hr, err := dyn.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), hrName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get HelmRelease: %v", err)
			}
			hrSuspended, _, _ = unstructured.NestedBool(hr.Object, "spec", "suspend")
			return countObjects(t, c, &velerov1.RestoreList{}), countObjects(t, c, &corev1.ConfigMapList{}), hrSuspended, restoreJobPhase(t, c, rj)
		}

		if restores, _, suspended, _ := run(t, running); restores != 1 || !suspended {
			t.Fatalf("job Running on the apiserver: %d Velero Restores, HelmRelease suspended=%v; want 1 and true", restores, suspended)
		}

		t.Run("failed before preparation", func(t *testing.T) {
			restores, configMaps, suspended, cached := run(t, failed)
			if suspended {
				t.Error("HelmRelease suspended: prepareForRestore ran for a Failed job")
			}
			if restores != 0 || configMaps != 0 {
				t.Errorf("%d Velero Restores and %d resource-modifier ConfigMaps created, want none", restores, configMaps)
			}
			if cached != running {
				t.Errorf("the driver wrote phase %q over the job", cached)
			}
		})

		t.Run("failed during preparation", func(t *testing.T) {
			restores, configMaps, suspended, cached := run(t, running, failed)
			if !suspended {
				t.Fatal("HelmRelease not suspended: the first re-read should have let prepareForRestore run")
			}
			if restores != 0 || configMaps != 0 {
				t.Errorf("%d Velero Restores and %d resource-modifier ConfigMaps created, want none", restores, configMaps)
			}
			if cached != running {
				t.Errorf("the driver wrote phase %q over the job", cached)
			}
		})
	})

	t.Run("cnpg", func(t *testing.T) {
		const (
			ns          = "tenant"
			clusterName = "postgres-app"
		)
		apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
		strategyGroup := strategyv1alpha1.GroupVersion.Group
		// Purges the Cluster unless stopped: a recovery Cluster left over from
		// before StartedAt, behind a WAL gate the completed cnpg.io/Backup clears.
		run := func(t *testing.T, apiPhase backupsv1alpha1.RestoreJobPhase) (clusterKept, pvcKept bool, cached backupsv1alpha1.RestoreJobPhase) {
			snap, err := marshalCNPGBackupSnapshot(newPostgresApp("app", ns), nil)
			if err != nil {
				t.Fatalf("marshal snapshot: %v", err)
			}
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "app"},
					StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "cnpg"},
					DriverMetadata: map[string]string{
						cnpgServerNameKey:      "app",
						cnpgDestinationPathKey: "s3://bucket/app/",
						cnpgBackupNameKey:      "cnpgbk",
					},
				},
				Status: backupsv1alpha1.BackupStatus{UnderlyingResources: snap},
			}
			strategy := &strategyv1alpha1.CNPG{
				ObjectMeta: metav1.ObjectMeta{Name: "cnpg"},
				Spec: strategyv1alpha1.CNPGSpec{Template: strategyv1alpha1.CNPGTemplate{
					BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://bucket/"},
				}},
			}
			cnpgBackup := &cnpgtypes.Backup{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "cnpgbk"},
				Spec:       cnpgtypes.BackupSpec{Cluster: cnpgtypes.ClusterReference{Name: clusterName}},
				Status:     cnpgtypes.BackupStatus{Phase: cnpgBackupPhaseComplete, EndWal: "000000010000000000000003"},
			}
			cluster := &cnpgtypes.Cluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: clusterName, CreationTimestamp: metav1.NewTime(started.Add(-time.Hour))},
				Spec: cnpgtypes.ClusterSpec{Bootstrap: &cnpgtypes.BootstrapConfiguration{
					Recovery: &cnpgtypes.RecoverySource{Source: "app"},
				}},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: clusterName + "-1", Labels: map[string]string{cnpgClusterLabel: clusterName}},
			}
			rj := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
				Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &started, Phase: running},
			}
			c := newCNPGStrategyTestClient(t, backup, rj, strategy, cnpgBackup, newPostgresApp("app", ns), cluster, pvc)
			r := &RestoreJobReconciler{
				Client:    c,
				APIReader: apiServerWith(t, c.Scheme(), rj, string(apiPhase)),
				Interface: dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t)),
				Recorder:  record.NewFakeRecorder(10),
			}
			seeded := &backupsv1alpha1.RestoreJob{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(rj), seeded); err != nil {
				t.Fatalf("get seeded RestoreJob: %v", err)
			}
			if _, err := r.reconcileCNPGRestore(context.Background(), seeded, backup); err != nil {
				t.Fatalf("reconcileCNPGRestore: %v", err)
			}
			clusterErr := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &cnpgtypes.Cluster{})
			pvcErr := c.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})
			return !apierrors.IsNotFound(clusterErr), !apierrors.IsNotFound(pvcErr), restoreJobPhase(t, c, rj)
		}

		if clusterKept, _, _ := run(t, running); clusterKept {
			t.Fatal("job Running on the apiserver: the target Cluster was not purged, so the pass never reaches the guard")
		}
		clusterKept, pvcKept, cached := run(t, failed)
		if !clusterKept || !pvcKept {
			t.Errorf("job Failed on the apiserver: Cluster kept=%v, PVC kept=%v; the target must be left alone", clusterKept, pvcKept)
		}
		if cached != running {
			t.Errorf("the driver wrote phase %q over the job", cached)
		}
	})

	t.Run("mariadb", func(t *testing.T) {
		apps := mariadbapp.GroupName
		run := func(t *testing.T, apiPhase backupsv1alpha1.RestoreJobPhase) (int, backupsv1alpha1.RestoreJobPhase) {
			srcOperatorBackup := &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-op-bk"}}
			cozyBackup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "src-cozy-bk"},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
					DriverMetadata: map[string]string{mariadbBackupNameKey: "src-op-bk"},
				},
			}
			rj := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: cozyBackup.Name}},
				Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &started, Phase: running},
			}
			targetMariaDB := &mariadbtypes.MariaDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mariadbNameForApp("src")}}
			c := newMariaDBStrategyTestClient(t, srcOperatorBackup, cozyBackup, rj, targetMariaDB)
			r := &RestoreJobReconciler{
				Client:    c,
				APIReader: apiServerWith(t, c.Scheme(), rj, string(apiPhase)),
				Recorder:  record.NewFakeRecorder(10),
			}
			if _, err := r.reconcileMariaDBRestore(context.Background(), rj.DeepCopy(), cozyBackup); err != nil {
				t.Fatalf("reconcileMariaDBRestore: %v", err)
			}
			return countObjects(t, c, &mariadbtypes.RestoreList{}), restoreJobPhase(t, c, rj)
		}

		if created, _ := run(t, running); created != 1 {
			t.Fatalf("job Running on the apiserver: %d k8s.mariadb.com Restores created, want 1", created)
		}
		created, cached := run(t, failed)
		if created != 0 {
			t.Errorf("job Failed on the apiserver: %d k8s.mariadb.com Restores created, want 0", created)
		}
		if cached != running {
			t.Errorf("the driver wrote phase %q over the job", cached)
		}
	})
}

func TestEnsureJobNotFinished(t *testing.T) {
	s := runtime.NewScheme()
	_ = backupsv1alpha1.AddToScheme(s)
	job := &backupsv1alpha1.BackupJob{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"}}
	deleting := job.DeepCopy()
	deleting.Finalizers = []string{"test/hold"}
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	unreachable := interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return apierrors.NewServiceUnavailable("apiserver down")
	}}

	cases := []struct {
		name   string
		reader client.Reader
		want   error
	}{
		{"running", apiServerWith(t, s, job, string(backupsv1alpha1.BackupJobPhaseRunning)), nil},
		{"succeeded", apiServerWith(t, s, job, string(backupsv1alpha1.BackupJobPhaseSucceeded)), errJobFinished},
		{"failed", apiServerWith(t, s, job, string(backupsv1alpha1.BackupJobPhaseFailed)), errJobFinished},
		{"deleted", clientfake.NewClientBuilder().WithScheme(s).Build(), errJobFinished},
		{"being deleted", clientfake.NewClientBuilder().WithScheme(s).WithObjects(deleting).Build(), errJobFinished},
		{"unreadable", clientfake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(unreachable).Build(), errJobRecheck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureJobNotFinished(context.Background(), tc.reader, job)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			stopped, _, retErr := stoppedByJobGuard(err)
			if !stopped {
				t.Fatal("stoppedByJobGuard does not stop the pass")
			}
			if (retErr != nil) != (tc.want == errJobRecheck) {
				t.Errorf("stoppedByJobGuard returned %v: only an unreadable job is retried", retErr)
			}
		})
	}
}
