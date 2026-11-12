// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	"github.com/cozystack/cozystack/internal/backupcontroller/postgresapp"
)

// archiveStoreA is the store a backup is written to; archiveStoreB is the
// same strategy after its credentials Secret, key names and CA were moved.
func archiveStoreA() strategyv1alpha1.BarmanObjectStoreTemplate {
	return strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://bkt-a/tenant/pg/",
		EndpointURL:     "https://s3-a.example:8443",
		S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
			SecretRef:          corev1.LocalObjectReference{Name: "creds-a"},
			AccessKeyIDKey:     "id-a",
			SecretAccessKeyKey: "key-a",
		},
		EndpointCA: &strategyv1alpha1.EndpointCARef{SecretRef: corev1.LocalObjectReference{Name: "ca-a"}, Key: "ca-a.crt"},
	}
}

func archiveStoreB() strategyv1alpha1.BarmanObjectStoreTemplate {
	return strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://bkt-b/tenant/pg/",
		EndpointURL:     "https://s3-b.example:8443",
		S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
			SecretRef:          corev1.LocalObjectReference{Name: "creds-b"},
			AccessKeyIDKey:     "id-b",
			SecretAccessKeyKey: "key-b",
		},
		EndpointCA: &strategyv1alpha1.EndpointCARef{SecretRef: corev1.LocalObjectReference{Name: "ca-b"}, Key: "ca-b.crt"},
	}
}

// artifactDriverMetadata takes a backup written with store through the
// path a real one follows: the record annotated on the cnpg.io/Backup at
// creation, read back when it completes, and copied into the artifact.
func artifactDriverMetadata(t *testing.T, store strategyv1alpha1.BarmanObjectStoreTemplate) map[string]string {
	t.Helper()
	written := cnpgDestinationOf(&strategyv1alpha1.CNPGTemplate{BarmanObjectStore: store})
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	cnpgBk := &cnpgtypes.Backup{ObjectMeta: metav1.ObjectMeta{
		Name:        "cnpg-bk",
		Annotations: map[string]string{cnpgWrittenDestinationAnnotation: string(raw)},
	}}
	moved := &strategyv1alpha1.CNPGTemplate{BarmanObjectStore: archiveStoreB()}
	destination, err := cnpgArtifactDestination(cnpgBk, moved)
	if err != nil {
		t.Fatalf("cnpgArtifactDestination: %v", err)
	}
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
		},
	}
	r := &BackupJobReconciler{Client: newCNPGStrategyTestClient(t)}
	artifact, err := r.createCNPGBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, cnpgBk, "postgres-pg", "srv", "postgresql", destination, moved, newPostgresApp("pg", "tenant"))
	if err != nil {
		t.Fatalf("createCNPGBackupArtifact: %v", err)
	}
	return artifact.Spec.DriverMetadata
}

// A restore reaches the archive with the access settings the backup was
// written with, whatever the strategy names by the time of the restore.
func TestCNPGRestoreArchiveAccess_RecordedAccessWins(t *testing.T) {
	a := archiveStoreA()
	b := archiveStoreB()
	creds, ca, err := cnpgRestoreArchiveAccess(artifactDriverMetadata(t, a), &b)
	if err != nil {
		t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
	}
	if !reflect.DeepEqual(creds, a.S3Credentials) {
		t.Errorf("credentials = %+v, want the recorded %+v", creds, a.S3Credentials)
	}
	if !reflect.DeepEqual(ca, a.EndpointCA) {
		t.Errorf("endpoint CA = %+v, want the recorded %+v", ca, a.EndpointCA)
	}
}

// A backup written without credentials or a CA is restored without them,
// not with the ones the strategy gained since: they belong to another
// location.
func TestCNPGRestoreArchiveAccess_RecordedAbsenceWins(t *testing.T) {
	a := archiveStoreA()
	a.S3Credentials = nil
	a.EndpointCA = nil
	b := archiveStoreB()
	creds, ca, err := cnpgRestoreArchiveAccess(artifactDriverMetadata(t, a), &b)
	if err != nil {
		t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
	}
	if creds != nil || ca != nil {
		t.Errorf("credentials = %+v, endpoint CA = %+v, want neither: the backup was written without them", creds, ca)
	}
}

// Default key names are recorded as the strategy rendered them, empty, so
// the chart keeps applying its defaults rather than the strategy's new ones.
func TestCNPGRestoreArchiveAccess_RecordedDefaultKeysStayDefault(t *testing.T) {
	a := archiveStoreA()
	a.S3Credentials.AccessKeyIDKey = ""
	a.S3Credentials.SecretAccessKeyKey = ""
	a.EndpointCA.Key = ""
	b := archiveStoreB()
	creds, ca, err := cnpgRestoreArchiveAccess(artifactDriverMetadata(t, a), &b)
	if err != nil {
		t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
	}
	want := &strategyv1alpha1.S3CredentialsTemplate{SecretRef: corev1.LocalObjectReference{Name: "creds-a"}}
	if !reflect.DeepEqual(creds, want) {
		t.Errorf("credentials = %+v, want %+v", creds, want)
	}
	if ca == nil || ca.SecretRef.Name != "ca-a" || ca.Key != "" {
		t.Errorf("endpoint CA = %+v, want ca-a with the default key", ca)
	}
}

// An artifact from a controller that recorded only the Secret keeps that
// Secret and takes the key names and the CA it never recorded from the
// strategy.
func TestCNPGRestoreArchiveAccess_LegacyArtifact(t *testing.T) {
	b := archiveStoreB()
	for _, tc := range []struct {
		name       string
		md         map[string]string
		wantSecret string
	}{
		{name: "with a recorded Secret", md: map[string]string{cnpgS3SecretRefKey: "creds-a"}, wantSecret: "creds-a"},
		{name: "without one", md: map[string]string{}, wantSecret: "creds-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds, ca, err := cnpgRestoreArchiveAccess(tc.md, &b)
			if err != nil {
				t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
			}
			if creds == nil || creds.SecretRef.Name != tc.wantSecret || creds.AccessKeyIDKey != "id-b" || creds.SecretAccessKeyKey != "key-b" {
				t.Errorf("credentials = %+v, want Secret %s with the strategy's key names", creds, tc.wantSecret)
			}
			if !reflect.DeepEqual(ca, b.EndpointCA) {
				t.Errorf("endpoint CA = %+v, want the strategy's %+v", ca, b.EndpointCA)
			}
		})
	}
}

// The legacy fallback never writes through to the strategy it was given.
func TestCNPGRestoreArchiveAccess_LegacyArtifactLeavesTheStrategyAlone(t *testing.T) {
	b := archiveStoreB()
	if _, _, err := cnpgRestoreArchiveAccess(map[string]string{cnpgS3SecretRefKey: "creds-a"}, &b); err != nil {
		t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
	}
	if b.S3Credentials.SecretRef.Name != "creds-b" {
		t.Errorf("the rendered strategy now names Secret %q", b.S3Credentials.SecretRef.Name)
	}
}

func TestCNPGRestoreArchiveAccess_UnreadableRecordFails(t *testing.T) {
	b := archiveStoreB()
	if _, _, err := cnpgRestoreArchiveAccess(map[string]string{cnpgArchiveAccessKey: "{"}, &b); err == nil {
		t.Fatalf("an unreadable record was accepted; the restore would fall back to a strategy that may name another location")
	}
}

// A record written before it carried the access settings leaves the
// artifact without them, so a restore treats it as legacy rather than as
// a backup written without credentials or a CA.
func TestCreateCNPGBackupArtifact_RecordWithoutAccessRecordsNone(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
		},
	}
	secret := "creds-a"
	written := cnpgDestination{DestinationPath: "s3://bkt-a/tenant/pg/", S3SecretRef: &secret}
	rendered := &strategyv1alpha1.CNPGTemplate{BarmanObjectStore: archiveStoreB()}
	r := &BackupJobReconciler{Client: newCNPGStrategyTestClient(t)}
	artifact, err := r.createCNPGBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, &cnpgtypes.Backup{ObjectMeta: metav1.ObjectMeta{Name: "cnpg-bk"}}, "postgres-pg", "srv", "postgresql", written, rendered, newPostgresApp("pg", "tenant"))
	if err != nil {
		t.Fatalf("createCNPGBackupArtifact: %v", err)
	}
	if v, ok := artifact.Spec.DriverMetadata[cnpgArchiveAccessKey]; ok {
		t.Errorf("driverMetadata[%s] = %q, want it absent for a record that carries no access settings", cnpgArchiveAccessKey, v)
	}
}

// The size probe reads the recorded Secret under the recorded key names.
func TestCNPGProbeStore_UsesTheRecordedAccess(t *testing.T) {
	a := archiveStoreA()
	b := archiveStoreB()
	written := cnpgDestinationOf(&strategyv1alpha1.CNPGTemplate{BarmanObjectStore: a})
	store := cnpgProbeStore(&b, written)
	if !reflect.DeepEqual(store.S3Credentials, a.S3Credentials) {
		t.Errorf("probe credentials = %+v, want the recorded %+v", store.S3Credentials, a.S3Credentials)
	}
	if !reflect.DeepEqual(store.EndpointCA, a.EndpointCA) {
		t.Errorf("probe endpoint CA = %+v, want the recorded %+v", store.EndpointCA, a.EndpointCA)
	}
	if b.S3Credentials.SecretRef.Name != "creds-b" || b.EndpointCA.SecretRef.Name != "ca-b" {
		t.Errorf("the probe wrote through to the rendered strategy: %+v", b)
	}
}

// TestReconcileCNPGRestore_PatchesTheRecordedAccess drives a restore up to
// the purge after the strategy moved to another bucket with another
// credentials Secret, key names and CA, and reads back what the target app
// is patched with.
func TestReconcileCNPGRestore_PatchesTheRecordedAccess(t *testing.T) {
	const (
		ns      = "tenant"
		appName = "pg"
	)
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	ctx := context.Background()

	secret := func(name string, keys ...string) *corev1.Secret {
		data := map[string][]byte{}
		for _, k := range keys {
			data[k] = []byte("x")
		}
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: data}
	}
	emptyKey := func(s *corev1.Secret, key string) *corev1.Secret {
		s.Data[key] = nil
		return s
	}
	recordedSecrets := func() []client.Object {
		return []client.Object{secret("creds-a", "id-a", "key-a"), secret("ca-a", "ca-a.crt")}
	}

	type outcome struct {
		app    *postgresapp.Postgres
		rj     *backupsv1alpha1.RestoreJob
		err    error
		events []string
	}
	restore := func(t *testing.T, md map[string]string, getErr error, objs ...client.Object) outcome {
		t.Helper()
		snap, err := marshalCNPGBackupSnapshot(newPostgresApp(appName, ns), nil, "")
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		md[cnpgServerNameKey] = "srv"
		md[cnpgBackupNameKey] = "cnpg-bk"
		backup := &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: appName},
				StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "cnpg-strategy"},
				DriverMetadata: md,
			},
			Status: backupsv1alpha1.BackupStatus{UnderlyingResources: snap},
		}
		strategy := &strategyv1alpha1.CNPG{
			ObjectMeta: metav1.ObjectMeta{Name: "cnpg-strategy"},
			Spec:       strategyv1alpha1.CNPGSpec{Template: strategyv1alpha1.CNPGTemplate{BarmanObjectStore: archiveStoreB()}},
		}
		sa := metav1.NewTime(time.Now())
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
			Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
			Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &sa, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
		}
		c := newCNPGStrategyTestClient(t, append([]client.Object{backup, rj, strategy, newPostgresApp(appName, ns)}, objs...)...)
		var cl client.Client = c
		if getErr != nil {
			cl = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, wc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Secret); ok {
						return getErr
					}
					return wc.Get(ctx, key, obj, opts...)
				},
			})
		}
		recorder := record.NewFakeRecorder(10)
		r := &RestoreJobReconciler{Client: cl, Interface: dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t)), Recorder: recorder}
		live := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "rj"}, live); err != nil {
			t.Fatalf("get seeded RestoreJob: %v", err)
		}
		_, reconcileErr := r.reconcileCNPGRestore(ctx, live, backup)
		app := &postgresapp.Postgres{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: appName}, app); err != nil {
			t.Fatalf("get Postgres app: %v", err)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "rj"}, live); err != nil {
			t.Fatalf("get RestoreJob: %v", err)
		}
		var events []string
		for len(recorder.Events) > 0 {
			events = append(events, <-recorder.Events)
		}
		return outcome{app: app, rj: live, err: reconcileErr, events: events}
	}
	recordedCreds := postgresapp.S3CredentialsSecret{Name: "creds-a", AccessKeyIDKey: "id-a", SecretAccessKeyKey: "key-a"}
	recordedCA := postgresapp.EndpointCA{Name: "ca-a", Key: "ca-a.crt"}
	strategyCreds := postgresapp.S3CredentialsSecret{Name: "creds-b", AccessKeyIDKey: "id-b", SecretAccessKeyKey: "key-b"}
	strategyCA := postgresapp.EndpointCA{Name: "ca-b", Key: "ca-b.crt"}
	patched := func(t *testing.T, o outcome, wantCreds postgresapp.S3CredentialsSecret, wantCA postgresapp.EndpointCA) {
		t.Helper()
		if o.err != nil {
			t.Fatalf("reconcileCNPGRestore: %v", o.err)
		}
		if !o.app.Spec.Bootstrap.Enabled {
			t.Fatalf("the reconcile did not reach the restore patch")
		}
		if o.app.Spec.Backup.S3CredentialsSecret != wantCreds {
			t.Errorf("s3CredentialsSecret = %+v, want %+v", o.app.Spec.Backup.S3CredentialsSecret, wantCreds)
		}
		if o.app.Spec.Backup.EndpointCA != wantCA {
			t.Errorf("endpointCA = %+v, want %+v", o.app.Spec.Backup.EndpointCA, wantCA)
		}
	}

	t.Run("the recorded access is patched in", func(t *testing.T) {
		o := restore(t, artifactDriverMetadata(t, archiveStoreA()), nil, recordedSecrets()...)
		patched(t, o, recordedCreds, recordedCA)
		for _, ev := range o.events {
			if strings.Contains(ev, "RecordedArchiveAccessUnavailable") {
				t.Errorf("event %q, but the recorded access was used", ev)
			}
		}
	})

	// Without its Secrets the recorded access cannot work; the strategy's
	// is what the restore used before the record existed, and may still
	// read the old bucket.
	for name, objs := range map[string][]client.Object{
		"the recorded Secret is gone":          {secret("ca-a", "ca-a.crt")},
		"the recorded Secret lacks a key":      {secret("creds-a", "id-a"), secret("ca-a", "ca-a.crt")},
		"the recorded Secret has an empty key": {emptyKey(secret("creds-a", "id-a", "key-a"), "key-a"), secret("ca-a", "ca-a.crt")},
		"the recorded CA Secret is gone":       {secret("creds-a", "id-a", "key-a")},
		"the recorded CA Secret lacks its key": {secret("creds-a", "id-a", "key-a"), secret("ca-a", "other.crt")},
	} {
		t.Run(name+": the strategy's access is patched in", func(t *testing.T) {
			o := restore(t, artifactDriverMetadata(t, archiveStoreA()), nil, objs...)
			patched(t, o, strategyCreds, strategyCA)
			found := false
			for _, ev := range o.events {
				found = found || strings.Contains(ev, "RecordedArchiveAccessUnavailable")
			}
			if !found {
				t.Errorf("events = %v, want one saying the recorded access was not used", o.events)
			}
		})
	}

	t.Run("a recorded CA alone is checked too", func(t *testing.T) {
		a := archiveStoreA()
		a.S3Credentials = archiveStoreB().S3Credentials
		o := restore(t, artifactDriverMetadata(t, a), nil, secret("creds-b", "id-b", "key-b"))
		patched(t, o, strategyCreds, strategyCA)
		found := false
		for _, ev := range o.events {
			found = found || strings.Contains(ev, "RecordedArchiveAccessUnavailable")
		}
		if !found {
			t.Errorf("events = %v, want one saying the recorded access was not used", o.events)
		}
	})

	// A reference with no Secret name is no reference: the backup ran
	// without it, and the restore patch drops it.
	t.Run("an empty recorded CA name is no CA", func(t *testing.T) {
		a := archiveStoreA()
		a.EndpointCA = &strategyv1alpha1.EndpointCARef{}
		o := restore(t, artifactDriverMetadata(t, a), nil, secret("creds-a", "id-a", "key-a"))
		patched(t, o, recordedCreds, postgresapp.EndpointCA{})
	})
	t.Run("an empty recorded Secret name is no credentials", func(t *testing.T) {
		a := archiveStoreA()
		a.S3Credentials.SecretRef.Name = ""
		o := restore(t, artifactDriverMetadata(t, a), nil, secret("ca-a", "ca-a.crt"))
		patched(t, o, postgresapp.S3CredentialsSecret{}, recordedCA)
	})

	t.Run("recorded credentials alone are checked too", func(t *testing.T) {
		a := archiveStoreA()
		a.EndpointCA = archiveStoreB().EndpointCA
		o := restore(t, artifactDriverMetadata(t, a), nil, secret("ca-b", "ca-b.crt"))
		patched(t, o, strategyCreds, strategyCA)
		found := false
		for _, ev := range o.events {
			found = found || strings.Contains(ev, "RecordedArchiveAccessUnavailable")
		}
		if !found {
			t.Errorf("events = %v, want one saying the recorded access was not used", o.events)
		}
	})

	// The strategy's own access is what a restore of an archive that has not
	// moved uses; nothing recorded is passed over, so nothing is reported,
	// whether or not its Secrets exist.
	t.Run("the same archive reports nothing", func(t *testing.T) {
		o := restore(t, artifactDriverMetadata(t, archiveStoreB()), nil)
		patched(t, o, strategyCreds, strategyCA)
		for _, ev := range o.events {
			if strings.Contains(ev, "RecordedArchiveAccessUnavailable") {
				t.Errorf("event %q for an archive the strategy still points at", ev)
			}
		}
	})

	t.Run("default key names are looked up under their defaults", func(t *testing.T) {
		a := archiveStoreA()
		a.S3Credentials.AccessKeyIDKey, a.S3Credentials.SecretAccessKeyKey, a.EndpointCA.Key = "", "", ""
		o := restore(t, artifactDriverMetadata(t, a), nil,
			secret("creds-a", defaultS3AccessKeyIDKey, defaultS3SecretAccessKeyKey), secret("ca-a", defaultEndpointCAKey))
		patched(t, o, postgresapp.S3CredentialsSecret{Name: "creds-a"}, postgresapp.EndpointCA{Name: "ca-a"})
	})

	t.Run("a Secret read error is returned before the patch", func(t *testing.T) {
		o := restore(t, artifactDriverMetadata(t, archiveStoreA()), apierrors.NewServiceUnavailable("apiserver unavailable"), recordedSecrets()...)
		if o.err == nil {
			t.Errorf("reconcileCNPGRestore swallowed the Secret read error")
		}
		if o.app.Spec.Bootstrap.Enabled {
			t.Errorf("the target app was patched although the recorded access could not be checked")
		}
	})

	t.Run("an unreadable record fails the restore before the purge", func(t *testing.T) {
		o := restore(t, map[string]string{cnpgDestinationPathKey: "s3://bkt-a/tenant/pg/", cnpgArchiveAccessKey: "{"}, nil)
		if o.rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
			t.Errorf("phase = %q, want Failed", o.rj.Status.Phase)
		}
		if o.app.Spec.Bootstrap.Enabled {
			t.Errorf("the target app was patched for recovery from a record that could not be read")
		}
	})
}

// Access to an archive that has not moved is rotated in the strategy: the
// Secret renamed, the keys renamed, the CA reissued, the old ones revoked.
// The strategy's access is the one WAL archiving keeps proving against
// that archive, so a restore takes it rather than the recorded one.
func TestCNPGRestoreArchiveAccess_RotatedAccessToTheSameArchive(t *testing.T) {
	rotated := archiveStoreA()
	rotated.S3Credentials = &strategyv1alpha1.S3CredentialsTemplate{
		SecretRef:          corev1.LocalObjectReference{Name: "creds-a-rotated"},
		AccessKeyIDKey:     "id-rotated",
		SecretAccessKeyKey: "key-rotated",
	}
	rotated.EndpointCA = &strategyv1alpha1.EndpointCARef{SecretRef: corev1.LocalObjectReference{Name: "ca-a-rotated"}}
	for name, edit := range map[string]func(*strategyv1alpha1.BarmanObjectStoreTemplate){
		"same prefix":             func(*strategyv1alpha1.BarmanObjectStoreTemplate) {},
		"another prefix":          func(s *strategyv1alpha1.BarmanObjectStoreTemplate) { s.DestinationPath = "s3://bkt-a/tenant/pg-copy/" },
		"endpoint trailing slash": func(s *strategyv1alpha1.BarmanObjectStoreTemplate) { s.EndpointURL += "/" },
	} {
		t.Run(name, func(t *testing.T) {
			store := rotated
			store.S3Credentials = rotated.S3Credentials.DeepCopy()
			store.EndpointCA = rotated.EndpointCA.DeepCopy()
			edit(&store)
			creds, ca, err := cnpgRestoreArchiveAccess(artifactDriverMetadata(t, archiveStoreA()), &store)
			if err != nil {
				t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
			}
			if !reflect.DeepEqual(creds, store.S3Credentials) {
				t.Errorf("credentials = %+v, want the strategy's rotated %+v", creds, store.S3Credentials)
			}
			if !reflect.DeepEqual(ca, store.EndpointCA) {
				t.Errorf("endpoint CA = %+v, want the strategy's rotated %+v", ca, store.EndpointCA)
			}
		})
	}
}

// The same holds for an artifact that recorded only the Secret.
func TestCNPGRestoreArchiveAccess_LegacyArtifactOfTheSameArchive(t *testing.T) {
	a := archiveStoreA()
	md := map[string]string{
		cnpgDestinationPathKey: a.DestinationPath,
		cnpgEndpointURLKey:     a.EndpointURL,
		cnpgS3SecretRefKey:     "creds-a-revoked",
	}
	creds, _, err := cnpgRestoreArchiveAccess(md, &a)
	if err != nil {
		t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
	}
	if !reflect.DeepEqual(creds, a.S3Credentials) {
		t.Errorf("credentials = %+v, want the strategy's %+v", creds, a.S3Credentials)
	}
}

// The location is the endpoint and the bucket; either one moving makes the
// strategy's access belong to another archive.
func TestCNPGRestoreArchiveAccess_MovedArchiveKeepsTheRecordedAccess(t *testing.T) {
	a := archiveStoreA()
	for name, edit := range map[string]func(*strategyv1alpha1.BarmanObjectStoreTemplate){
		"another bucket":   func(s *strategyv1alpha1.BarmanObjectStoreTemplate) { s.DestinationPath = "s3://bkt-b/tenant/pg/" },
		"another endpoint": func(s *strategyv1alpha1.BarmanObjectStoreTemplate) { s.EndpointURL = "https://s3-b.example:8443" },
		"not s3":           func(s *strategyv1alpha1.BarmanObjectStoreTemplate) { s.DestinationPath = "gs://bkt-a/tenant/pg/" },
	} {
		t.Run(name, func(t *testing.T) {
			store := archiveStoreB()
			store.DestinationPath, store.EndpointURL = a.DestinationPath, a.EndpointURL
			edit(&store)
			creds, ca, err := cnpgRestoreArchiveAccess(artifactDriverMetadata(t, a), &store)
			if err != nil {
				t.Fatalf("cnpgRestoreArchiveAccess: %v", err)
			}
			if !reflect.DeepEqual(creds, a.S3Credentials) || !reflect.DeepEqual(ca, a.EndpointCA) {
				t.Errorf("credentials = %+v, endpoint CA = %+v, want the recorded ones", creds, ca)
			}
		})
	}
}
