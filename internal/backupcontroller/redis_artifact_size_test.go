// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const redisTestKey = "tenant/cache/bj-1.rdb"

func projectedCredsSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "cozy-backups-creds"},
		Data:       map[string][]byte{},
	}
	for k, v := range map[string]string{
		"endpoint":              "s3.example.org",
		"bucketName":            "cozy-backups",
		"region":                "us-east-1",
		"AWS_ACCESS_KEY_ID":     "AKID",
		"AWS_SECRET_ACCESS_KEY": "secret",
	} {
		s.Data[k] = []byte(v)
	}
	for k, v := range data {
		if v == "" {
			delete(s.Data, k)
			continue
		}
		s.Data[k] = []byte(v)
	}
	return s
}

func redisReconciler(t *testing.T, l *fakeS3Lister, objs ...client.Object) *BackupJobReconciler {
	r := reconcilerWithLister(t, l, objs...)
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	return r
}

func TestRedisArtifact(t *testing.T) {
	t.Run("stats the snapshot with the projected credentials", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 2048}
		r := redisReconciler(t, l, projectedCredsSecret(nil))
		got := r.redisArtifact(context.Background(), "tenant", redisTestKey, nil)
		if got == nil || got.URI != "s3://cozy-backups/"+redisTestKey || got.SizeBytes != 2048 {
			t.Fatalf("artifact = %#v", got)
		}
		if l.bucket != "cozy-backups" || l.statKey != redisTestKey {
			t.Errorf("stat %q/%q, want cozy-backups/%q", l.bucket, l.statKey, redisTestKey)
		}
		if l.conn.Endpoint != "s3.example.org" || !l.conn.Secure || l.conn.AccessKey != "AKID" || l.conn.Region != "us-east-1" {
			t.Errorf("connection = %+v", l.conn)
		}
	})

	unset := map[string]struct {
		lister   *fakeS3Lister
		objs     []client.Object
		noTarget bool
	}{
		"missing object":     {lister: &fakeS3Lister{statErr: errors.New("NoSuchKey")}, objs: []client.Object{projectedCredsSecret(nil)}},
		"missing Secret":     {lister: &fakeS3Lister{statSize: 1}},
		"missing bucket":     {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"bucketName": ""})}},
		"missing keys":       {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"AWS_SECRET_ACCESS_KEY": ""})}},
		"no target name":     {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(nil)}, noTarget: true},
		"missing endpoint":   {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"endpoint": ""})}},
		"missing access key": {lister: &fakeS3Lister{statSize: 1}, objs: []client.Object{projectedCredsSecret(map[string]string{"AWS_ACCESS_KEY_ID": ""})}},
	}
	for name, tc := range unset {
		t.Run(name+" leaves the artifact unset", func(t *testing.T) {
			r := redisReconciler(t, tc.lister, tc.objs...)
			if tc.noTarget {
				r.CredentialsConfig.TargetSecretName = ""
			}
			if got := r.redisArtifact(context.Background(), "tenant", redisTestKey, nil); got != nil {
				t.Errorf("artifact = %#v, want nil", got)
			}
		})
	}
}

// TestRedisArtifact_MinioClient runs the real minio client against a TLS
// stub with a self-signed certificate that answers the HEAD request. It
// covers, in one pass, the trust settings the backup Job used, request
// signing and Content-Length parsing.
func TestRedisArtifact_MinioClient(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod, gotPath, gotAuth = req.Method, req.URL.Path, req.Header.Get("Authorization")
		w.Header().Set("Content-Length", "4096")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 10:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	creds := projectedCredsSecret(map[string]string{"endpoint": strings.TrimPrefix(srv.URL, "https://")})
	ca := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "s3-endpoint-ca"},
		Data:       map[string][]byte{"ca.crt": caPEM},
	}
	c := newMariaDBStrategyTestClient(t, creds, ca)
	r := &BackupJobReconciler{Client: c, Scheme: c.Scheme()}
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"

	if got := r.redisArtifact(context.Background(), "tenant", redisTestKey, &corev1.PodSpec{}); got != nil {
		t.Errorf("without the Job's trust settings: artifact = %#v, want nil (untrusted certificate)", got)
	}

	got := r.redisArtifact(context.Background(), "tenant", redisTestKey, redisPodSpec("s3-endpoint-ca", ""))
	if got == nil || got.SizeBytes != 4096 {
		t.Fatalf("with the s3-ca volume: artifact = %#v, want SizeBytes 4096", got)
	}
	if gotMethod != http.MethodHead || gotPath != "/cozy-backups/"+redisTestKey {
		t.Errorf("request %s %s, want HEAD /cozy-backups/%s", gotMethod, gotPath, redisTestKey)
	}
	if !strings.Contains(gotAuth, "Credential=AKID/") {
		t.Errorf("request not signed with the Secret's access key: %q", gotAuth)
	}

	if got := r.redisArtifact(context.Background(), "tenant", redisTestKey, redisPodSpec("", "true")); got == nil || got.SizeBytes != 4096 {
		t.Errorf("with S3_INSECURE_SKIP_VERIFY=true: artifact = %#v, want SizeBytes 4096", got)
	}
}

// redisPodSpec builds the parts of the backup Job's pod spec that
// redisS3Trust reads: the s3-ca volume when caSecret is set, and the
// S3_INSECURE_SKIP_VERIFY variable.
func redisPodSpec(caSecret, skipVerify string) *corev1.PodSpec {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "backup",
		Env:  []corev1.EnvVar{{Name: "S3_INSECURE_SKIP_VERIFY", Value: skipVerify}},
	}}}
	if caSecret != "" {
		spec.Volumes = []corev1.Volume{{
			Name:         "s3-ca",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: caSecret}},
		}}
	}
	return spec
}

func TestRedisS3Trust(t *testing.T) {
	caSecret := func(data map[string]string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "s3-endpoint-ca"}, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	cases := map[string]struct {
		spec     *corev1.PodSpec
		objs     []client.Object
		wantCA   string
		wantSkip bool
	}{
		"ca.crt wins over tls.crt and skip-verify": {
			spec:   redisPodSpec("s3-endpoint-ca", "true"),
			objs:   []client.Object{caSecret(map[string]string{"ca.crt": "CA", "tls.crt": "TLS"})},
			wantCA: "CA",
		},
		"tls.crt when there is no ca.crt": {
			spec:   redisPodSpec("s3-endpoint-ca", ""),
			objs:   []client.Object{caSecret(map[string]string{"tls.crt": "TLS"})},
			wantCA: "TLS",
		},
		"volume items map the file to another key": {
			spec: func() *corev1.PodSpec {
				s := redisPodSpec("s3-endpoint-ca", "")
				s.Volumes[0].Secret.Items = []corev1.KeyToPath{{Key: "bundle.pem", Path: "ca.crt"}}
				return s
			}(),
			objs:   []client.Object{caSecret(map[string]string{"bundle.pem": "MAPPED", "ca.crt": "UNMAPPED"})},
			wantCA: "MAPPED",
		},
		"missing CA Secret falls back to the system roots": {
			spec: redisPodSpec("s3-endpoint-ca", ""),
		},
		"skip-verify without a CA volume": {
			spec:     redisPodSpec("", "true"),
			wantSkip: true,
		},
		"skip-verify only when it is exactly true": {
			spec: redisPodSpec("", "false"),
		},
		"no pod spec": {},
	}
	t.Run("a mounted CA Secret that is gone never turns into skip-verify", func(t *testing.T) {
		r := &BackupJobReconciler{Client: newMariaDBStrategyTestClient(t)}
		ca, skip, err := r.redisS3Trust(context.Background(), "tenant", redisPodSpec("s3-endpoint-ca", "true"))
		if err == nil || skip || ca != nil {
			t.Errorf("trust = (%q, %v, %v), want an error and no skip-verify", ca, skip, err)
		}
	})
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newMariaDBStrategyTestClient(t, tc.objs...)
			r := &BackupJobReconciler{Client: c}
			ca, skip, err := r.redisS3Trust(context.Background(), "tenant", tc.spec)
			if err != nil {
				t.Fatalf("redisS3Trust: %v", err)
			}
			if string(ca) != tc.wantCA || skip != tc.wantSkip {
				t.Errorf("trust = (%q, %v), want (%q, %v)", ca, skip, tc.wantCA, tc.wantSkip)
			}
		})
	}
}

func TestCreateRedisBackupArtifact_Artifact(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "Redis", Name: "cache"},
		},
	}

	t.Run("records the snapshot location and size", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 777}
		r := redisReconciler(t, l, j, projectedCredsSecret(nil))
		got, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey, nil)
		if err != nil {
			t.Fatalf("createRedisBackupArtifact: %v", err)
		}
		if got.Status.Artifact == nil || got.Status.Artifact.SizeBytes != 777 || got.Status.Artifact.URI != "s3://cozy-backups/"+redisTestKey {
			t.Fatalf("Artifact = %#v", got.Status.Artifact)
		}
	})

	t.Run("an existing Backup is returned without probing", func(t *testing.T) {
		existing := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"}}
		l := &fakeS3Lister{statSize: 777}
		r := redisReconciler(t, l, j, existing, projectedCredsSecret(nil))
		if _, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey, nil); err != nil {
			t.Fatalf("createRedisBackupArtifact: %v", err)
		}
		if l.calls != 0 {
			t.Errorf("S3 probed %d times for a Backup that already exists", l.calls)
		}
	})
}

// TestReconcileRedis_ArtifactUsesTheJobTrust pins the wiring from the
// completed backup Job to the probe: the s3-ca volume of the pod the Job
// ran must reach the S3 connection.
func TestReconcileRedis_ArtifactUsesTheJobTrust(t *testing.T) {
	app := newRedisApp("cache", "tenant-test")
	strategy := newRedisStrategy("redis-strategy")
	now := metav1.Now()
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bj", Namespace: "tenant-test"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef:  newRedisAppRef("cache"),
			BackupClassName: "cozy-default",
		},
		Status: backupsv1alpha1.BackupJobStatus{StartedAt: &now, Phase: backupsv1alpha1.BackupJobPhaseRunning},
	}
	completedK8sJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameForBackupJob(backupJob),
			Namespace: backupJob.Namespace,
			Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      backupJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: backupJob.Namespace,
			},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: *redisPodSpec("s3-endpoint-ca", "")}},
		Status: batchv1.JobStatus{
			CompletionTime: &now,
			Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	creds := projectedCredsSecret(nil)
	creds.Namespace = "tenant-test"
	ca := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-test", Name: "s3-endpoint-ca"},
		Data:       map[string][]byte{"ca.crt": []byte("CA")},
	}
	resolved := newRedisResolved("redis-strategy", map[string]string{"bucketName": "redis-bucket"})

	r, _ := newRedisTestEnv(t, app, clientfake.NewClientBuilder().WithObjects(backupJob, strategy, completedK8sJob, creds, ca))
	l := &fakeS3Lister{statSize: 512}
	r.CredentialsConfig.TargetSecretName = "cozy-backups-creds"
	r.newS3ObjectLister = func(conn s3Connection) (s3ObjectLister, error) {
		l.conn = conn
		return l, nil
	}
	ctx := context.Background()

	if _, err := r.reconcileRedis(ctx, backupJob, resolved); err != nil {
		t.Fatalf("reconcileRedis() error = %v", err)
	}
	if string(l.conn.CAPEM) != "CA" {
		t.Errorf("connection CA = %q, want the s3-ca volume's ca.crt", l.conn.CAPEM)
	}

	updated := &backupsv1alpha1.BackupJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(backupJob), updated); err != nil {
		t.Fatalf("get backupjob: %v", err)
	}
	if updated.Status.BackupRef == nil {
		t.Fatalf("expected BackupRef set")
	}
	created := &backupsv1alpha1.Backup{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: backupJob.Namespace, Name: updated.Status.BackupRef.Name}, created); err != nil {
		t.Fatalf("get Backup: %v", err)
	}
	if created.Status.Artifact == nil || created.Status.Artifact.SizeBytes != 512 {
		t.Errorf("Artifact = %#v, want SizeBytes 512", created.Status.Artifact)
	}
}

func TestCreateRedisBackupArtifact_RetryCheck(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "Redis", Name: "cache"},
		},
	}

	t.Run("a failed existence check creates the Backup without probing", func(t *testing.T) {
		l := &fakeS3Lister{statSize: 777}
		r := redisReconciler(t, l, j, projectedCredsSecret(nil))
		r.APIReader = failingReads(t, newMariaDBStrategyTestClient(t))
		got, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey, nil)
		if err != nil || got == nil {
			t.Fatalf("createRedisBackupArtifact = %v, %v; a transient read must not fail the backup", got, err)
		}
		if l.calls != 0 {
			t.Errorf("S3 probed %d times after the existence check failed", l.calls)
		}
	})

	t.Run("an existing Backup that cannot be read back is retried", func(t *testing.T) {
		backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj-1"}}
		store := newMariaDBStrategyTestClient(t, j, backup, projectedCredsSecret(nil))
		r := redisReconciler(t, &fakeS3Lister{statSize: 777})
		r.Client, r.APIReader = staleCache(t, store), failingReads(t, store)
		if _, err := r.createRedisBackupArtifact(context.Background(), j, &ResolvedBackupConfig{}, redisTestKey, nil); !errors.Is(err, errBackupNotReadBack) {
			t.Errorf("err = %v, want errBackupNotReadBack", err)
		}
	})
}
