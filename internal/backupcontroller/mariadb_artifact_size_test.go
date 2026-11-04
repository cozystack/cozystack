// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbapp"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

func dumpKey(prefix string, ts time.Time, compression string) string {
	if compression == "" {
		return fmt.Sprintf("%sbackup.%s.sql", prefix, ts.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%sbackup.%s.%s.sql", prefix, ts.UTC().Format(time.RFC3339), compression)
}

// mariadbBackupSucceeded is the Complete condition mariadb-operator sets on a
// Backup whose Job succeeded. A failed Job also gets Status=True, with
// Reason=JobFailed, so a fixture without the reason is not a success.
func mariadbBackupSucceeded(at metav1.Time) metav1.Condition {
	return metav1.Condition{
		Type:               mariadbtypes.ConditionTypeComplete,
		Status:             metav1.ConditionTrue,
		Reason:             mariadbtypes.ConditionReasonJobComplete,
		Message:            "Success",
		LastTransitionTime: at,
	}
}

func TestParseMariaDBLogicalBackupFile(t *testing.T) {
	cases := map[string]bool{
		"backup.2026-10-21T10:00:00Z.sql":        true,
		"backup.2026-10-21T10:00:00Z.gzip.sql":   true,
		"backup.2026-10-21T10:00:00+04:00.sql":   true,
		"backup.2026-10-21T10:00:00Z.sql.tmp":    false,
		"backup.2026-10-21T10:00:00Z.sql.gz":     true,
		"backup.2026-10-21T10:00:00Z.sql.zst":    true,
		"backup.2026-10-21T10:00:00Z.zstd.sql":   true,
		"backup.2026-10-21T10:00:00Z.xz.sql":     false,
		"backup.2026-10-21T10:00:00Z.sql.gz.tmp": false,
		"physicalbackup-20261021100000.xb":       false,
		"backup.not-a-date.sql":                  false,
		"sub/backup.2026-10-21T10:00:00Z.sql":    false,
		"backup.2026-10-21T10:00:00Z.a.gzip.sql": false,
		"backups.2026-10-21T10:00:00Z.gzip.sql":  false,
		"backup.2026-10-21T10:00:00Z.gzip.dump":  false,
		"":                                       false,
	}
	for name, want := range cases {
		if _, got := parseMariaDBLogicalBackupFile(name); got != want {
			t.Errorf("parseMariaDBLogicalBackupFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMariaDBOperatorS3Prefix(t *testing.T) {
	cases := map[string]string{"": "", "/": "", "ns/app": "ns/app/", "ns/app/": "ns/app/"}
	for in, want := range cases {
		if got := mariadbOperatorS3Prefix(in); got != want {
			t.Errorf("mariadbOperatorS3Prefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// operatorBackupStoringTo returns the operator Backup a strategy with s3
// storage renders, created at created in namespace tenant.
func operatorBackupStoringTo(t *testing.T, s3 *strategyv1alpha1.MariaDBS3Template, created time.Time) *mariadbtypes.Backup {
	t.Helper()
	storage, err := buildMariaDBBackupStorage(strategyv1alpha1.MariaDBStorageTemplate{S3: s3})
	if err != nil {
		t.Fatal(err)
	}
	return &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk", CreationTimestamp: metav1.NewTime(created)},
		Spec:       mariadbtypes.BackupSpec{Storage: storage},
	}
}

func TestMariaDBArtifactSizeBytes(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	completed := created.Add(3 * time.Minute)
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/db"
	mdbBackup := operatorBackupStoringTo(t, s3, created)
	const prefix = "tenant/db/"

	t.Run("single dump inside the backup window", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-24*time.Hour), "gzip"), Size: 111, LastModified: created.Add(-24 * time.Hour)},
			{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 4242, LastModified: created.Add(time.Minute)},
			{Key: prefix + "0-backup-target.txt", Size: 7},
		}}
		got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed)
		if got != 4242 {
			t.Errorf("size = %d, want 4242", got)
		}
		if l.conn.AccessKey != "AKID" || l.conn.Endpoint != "s3.example:9000" {
			t.Errorf("connection = %+v, want the Secret's key and the rendered endpoint", l.conn)
		}
		if l.bucket != "bkt" || l.prefix != prefix || l.recursive {
			t.Errorf("listed %q/%q recursive=%v, want bkt/%q non-recursive", l.bucket, l.prefix, l.recursive, prefix)
		}
	})

	t.Run("reads the endpoint CA from its Secret", func(t *testing.T) {
		tlsS3 := newRenderedMariaDBTemplate().Storage.S3
		tlsS3.Prefix = "tenant/db"
		tlsS3.TLS = &strategyv1alpha1.MariaDBS3TLS{
			Enabled:        true,
			CASecretKeyRef: &strategyv1alpha1.MariaDBSecretKeySelector{Name: "ca", Key: "ca.crt"},
		}
		ca := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "ca"},
			Data:       map[string][]byte{"ca.crt": []byte("PEM")},
		}
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 3, LastModified: created.Add(time.Minute)}}}
		r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"), ca)
		if got := r.mariadbArtifactSizeBytes(context.Background(), operatorBackupStoringTo(t, tlsS3, created), completed); got != 3 {
			t.Errorf("size = %d, want 3", got)
		}
		if !l.conn.Secure || string(l.conn.CAPEM) != "PEM" {
			t.Errorf("connection = %+v, want TLS with the Secret's CA", l.conn)
		}
	})

	t.Run("reads the session token from its Secret", func(t *testing.T) {
		stsS3 := newRenderedMariaDBTemplate().Storage.S3
		stsS3.Prefix = "tenant/db"
		stsS3.SessionTokenSecretKeyRef = &strategyv1alpha1.MariaDBSecretKeySelector{Name: "sts", Key: "token"}
		sts := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "sts"},
			Data:       map[string][]byte{"token": []byte("TOKEN")},
		}
		l := &fakeS3Lister{objects: []s3ObjectInfo{{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 4, LastModified: created.Add(time.Minute)}}}
		r := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds"), sts)
		if got := r.mariadbArtifactSizeBytes(context.Background(), operatorBackupStoringTo(t, stsS3, created), completed); got != 4 {
			t.Errorf("size = %d, want 4", got)
		}
		if l.conn.SessionToken != "TOKEN" {
			t.Errorf("session token = %q, want the Secret's", l.conn.SessionToken)
		}
	})

	t.Run("pod clock skew within tolerance", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-30*time.Second), ""), Size: 9, LastModified: created.Add(time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 9 {
			t.Errorf("size = %d, want 9", got)
		}
	})

	t.Run("a skewed pod clock does not attribute a neighbouring run's dump", func(t *testing.T) {
		// The pod clock runs 5m behind. This run's dump is named outside the
		// window; the previous run's dump, uploaded before this run started,
		// is named inside it.
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(-4*time.Minute), "gzip"), Size: 7, LastModified: created.Add(time.Minute)},
			{Key: dumpKey(prefix, created.Add(30*time.Second), "gzip"), Size: 666, LastModified: created.Add(-4 * time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0: no dump is inside the window on both clocks", got)
		}
	})

	t.Run("a dump uploaded just after Complete is inside the slack", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, completed.Add(30*time.Second), "gzip"), Size: 5, LastModified: completed.Add(30 * time.Second)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 5 {
			t.Errorf("size = %d, want 5", got)
		}
	})

	t.Run("a hanging endpoint is given up at the probe timeout", func(t *testing.T) {
		r := reconcilerWithLister(t, &fakeS3Lister{}, s3CredsSecret("tenant", "creds"))
		r.newS3ObjectLister = func(s3Connection) (s3ObjectLister, error) { return blockingLister{}, nil }
		withShortProbeTimeout(t, func() {
			if got := r.mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
				t.Errorf("size = %d, want 0", got)
			}
		})
	})

	t.Run("two dumps in the window leave size unset", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 1, LastModified: created.Add(time.Minute)},
			{Key: dumpKey(prefix, created.Add(2*time.Minute), "gzip"), Size: 2, LastModified: created.Add(2 * time.Minute)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0 (ambiguous)", got)
		}
	})

	t.Run("no dump in the window leaves size unset", func(t *testing.T) {
		l := &fakeS3Lister{objects: []s3ObjectInfo{
			{Key: dumpKey(prefix, completed.Add(time.Hour), "gzip"), Size: 1, LastModified: completed.Add(time.Hour)},
		}}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})

	t.Run("list error leaves size unset", func(t *testing.T) {
		l := &fakeS3Lister{err: errors.New("AccessDenied")}
		if got := reconcilerWithLister(t, l, s3CredsSecret("tenant", "creds")).mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})

	t.Run("missing credentials Secret leaves size unset", func(t *testing.T) {
		c := newMariaDBStrategyTestClient(t)
		r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
		if got := r.mariadbArtifactSizeBytes(context.Background(), mdbBackup, completed); got != 0 {
			t.Errorf("size = %d, want 0", got)
		}
	})
}

// TestMatchMariaDBDump pins what a restore reads off a match: the object
// itself, and errors it can tell apart.
func TestMatchMariaDBDump(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	completed := created.Add(3 * time.Minute)
	const prefix = "tenant/db/"
	own := s3ObjectInfo{Key: dumpKey(prefix, created.Add(time.Minute), "gzip"), Size: 4242, LastModified: created.Add(2 * time.Minute)}
	run := newMariaDBRun(&mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk"}}, created, completed)

	got, err := matchMariaDBDump([]s3ObjectInfo{own}, prefix, run, nil)
	if err != nil {
		t.Fatalf("matchMariaDBDump: %v", err)
	}
	want := mariadbDump{
		Key:          own.Key,
		Name:         strings.TrimPrefix(own.Key, prefix),
		Timestamp:    created.Add(time.Minute),
		Size:         4242,
		LastModified: own.LastModified,
	}
	if got != want {
		t.Errorf("matchMariaDBDump = %+v, want %+v", got, want)
	}

	if _, err := matchMariaDBDump(nil, prefix, run, nil); !errors.Is(err, errMariaDBDumpNotFound) {
		t.Errorf("no dump: err = %v, want errMariaDBDumpNotFound", err)
	}
	second := s3ObjectInfo{Key: dumpKey(prefix, created.Add(2*time.Minute), "gzip"), Size: 1, LastModified: created.Add(2 * time.Minute)}
	if _, err := matchMariaDBDump([]s3ObjectInfo{own, second}, prefix, run, nil); !errors.Is(err, errMariaDBDumpAmbiguous) {
		t.Errorf("two dumps: err = %v, want errMariaDBDumpAmbiguous", err)
	}
}

// TestFindMariaDBDump pins that a dump is read with nothing but a client,
// as a reconciler other than BackupJobReconciler has, and that an operator
// Backup that does not store to S3 is refused without a listing.
func TestFindMariaDBDump(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	completed := created.Add(3 * time.Minute)
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/db"
	l := &fakeS3Lister{objects: []s3ObjectInfo{
		{Key: dumpKey("tenant/db/", created.Add(time.Minute), "gzip"), Size: 4242, LastModified: created.Add(time.Minute)},
	}}
	store := objectStore{
		reader: newMariaDBStrategyTestClient(t, s3CredsSecret("tenant", "creds")),
		newLister: func(conn s3Connection) (s3ObjectLister, error) {
			l.conn = conn
			return l, nil
		},
	}

	got, err := findMariaDBDump(context.Background(), store, operatorBackupStoringTo(t, s3, created), completed)
	if err != nil || got.Size != 4242 {
		t.Fatalf("findMariaDBDump = %+v, %v; want the 4242-byte dump", got, err)
	}
	if l.conn.AccessKey != "AKID" {
		t.Errorf("connection = %+v, want the Secret's key", l.conn)
	}

	pvc := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-pvc", CreationTimestamp: metav1.NewTime(created)},
		Spec:       mariadbtypes.BackupSpec{Storage: mariadbtypes.BackupStorage{PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{}}},
	}
	if _, err := findMariaDBDump(context.Background(), store, pvc, completed); err == nil {
		t.Error("findMariaDBDump on a PVC-backed operator Backup returned no error")
	}
	if l.calls != 1 {
		t.Errorf("S3 listed %d times, want once: a PVC-backed Backup has nothing to list", l.calls)
	}
}

// TestFindMariaDBDump_Ownership drives runs of one application that share
// a prefix. This run lives from t0 to t0+40s, so its window with the
// one-minute slack is [t0-1m, t0+1m40s]; its own dump is 123 bytes and a
// neighbour's is 666 or 777. A neighbour's dump must never be returned.
func TestFindMariaDBDump_Ownership(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Prefix = "tenant/db"
	const prefix = "tenant/db/"
	run := func(name string, created, completed time.Time) *mariadbtypes.Backup {
		b := operatorBackupStoringTo(t, s3, created)
		b.Name = name
		b.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(metav1.NewTime(completed))}
		return b
	}
	dump := func(named, uploaded time.Time, size int64) s3ObjectInfo {
		return s3ObjectInfo{Key: dumpKey(prefix, named, "gzip"), Size: size, LastModified: uploaded}
	}
	own := run("op-bk", t0, t0.Add(40*time.Second))
	find := func(t *testing.T, objects []s3ObjectInfo, operatorBackups ...client.Object) (mariadbDump, error) {
		t.Helper()
		store := objectStore{
			reader: newMariaDBStrategyTestClient(t, append(operatorBackups, own, s3CredsSecret("tenant", "creds"))...),
			newLister: func(s3Connection) (s3ObjectLister, error) {
				return &fakeS3Lister{objects: objects}, nil
			},
		}
		return findMariaDBDump(context.Background(), store, own, t0.Add(40*time.Second))
	}
	wantAmbiguous := func(t *testing.T, got mariadbDump, err error) {
		t.Helper()
		if !errors.Is(err, errMariaDBDumpAmbiguous) {
			t.Errorf("findMariaDBDump = %+v, %v; want errMariaDBDumpAmbiguous", got, err)
		}
	}
	wantOwn := func(t *testing.T, got mariadbDump, err error) {
		t.Helper()
		if err != nil || got.Size != 123 {
			t.Errorf("findMariaDBDump = %+v, %v; want this run's 123-byte dump", got, err)
		}
	}

	t.Run("a pod clock behind does not hand this run the previous run's dump", func(t *testing.T) {
		// The previous run finished 20s before this one was created and its
		// operator Backup is gone. This run's pod clock is 5m behind, so its
		// own dump is named outside the window and uploaded inside it.
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(-30*time.Second), t0.Add(-20*time.Second), 666),
			dump(t0.Add(5*time.Second-5*time.Minute), t0.Add(30*time.Second), 123),
		})
		wantAmbiguous(t, got, err)
	})

	t.Run("a pod clock ahead does not hand this run the next run's dump", func(t *testing.T) {
		// The next run started 5s after this one completed and has uploaded
		// its dump by the time this one is probed; it has not completed yet.
		next := operatorBackupStoringTo(t, s3, t0.Add(45*time.Second))
		next.Name = "op-next"
		next.Status.Conditions = []metav1.Condition{{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionFalse,
			Reason: mariadbtypes.ConditionReasonJobRunning, Message: "Running", LastTransitionTime: metav1.NewTime(t0.Add(45 * time.Second)),
		}}
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(5*time.Second+5*time.Minute), t0.Add(30*time.Second), 123),
			dump(t0.Add(50*time.Second), t0.Add(80*time.Second), 777),
		}, next)
		wantAmbiguous(t, got, err)
	})

	t.Run("an object store clock behind does not hand this run the next run's dump", func(t *testing.T) {
		// The store clock is 2m behind: this run's upload is stamped before
		// the window opens, the next run's is pulled back into it.
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(5*time.Second), t0.Add(30*time.Second-2*time.Minute), 123),
			dump(t0.Add(50*time.Second), t0.Add(3*time.Minute-2*time.Minute), 777),
		})
		wantAmbiguous(t, got, err)
	})

	t.Run("a dump another finished run wrote is not taken when this run's own is gone", func(t *testing.T) {
		// This run's dump was pruned; the next run's falls in this run's
		// window on both clocks, and in its own.
		next := run("op-next", t0.Add(45*time.Second), t0.Add(85*time.Second))
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(50*time.Second), t0.Add(75*time.Second), 777),
		}, next)
		wantAmbiguous(t, got, err)
	})

	t.Run("a dump the window holds on one clock only is not taken", func(t *testing.T) {
		// This run's dump is gone; the previous run's operator Backup is too,
		// and its dump, started 2m before this run, finished uploading 20s
		// before it.
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(-2*time.Minute), t0.Add(-20*time.Second), 666),
		})
		if err == nil {
			t.Errorf("findMariaDBDump = %+v; want no dump attributed", got)
		}
	})

	t.Run("a run still in progress does not contest this run's dump", func(t *testing.T) {
		// A concurrent run created 30s before this one has not uploaded yet.
		running := operatorBackupStoringTo(t, s3, t0.Add(-30*time.Second))
		running.Name = "op-running"
		running.Status.Conditions = []metav1.Condition{{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionFalse,
			Reason: mariadbtypes.ConditionReasonJobRunning, Message: "Running", LastTransitionTime: metav1.NewTime(t0.Add(-30 * time.Second)),
		}}
		got, err := find(t, []s3ObjectInfo{dump(t0.Add(5*time.Second), t0.Add(30*time.Second), 123)}, running)
		wantOwn(t, got, err)
	})

	t.Run("runs minutes apart each keep their own dump", func(t *testing.T) {
		previous := run("op-previous", t0.Add(-4*time.Minute), t0.Add(-3*time.Minute))
		next := run("op-next", t0.Add(3*time.Minute), t0.Add(4*time.Minute))
		got, err := find(t, []s3ObjectInfo{
			dump(t0.Add(-230*time.Second), t0.Add(-190*time.Second), 666),
			dump(t0.Add(5*time.Second), t0.Add(30*time.Second), 123),
			dump(t0.Add(185*time.Second), t0.Add(220*time.Second), 777),
		}, previous, next)
		wantOwn(t, got, err)
	})

	t.Run("a run that writes elsewhere does not contest this run's dump", func(t *testing.T) {
		other := newRenderedMariaDBTemplate().Storage.S3
		other.Prefix = "tenant/other"
		elsewhere := operatorBackupStoringTo(t, other, t0)
		elsewhere.Name = "op-elsewhere"
		elsewhere.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(metav1.NewTime(t0.Add(40 * time.Second)))}
		got, err := find(t, []s3ObjectInfo{dump(t0.Add(5*time.Second), t0.Add(30*time.Second), 123)}, elsewhere)
		wantOwn(t, got, err)
	})

	t.Run("a run stuck short of Complete does not contest later runs", func(t *testing.T) {
		// A backup Job whose pod never starts leaves its operator Backup
		// JobRunning for good.
		stuck := operatorBackupStoringTo(t, s3, t0.Add(-3*time.Hour))
		stuck.Name = "op-stuck"
		stuck.Status.Conditions = []metav1.Condition{{
			Type: mariadbtypes.ConditionTypeComplete, Status: metav1.ConditionFalse,
			Reason: mariadbtypes.ConditionReasonJobRunning, Message: "Running", LastTransitionTime: metav1.NewTime(t0.Add(-3 * time.Hour)),
		}}
		got, err := find(t, []s3ObjectInfo{dump(t0.Add(5*time.Second), t0.Add(30*time.Second), 123)}, stuck)
		wantOwn(t, got, err)
	})
}

// TestMariaDBArtifactSizeBytes_MinioClient drives the real minio client
// against a stub ListObjectsV2 endpoint, so the Secret wiring, prefix and
// response parsing are exercised together.
func TestMariaDBArtifactSizeBytes_MinioClient(t *testing.T) {
	created := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	key := dumpKey("tenant/db/", created.Add(time.Minute), "gzip")

	var gotPath, gotPrefix, gotDelimiter, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath, gotPrefix, gotAuth = req.URL.Path, req.URL.Query().Get("prefix"), req.Header.Get("Authorization")
		gotDelimiter = req.URL.Query().Get("delimiter")
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>bkt</Name><Prefix>tenant/db/</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
  <Contents><Key>%s</Key><Size>31337</Size><ETag>"x"</ETag><LastModified>2026-10-21T10:01:00.000Z</LastModified></Contents>
  <CommonPrefixes><Prefix>tenant/db/srv/</Prefix></CommonPrefixes>
</ListBucketResult>`, key)
	}))
	defer srv.Close()

	c := newMariaDBStrategyTestClient(t, s3CredsSecret("tenant", "creds"))
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}

	s3 := newRenderedMariaDBTemplate().Storage.S3
	s3.Endpoint = strings.TrimPrefix(srv.URL, "http://")
	s3.Prefix = "tenant/db/"
	s3.Region = "us-east-1"

	if got := r.mariadbArtifactSizeBytes(context.Background(), operatorBackupStoringTo(t, s3, created), created.Add(3*time.Minute)); got != 31337 {
		t.Fatalf("size = %d, want 31337", got)
	}
	if gotPath != "/bkt/" || gotPrefix != "tenant/db/" || gotDelimiter != "/" {
		t.Errorf("request path=%q prefix=%q delimiter=%q, want /bkt/, tenant/db/ and a non-recursive listing", gotPath, gotPrefix, gotDelimiter)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") {
		t.Errorf("request not signed with the Secret's access key: %q", gotAuth)
	}
}

func TestCreateMariaDBBackupArtifact_SizeBytes(t *testing.T) {
	apps := mariadbapp.GroupName
	completed := metav1.NewTime(time.Date(2026, 10, 21, 10, 3, 0, 0, time.UTC))
	rendered := newRenderedMariaDBTemplate()
	rendered.Storage.S3.Prefix = "src/"
	mdbBackup := operatorBackupStoringTo(t, rendered.Storage.S3, completed.Add(-3*time.Minute))
	mdbBackup.Name = "src-op-bk"
	mdbBackup.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(completed)}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-size"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	r := reconcilerWithLister(t, &fakeS3Lister{objects: []s3ObjectInfo{
		{Key: dumpKey("src/", completed.Add(-time.Minute), "gzip"), Size: 2048, LastModified: completed.Add(-time.Minute)},
	}}, job, s3CredsSecret("tenant", "creds"))

	artefact, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, mdbBackup, rendered)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if artefact.Status.Artifact == nil || artefact.Status.Artifact.SizeBytes != 2048 {
		t.Fatalf("Artifact = %#v, want SizeBytes 2048", artefact.Status.Artifact)
	}
}

// TestCreateMariaDBBackupArtifact_StrategyEditedMidBackup pins that the
// probe reads where the operator Backup wrote its dump, not where the
// strategy, edited while the backup ran, now points.
func TestCreateMariaDBBackupArtifact_StrategyEditedMidBackup(t *testing.T) {
	apps := mariadbapp.GroupName
	completed := metav1.NewTime(time.Date(2026, 10, 21, 10, 3, 0, 0, time.UTC))
	original := newRenderedMariaDBTemplate().Storage.S3
	original.Prefix = "original/"
	mdbBackup := operatorBackupStoringTo(t, original, completed.Add(-3*time.Minute))
	mdbBackup.Status.Conditions = []metav1.Condition{mariadbBackupSucceeded(completed)}
	edited := newRenderedMariaDBTemplate()
	edited.Storage.S3.Bucket = "replacement-bkt"
	edited.Storage.S3.Endpoint = "replacement.example:9000"
	edited.Storage.S3.Prefix = "replacement/"
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-edited"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	l := &fakeS3Lister{objects: []s3ObjectInfo{
		{Key: dumpKey("original/", completed.Add(-time.Minute), "gzip"), Size: 2048, LastModified: completed.Add(-time.Minute)},
	}}
	r := reconcilerWithLister(t, l, job, s3CredsSecret("tenant", "creds"))
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}

	artefact, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, mdbBackup, edited)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if l.bucket != "bkt" || l.prefix != "original/" || l.conn.Endpoint != "s3.example:9000" {
		t.Errorf("listed %s/%s at %s, want bkt/original/ at s3.example:9000, where the operator Backup wrote", l.bucket, l.prefix, l.conn.Endpoint)
	}
	if artefact.Status.Artifact == nil || artefact.Status.Artifact.SizeBytes != 2048 {
		t.Errorf("Artifact = %#v, want SizeBytes 2048", artefact.Status.Artifact)
	}
}

func TestCreateMariaDBBackupArtifact_ExistingBackupSkipsProbe(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-retry"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	existing := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-retry"},
		Status:     backupsv1alpha1.BackupStatus{Artifact: &backupsv1alpha1.BackupArtifact{URI: "s3://bkt/src/op-bk", SizeBytes: 99}},
	}
	l := &fakeS3Lister{}
	r := reconcilerWithLister(t, l, job, existing, s3CredsSecret("tenant", "creds"))
	rendered := newRenderedMariaDBTemplate()
	rendered.Storage.S3.Prefix = "src/"
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "mariadb-strategy-default"},
		Parameters:  map[string]string{},
	}

	got, err := r.createMariaDBBackupArtifact(context.Background(), job, resolved, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op-bk"}}, rendered)
	if err != nil {
		t.Fatalf("createMariaDBBackupArtifact: %v", err)
	}
	if l.calls != 0 {
		t.Errorf("S3 listed %d times for a Backup that already exists", l.calls)
	}
	if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 99 {
		t.Errorf("Artifact = %#v, want the existing one", got.Status.Artifact)
	}
}

func TestCreateMariaDBBackupArtifact_TransientRetryReadStillCreates(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-transient"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	l := &fakeS3Lister{}
	r := reconcilerWithLister(t, l, job, s3CredsSecret("tenant", "creds"))
	r.APIReader = failingReads(t, newMariaDBStrategyTestClient(t))
	got, err := r.createMariaDBBackupArtifact(context.Background(), job, &ResolvedBackupConfig{}, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op"}}, newRenderedMariaDBTemplate())
	if err != nil || got == nil || got.Name != "bj-transient" {
		t.Fatalf("createMariaDBBackupArtifact = %v, %v; a transient read must not fail the backup", got, err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(got), &backupsv1alpha1.Backup{}); err != nil {
		t.Errorf("the Backup was not created: %v", err)
	}
	if l.calls != 0 {
		t.Errorf("S3 probed %d times after the existence check failed", l.calls)
	}
}

func TestCreateMariaDBBackupArtifact_AlreadyExistsRefetchReadsPastTheCache(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-race"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-race"}}
	store := newMariaDBStrategyTestClient(t, job, backup)
	r := &BackupJobReconciler{Client: staleCache(t, store), APIReader: notFoundOnce(t, store), Scheme: store.Scheme()}
	got, err := r.createMariaDBBackupArtifact(context.Background(), job, &ResolvedBackupConfig{}, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op"}}, &strategyv1alpha1.MariaDBTemplate{})
	if err != nil || got == nil || got.Name != "bj-race" {
		t.Fatalf("createMariaDBBackupArtifact = %v, %v; want the Backup that already exists", got, err)
	}
}

func TestCreateMariaDBBackupArtifact_UnreadableExistingBackupIsRetried(t *testing.T) {
	apps := mariadbapp.GroupName
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-unreadable"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{Kind: "MariaDB", Name: "src", APIGroup: &apps},
		},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-unreadable"}}
	store := newMariaDBStrategyTestClient(t, job, backup)
	r := &BackupJobReconciler{Client: staleCache(t, store), APIReader: failingReads(t, store), Scheme: store.Scheme()}
	_, err := r.createMariaDBBackupArtifact(context.Background(), job, &ResolvedBackupConfig{}, &mariadbtypes.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "op"}}, &strategyv1alpha1.MariaDBTemplate{})
	if !errors.Is(err, errBackupNotReadBack) {
		t.Errorf("err = %v, want errBackupNotReadBack", err)
	}
}

// TestReconcileMariaDB_UnreadableBackupIsRetried drives a completed backup
// through the reconcile: when the Backup exists but cannot be read back,
// the reconcile is retried and the BackupJob is not failed.
func TestReconcileMariaDB_UnreadableBackupIsRetried(t *testing.T) {
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	now := metav1.Now()
	bj := newMariaDBBackupJob("bj-unreadable", "tenant")
	bj.Status.StartedAt = &now
	bj.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	strategy := &strategyv1alpha1.MariaDB{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-strategy"},
		Spec:       strategyv1alpha1.MariaDBSpec{Template: *newRenderedMariaDBTemplate()},
	}
	operatorMariaDB := &mariadbtypes.MariaDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mariadbNameForApp("mariadb-src")}}
	operatorBackup := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "op-bk",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      bj.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: bj.Namespace,
			},
		},
		Status: mariadbtypes.BackupStatus{Conditions: []metav1.Condition{mariadbBackupSucceeded(now)}},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: bj.Name}}
	store := newMariaDBStrategyTestClient(t, bj, strategy, newMariaDBApp("mariadb-src", "tenant"), operatorMariaDB, operatorBackup, backup)
	r := &BackupJobReconciler{
		Client:    store,
		APIReader: failingReads(t, store),
		Scheme:    store.Scheme(),
		Recorder:  record.NewFakeRecorder(10),
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		APIGroup: &strategyGroup, Kind: strategyv1alpha1.MariaDBStrategyKind, Name: strategy.Name,
	}}

	_, err := r.reconcileMariaDB(context.Background(), bj, resolved)
	if !errors.Is(err, errBackupNotReadBack) {
		t.Fatalf("reconcileMariaDB err = %v, want errBackupNotReadBack so the reconcile is retried", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := store.Get(context.Background(), client.ObjectKeyFromObject(bj), persisted); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if persisted.Status.Phase == backupsv1alpha1.BackupJobPhaseFailed {
		t.Error("the BackupJob was failed although its Backup exists")
	}
}

// TestReconcileMariaDB_RejectedCreateFailsTheJob pins that only the
// read-back is retried: a Create the apiserver rejects still fails the
// BackupJob.
func TestReconcileMariaDB_RejectedCreateFailsTheJob(t *testing.T) {
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	now := metav1.Now()
	bj := newMariaDBBackupJob("bj-rejected", "tenant")
	bj.Status.StartedAt = &now
	bj.Status.Phase = backupsv1alpha1.BackupJobPhaseRunning
	strategy := &strategyv1alpha1.MariaDB{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-strategy"},
		Spec:       strategyv1alpha1.MariaDBSpec{Template: *newRenderedMariaDBTemplate()},
	}
	operatorMariaDB := &mariadbtypes.MariaDB{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: mariadbNameForApp("mariadb-src")}}
	operatorBackup := &mariadbtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant", Name: "op-bk",
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      bj.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: bj.Namespace,
			},
		},
		Status: mariadbtypes.BackupStatus{Conditions: []metav1.Condition{mariadbBackupSucceeded(now)}},
	}
	store := newMariaDBStrategyTestClient(t, bj, strategy, newMariaDBApp("mariadb-src", "tenant"), operatorMariaDB, operatorBackup)
	r := &BackupJobReconciler{
		Client:   forbidBackupCreate(t, store),
		Scheme:   store.Scheme(),
		Recorder: record.NewFakeRecorder(10),
	}
	resolved := &ResolvedBackupConfig{StrategyRef: corev1.TypedLocalObjectReference{
		APIGroup: &strategyGroup, Kind: strategyv1alpha1.MariaDBStrategyKind, Name: strategy.Name,
	}}

	if _, err := r.reconcileMariaDB(context.Background(), bj, resolved); err != nil {
		t.Fatalf("reconcileMariaDB err = %v, want the BackupJob failed rather than retried", err)
	}
	persisted := &backupsv1alpha1.BackupJob{}
	if err := store.Get(context.Background(), client.ObjectKeyFromObject(bj), persisted); err != nil {
		t.Fatalf("get BackupJob: %v", err)
	}
	if persisted.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Errorf("phase = %q, want Failed for a rejected Create", persisted.Status.Phase)
	}
}
