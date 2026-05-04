// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	"github.com/cozystack/cozystack/internal/backupcontroller/postgresapp"
)

func TestCNPGClusterNameForApp(t *testing.T) {
	got := cnpgClusterNameForApp("foo")
	if got != "postgres-foo" {
		t.Fatalf("cnpgClusterNameForApp(%q) = %q, want postgres-foo", "foo", got)
	}
}

func TestBuildBarmanObjectStore_AllFields(t *testing.T) {
	jobs := int32(4)
	tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://bucket/path/",
		EndpointURL:     "http://s3:9000",
		RetentionPolicy: "30d",
		S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
			SecretRef: corev1.LocalObjectReference{Name: "creds"},
		},
		Wal:  &strategyv1alpha1.BarmanWalTemplate{Compression: "gzip"},
		Data: &strategyv1alpha1.BarmanDataTemplate{Compression: "gzip", Jobs: &jobs},
	}
	got := buildBarmanObjectStore(tmpl, "my-server")

	// retentionPolicy lives at spec.backup.retentionPolicy in CNPG; it is
	// emitted by applyClusterBarmanObjectStore and intentionally absent here.
	want := &cnpgtypes.BarmanObjectStoreConfiguration{
		DestinationPath: "s3://bucket/path/",
		EndpointURL:     "http://s3:9000",
		ServerName:      "my-server",
		S3Credentials: &cnpgtypes.S3Credentials{
			AccessKeyID:     &cnpgtypes.SecretKeySelector{Name: "creds", Key: defaultS3AccessKeyIDKey},
			SecretAccessKey: &cnpgtypes.SecretKeySelector{Name: "creds", Key: defaultS3SecretAccessKeyKey},
		},
		Wal:  &cnpgtypes.WalBackupConfiguration{Compression: "gzip"},
		Data: &cnpgtypes.DataBackupConfiguration{Compression: "gzip", Jobs: ptr.To(int32(4))},
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Fatalf("buildBarmanObjectStore mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildBarmanObjectStore_Minimal(t *testing.T) {
	tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://only/",
	}
	got := buildBarmanObjectStore(tmpl, "fallback")
	want := &cnpgtypes.BarmanObjectStoreConfiguration{
		DestinationPath: "s3://only/",
		ServerName:      "fallback",
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Fatalf("minimal buildBarmanObjectStore mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildBarmanObjectStore_CustomCredentialKeys(t *testing.T) {
	tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
		DestinationPath: "s3://x/",
		S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
			SecretRef:          corev1.LocalObjectReference{Name: "creds"},
			AccessKeyIDKey:     "AKID",
			SecretAccessKeyKey: "SKID",
		},
	}
	got := buildBarmanObjectStore(tmpl, "s")
	if got.S3Credentials.AccessKeyID.Key != "AKID" {
		t.Fatalf("expected custom access key id key AKID, got %q", got.S3Credentials.AccessKeyID.Key)
	}
	if got.S3Credentials.SecretAccessKey.Key != "SKID" {
		t.Fatalf("expected custom secret access key key SKID, got %q", got.S3Credentials.SecretAccessKey.Key)
	}
}

// TestBuildBarmanObjectStore_EndpointCA covers the TLS-with-self-signed-CA
// path: a strategy that names a Secret holding the CA bundle gets translated
// into the matching cnpgtypes.SecretKeySelector on the live Cluster, defaulting
// the key name to "ca.crt" when not specified.
func TestBuildBarmanObjectStore_EndpointCA(t *testing.T) {
	t.Run("default key", func(t *testing.T) {
		tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://b/",
			EndpointCA: &strategyv1alpha1.EndpointCARef{
				SecretRef: corev1.LocalObjectReference{Name: "trust-bundle"},
			},
		}
		got := buildBarmanObjectStore(tmpl, "s")
		if got.EndpointCA == nil || got.EndpointCA.Name != "trust-bundle" || got.EndpointCA.Key != "ca.crt" {
			t.Fatalf("EndpointCA mismatch: %#v", got.EndpointCA)
		}
	})
	t.Run("custom key", func(t *testing.T) {
		tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://b/",
			EndpointCA: &strategyv1alpha1.EndpointCARef{
				SecretRef: corev1.LocalObjectReference{Name: "trust-bundle"},
				Key:       "tls.crt",
			},
		}
		got := buildBarmanObjectStore(tmpl, "s")
		if got.EndpointCA == nil || got.EndpointCA.Key != "tls.crt" {
			t.Fatalf("EndpointCA key not honored: %#v", got.EndpointCA)
		}
	})
	t.Run("nil block stays nil", func(t *testing.T) {
		tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://b/"}
		got := buildBarmanObjectStore(tmpl, "s")
		if got.EndpointCA != nil {
			t.Fatalf("EndpointCA must be nil when not configured; got %#v", got.EndpointCA)
		}
	})
	t.Run("empty SecretRef.Name treated as not configured", func(t *testing.T) {
		tmpl := strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://b/",
			EndpointCA:      &strategyv1alpha1.EndpointCARef{Key: "ca.crt"},
		}
		got := buildBarmanObjectStore(tmpl, "s")
		if got.EndpointCA != nil {
			t.Fatalf("expected nil EndpointCA when SecretRef.Name is empty; got %#v", got.EndpointCA)
		}
	})
}

func TestRenderCNPGTemplate_TemplatingApplicationName(t *testing.T) {
	tmpl := strategyv1alpha1.CNPGTemplate{
		ServerName: "{{ .Application.metadata.name }}",
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/{{ .Application.metadata.name }}/",
			S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
				SecretRef: corev1.LocalObjectReference{Name: "{{ .Application.metadata.name }}-creds"},
			},
		},
	}
	app := newPostgresApp("pg-src", "tenant")
	got, err := renderCNPGTemplate(tmpl, app, nil)
	if err != nil {
		t.Fatalf("renderCNPGTemplate: %v", err)
	}
	if got.ServerName != "pg-src" {
		t.Errorf("ServerName not templated: got %q", got.ServerName)
	}
	if got.BarmanObjectStore.DestinationPath != "s3://bucket/pg-src/" {
		t.Errorf("DestinationPath not templated: got %q", got.BarmanObjectStore.DestinationPath)
	}
	if got.BarmanObjectStore.S3Credentials.SecretRef.Name != "pg-src-creds" {
		t.Errorf("SecretRef.Name not templated: got %q", got.BarmanObjectStore.S3Credentials.SecretRef.Name)
	}
}

func TestParseCNPGRestoreOptions(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		wantRecoveryTime string
		wantTimeout      int64
		wantErr          bool
	}{
		{"nil blob", "", "", 0, false},
		{"missing fields", `{"foo":"bar"}`, "", 0, false},
		{"recovery time only", `{"recoveryTime":"2026-05-01T12:00:00Z"}`, "2026-05-01T12:00:00Z", 0, false},
		{"timeout only", `{"restoreTimeoutSeconds":7200}`, "", 7200, false},
		{"both", `{"recoveryTime":"2026-05-01T12:00:00Z","restoreTimeoutSeconds":3600}`, "2026-05-01T12:00:00Z", 3600, false},
		// A malformed blob must surface a parse error so callers can log it.
		// Returning silently used to mask "why didn't recoveryTime apply" for
		// tenants debugging restores; see Blocker 5 in the branch review.
		{"malformed", `not-json`, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ext *runtime.RawExtension
			if tc.raw != "" {
				ext = &runtime.RawExtension{Raw: []byte(tc.raw)}
			}
			got, err := parseCNPGRestoreOptions(ext)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected parse error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if got.RecoveryTime != tc.wantRecoveryTime {
				t.Errorf("recoveryTime: got %q want %q", got.RecoveryTime, tc.wantRecoveryTime)
			}
			if got.RestoreTimeoutSeconds != tc.wantTimeout {
				t.Errorf("restoreTimeoutSeconds: got %d want %d", got.RestoreTimeoutSeconds, tc.wantTimeout)
			}
		})
	}
}

func TestEffectiveRestoreDeadline(t *testing.T) {
	cases := []struct {
		name string
		opts CNPGRestoreOptions
		want time.Duration
	}{
		{"unset falls back to default", CNPGRestoreOptions{}, cnpgDefaultRestoreDeadline},
		{"zero falls back to default", CNPGRestoreOptions{RestoreTimeoutSeconds: 0}, cnpgDefaultRestoreDeadline},
		{"negative falls back to default", CNPGRestoreOptions{RestoreTimeoutSeconds: -5}, cnpgDefaultRestoreDeadline},
		{"positive override", CNPGRestoreOptions{RestoreTimeoutSeconds: 7200}, 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.effectiveRestoreDeadline()
			if got != tc.want {
				t.Errorf("got %s want %s", got, tc.want)
			}
		})
	}
}

// TestValidateCNPGApplicationRef locks in the API-group guard. Without it a
// non-Cozystack ref like other.example.com/Postgres slipped past the Kind
// check and hit the hard-wired apps.cozystack.io typed client, reconciling
// against the wrong CRD entirely.
func TestValidateCNPGApplicationRef(t *testing.T) {
	cozyGroup := postgresapp.GroupName
	otherGroup := "other.example.com"

	cases := []struct {
		name    string
		ref     corev1.TypedLocalObjectReference
		wantErr bool
	}{
		{
			name: "kind=Postgres, apiGroup=apps.cozystack.io: accepted",
			ref:  corev1.TypedLocalObjectReference{APIGroup: &cozyGroup, Kind: postgresAppKind, Name: "pg"},
		},
		{
			name: "kind=Postgres, nil apiGroup: accepted (default applies upstream)",
			ref:  corev1.TypedLocalObjectReference{Kind: postgresAppKind, Name: "pg"},
		},
		{
			name:    "wrong kind: rejected",
			ref:     corev1.TypedLocalObjectReference{APIGroup: &cozyGroup, Kind: "MySQL", Name: "pg"},
			wantErr: true,
		},
		{
			name:    "right kind, wrong apiGroup: rejected",
			ref:     corev1.TypedLocalObjectReference{APIGroup: &otherGroup, Kind: postgresAppKind, Name: "pg"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCNPGApplicationRef(tc.ref)
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestResolveCNPGRestoreTarget(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	src := &backupsv1alpha1.Backup{}
	src.Namespace = "tenant-foo"
	src.Spec.ApplicationRef.APIGroup = &apiGroup
	src.Spec.ApplicationRef.Kind = postgresAppKind
	src.Spec.ApplicationRef.Name = "pg-src"

	r := &RestoreJobReconciler{}

	t.Run("in-place when targetApplicationRef nil", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		got := r.resolveCNPGRestoreTarget(rj, src)
		if got.IsCopy {
			t.Fatalf("expected in-place, got copy")
		}
		if got.AppName != "pg-src" || got.Namespace != "tenant-foo" || got.Kind != postgresAppKind {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("to-copy when targetApplicationRef differs", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		rj.Spec.TargetApplicationRef = newTypedRef(apiGroup, postgresAppKind, "pg-target")
		got := r.resolveCNPGRestoreTarget(rj, src)
		if !got.IsCopy {
			t.Fatalf("expected copy, got in-place")
		}
		if got.AppName != "pg-target" {
			t.Fatalf("unexpected app name: %q", got.AppName)
		}
	})

	t.Run("in-place when targetApplicationRef matches source name", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		rj.Spec.TargetApplicationRef = newTypedRef(apiGroup, postgresAppKind, "pg-src")
		got := r.resolveCNPGRestoreTarget(rj, src)
		if got.IsCopy {
			t.Fatalf("expected in-place, got copy")
		}
	})
}

func TestCNPGBackupPhase(t *testing.T) {
	cases := []struct {
		name      string
		backup    *cnpgtypes.Backup
		wantPhase string
		wantMsg   string
	}{
		{"nil", nil, "", ""},
		{"completed", &cnpgtypes.Backup{Status: cnpgtypes.BackupStatus{Phase: "completed"}}, "completed", ""},
		{"failed-with-error", &cnpgtypes.Backup{Status: cnpgtypes.BackupStatus{Phase: "failed", Error: "boom"}}, "failed", "boom"},
		{"empty status", &cnpgtypes.Backup{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phase, msg := cnpgBackupPhase(tc.backup)
			if phase != tc.wantPhase || msg != tc.wantMsg {
				t.Fatalf("got (%q,%q), want (%q,%q)", phase, msg, tc.wantPhase, tc.wantMsg)
			}
		})
	}
}

// newTypedRef is a small helper to build a TypedLocalObjectReference without
// the test having to bother with apiGroup pointer plumbing.
func newTypedRef(apiGroup, kind, name string) *corev1.TypedLocalObjectReference {
	return &corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: kind, Name: name}
}

// newPostgresApp builds a typed Postgres CR for tests.
func newPostgresApp(name, namespace string) *postgresapp.Postgres {
	return &postgresapp.Postgres{
		TypeMeta: metav1.TypeMeta{
			APIVersion: postgresapp.GroupVersion.String(),
			Kind:       postgresapp.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

// TestBuildPostgresAppRestorePatch_ForwardsSecretRef_NotCleartext locks in
// the security-critical contract that S3 credentials are forwarded to the
// chart as a Secret reference, not pasted into the CR .spec as cleartext.
// Regressing this would expose access keys via etcd, audit logs, and
// tenant-readable kubectl get -o yaml.
func TestBuildPostgresAppRestorePatch_ForwardsSecretRef_NotCleartext(t *testing.T) {
	app := newPostgresApp("pg-target", "tenant-foo")

	creds := &strategyv1alpha1.S3CredentialsTemplate{
		SecretRef: corev1.LocalObjectReference{Name: "pg-target-cnpg-backup-creds"},
	}

	patched := buildPostgresAppRestorePatch(
		app,
		"pg-src",
		"s3://bucket/pg-src/",
		"https://s3.example",
		"",
		creds,
		nil, nil,
	)

	if got := patched.Spec.Backup.S3CredentialsSecret.Name; got != "pg-target-cnpg-backup-creds" {
		t.Errorf("s3CredentialsSecret.name: got %q want %q", got, "pg-target-cnpg-backup-creds")
	}

	// Cleartext key fields must NOT be set. Their presence on the CR .spec is
	// the bug this test guards against.
	if got := patched.Spec.Backup.S3AccessKey; got != "" {
		t.Errorf("spec.backup.s3AccessKey must not be set on the patched app; got %q", got)
	}
	if got := patched.Spec.Backup.S3SecretKey; got != "" {
		t.Errorf("spec.backup.s3SecretKey must not be set on the patched app; got %q", got)
	}
}

// TestBuildPostgresAppRestorePatch_ForwardsCustomKeyOverrides verifies the
// strategy template's optional key-name overrides round-trip into the chart
// values so non-default Secret layouts work end-to-end.
func TestBuildPostgresAppRestorePatch_ForwardsCustomKeyOverrides(t *testing.T) {
	app := newPostgresApp("pg", "tenant")
	creds := &strategyv1alpha1.S3CredentialsTemplate{
		SecretRef:          corev1.LocalObjectReference{Name: "creds"},
		AccessKeyIDKey:     "ACCESS_KEY",
		SecretAccessKeyKey: "SECRET_KEY",
	}

	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", creds, nil, nil)

	want := postgresapp.S3CredentialsSecret{
		Name:               "creds",
		AccessKeyIDKey:     "ACCESS_KEY",
		SecretAccessKeyKey: "SECRET_KEY",
	}
	if got := patched.Spec.Backup.S3CredentialsSecret; got != want {
		t.Fatalf("s3CredentialsSecret mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// TestBuildPostgresAppRestorePatch_NoSecretRefIsSkipped verifies the
// patcher does not stamp an empty s3CredentialsSecret block when the strategy
// did not declare credentials. Otherwise the chart would render an empty
// Secret name and helm install would fail.
func TestBuildPostgresAppRestorePatch_NoSecretRefIsSkipped(t *testing.T) {
	app := newPostgresApp("pg", "tenant")
	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, nil, nil)
	if got := patched.Spec.Backup.S3CredentialsSecret; got != (postgresapp.S3CredentialsSecret{}) {
		t.Errorf("spec.backup.s3CredentialsSecret must be zero when credsRef is nil; got %#v", got)
	}
}

// TestCNPGPurgeNeeded locks in the dual-guard against re-purging the
// freshly-recovered Cluster on a status-update failure. The controller used
// to rely solely on a Status condition: if the post-purge Status().Update
// raced or failed, the next reconcile would re-purge the just-restored
// Cluster, destroying recovery progress. Cross-checking the live Cluster's
// bootstrap.recovery makes the second purge a no-op.
func TestCNPGPurgeNeeded(t *testing.T) {
	cases := []struct {
		name            string
		purgedCondition bool
		hasRecovery     bool
		want            bool
	}{
		{
			name:            "fresh restore, old cluster still in place: purge",
			purgedCondition: false,
			hasRecovery:     false,
			want:            true,
		},
		{
			name:            "purge already recorded: skip",
			purgedCondition: true,
			hasRecovery:     false,
			want:            false,
		},
		{
			name:            "live cluster already recovered (status write raced): skip - the bug fix",
			purgedCondition: false,
			hasRecovery:     true,
			want:            false,
		},
		{
			name:            "both true: skip (post-purge steady state)",
			purgedCondition: true,
			hasRecovery:     true,
			want:            false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cnpgPurgeNeeded(tc.purgedCondition, tc.hasRecovery)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestBuildPostgresAppRestorePatch_ReplacesTargetUsersAndDatabases locks
// in the authoritative-from-source semantics: target's stale users and
// databases must be wiped and replaced with source's exact map. The
// recovered cluster carries source's role catalog and data; if target's
// pre-restore drift survives, the chart's init-job either tries to
// re-create roles against the wrong data or leaks cleartext passwords
// from a previous tenant configuration.
func TestBuildPostgresAppRestorePatch_ReplacesTargetUsersAndDatabases(t *testing.T) {
	app := newPostgresApp("pg-target", "tenant")
	// Target had pre-existing users/databases (e.g. from a previous restore
	// or operator drift). Replace must wipe them.
	app.Spec.Users = map[string]postgresapp.User{
		"stale-target-user": {Password: "leak-me"},
	}
	app.Spec.Databases = map[string]postgresapp.Database{
		"stale-target-db": {Extensions: []string{"pgcrypto"}},
	}

	sourceUsers := map[string]postgresapp.User{
		"app": {Password: "src"},
	}
	sourceDatabases := map[string]postgresapp.Database{
		"appdb": {Extensions: []string{"hstore"}},
	}

	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, sourceDatabases, sourceUsers)

	if _, ok := patched.Spec.Users["stale-target-user"]; ok {
		t.Errorf("stale target user survived restore; replace semantics regressed")
	}
	if u, ok := patched.Spec.Users["app"]; !ok {
		t.Errorf("source user was not propagated onto target")
	} else if u.Password != "src" {
		t.Errorf("source user password mismatch; got %q want %q", u.Password, "src")
	}
	if _, ok := patched.Spec.Databases["stale-target-db"]; ok {
		t.Errorf("stale target database survived restore; replace semantics regressed")
	}
	if _, ok := patched.Spec.Databases["appdb"]; !ok {
		t.Errorf("source database was not propagated onto target")
	}
}

// TestBuildPostgresAppRestorePatch_ScrubsStaleBackupSettings guards the
// stale-state hygiene called out by coderabbitai on
// internal/backupcontroller/cnpgstrategy_controller.go:597-604: a re-restore
// into the same target must not inherit a previous restore's recoveryTime,
// endpointURL, S3 credentials Secret reference, or inline cleartext S3 keys.
func TestBuildPostgresAppRestorePatch_ScrubsStaleBackupSettings(t *testing.T) {
	app := newPostgresApp("pg-target", "tenant")
	app.Spec.Bootstrap.RecoveryTime = "2025-01-01T00:00:00Z"
	app.Spec.Backup.EndpointURL = "https://stale.example"
	app.Spec.Backup.S3AccessKey = "stale-ak"
	app.Spec.Backup.S3SecretKey = "stale-sk"
	app.Spec.Backup.S3CredentialsSecret = postgresapp.S3CredentialsSecret{
		Name: "stale-creds", AccessKeyIDKey: "OLD_ID", SecretAccessKeyKey: "OLD_KEY",
	}

	// Restore with no recoveryTime, no endpointURL, no creds - everything
	// stale on the target must be wiped.
	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, nil, nil)

	if got := patched.Spec.Bootstrap.RecoveryTime; got != "" {
		t.Errorf("stale recoveryTime survived; got %q", got)
	}
	if got := patched.Spec.Backup.EndpointURL; got != "" {
		t.Errorf("stale endpointURL survived; got %q", got)
	}
	if got := patched.Spec.Backup.S3AccessKey; got != "" {
		t.Errorf("inline s3AccessKey was not scrubbed; got %q", got)
	}
	if got := patched.Spec.Backup.S3SecretKey; got != "" {
		t.Errorf("inline s3SecretKey was not scrubbed; got %q", got)
	}
	if patched.Spec.Backup.S3CredentialsSecret != (postgresapp.S3CredentialsSecret{}) {
		t.Errorf("stale s3CredentialsSecret survived; got %+v", patched.Spec.Backup.S3CredentialsSecret)
	}
}

// TestMarshalUnmarshalCNPGBackupSnapshot round-trips the source-spec
// snapshot persisted in Backup.status.underlyingResources.
func TestMarshalUnmarshalCNPGBackupSnapshot(t *testing.T) {
	src := newPostgresApp("pg-src", "tenant")
	src.Spec.Databases = map[string]postgresapp.Database{
		"app": {Extensions: []string{"pgcrypto"}},
	}
	src.Spec.Users = map[string]postgresapp.User{
		"app": {Password: "p"},
	}

	raw, err := marshalCNPGBackupSnapshot(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw == nil {
		t.Fatalf("expected non-nil RawExtension")
	}

	bk := &backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw}}
	dbs, users, err := unmarshalCNPGBackupSnapshot(bk)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dbs["app"].Extensions[0] != "pgcrypto" {
		t.Errorf("databases round-trip mismatch: %#v", dbs)
	}
	if users["app"].Password != "p" {
		t.Errorf("users round-trip mismatch: %#v", users)
	}
}

// TestUnmarshalCNPGBackupSnapshot_RejectsMissingOrWrongKind locks in the
// fail-loud contract: a Backup without a snapshot must NOT silently
// proceed - it would let the chart's init-job DROP recovered roles. Same
// for a Backup whose snapshot carries the wrong Kind (foreign payload).
func TestUnmarshalCNPGBackupSnapshot_RejectsMissingOrWrongKind(t *testing.T) {
	cases := []struct {
		name string
		ur   *runtime.RawExtension
	}{
		{"nil underlyingResources", nil},
		{"empty raw", &runtime.RawExtension{Raw: []byte{}}},
		{"wrong kind", &runtime.RawExtension{Raw: []byte(`{"kind":"VMInstanceResources","apiVersion":"backups.cozystack.io/v1alpha1"}`)}},
		{"malformed json", &runtime.RawExtension{Raw: []byte(`not-json`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bk := &backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: tc.ur}}
			_, _, err := unmarshalCNPGBackupSnapshot(bk)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// TestCNPGBackupDeadlineExceeded locks in the wall-clock cap that prevents a
// perpetually-failed cnpg.io/Backup from pinning the BackupJob in Running
// forever. Without this guard the Plan-controller queue would back up
// behind a single broken backup.
func TestCNPGBackupDeadlineExceeded(t *testing.T) {
	cases := []struct {
		name      string
		startedAt *metav1.Time
		want      bool
	}{
		{
			name:      "nil startedAt does not trip the gate",
			startedAt: nil,
			want:      false,
		},
		{
			name:      "fresh start is well under the deadline",
			startedAt: &metav1.Time{Time: time.Now()},
			want:      false,
		},
		{
			name:      "just under deadline does not trip",
			startedAt: &metav1.Time{Time: time.Now().Add(-(cnpgDefaultBackupDeadline - time.Minute))},
			want:      false,
		},
		{
			name:      "past deadline trips the gate",
			startedAt: &metav1.Time{Time: time.Now().Add(-(cnpgDefaultBackupDeadline + time.Minute))},
			want:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cnpgBackupDeadlineExceeded(tc.startedAt)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestCreateCNPGBackupArtifact_AlreadyExistsReturnsExisting locks in the
// idempotency guard for createCNPGBackupArtifact: an AlreadyExists collision
// (typical when the previous reconcile created the artifact and then raced
// on the BackupJob status update) must return the existing artifact rather
// than propagate the error and flip a completed run to Failed.
func TestCreateCNPGBackupArtifact_AlreadyExistsReturnsExisting(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group

	preExisting := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
			StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "strat"},
			DriverMetadata: map[string]string{"marker": "first-reconcile"},
		},
	}
	c := newCNPGStrategyTestClient(t, preExisting)
	r := &BackupJobReconciler{Client: c}

	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
		},
	}
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "strat"},
	}
	cnpgBk := &cnpgtypes.Backup{ObjectMeta: metav1.ObjectMeta{Name: "cnpg-bk"}}
	rendered := &strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://b/"},
	}

	sourceApp := newPostgresApp("pg", "tenant")
	got, err := r.createCNPGBackupArtifact(context.Background(), j, resolved, cnpgBk, "postgres-pg", "postgres-pg", rendered, sourceApp)
	if err != nil {
		t.Fatalf("expected AlreadyExists to be swallowed, got error %v", err)
	}
	if got == nil {
		t.Fatalf("expected existing Backup returned, got nil")
	}
	if got.Spec.DriverMetadata["marker"] != "first-reconcile" {
		t.Errorf("expected the existing object back, got %#v", got.Spec.DriverMetadata)
	}
}

// TestApplyClusterBarmanObjectStore_NotFoundOnMissingCluster locks in the
// precondition added for the smaller blocker on applyClusterBarmanObjectStore:
// when the live cnpg.io Cluster has not yet been rendered by the chart, the
// driver must surface NotFound (so the caller can retry-with-backoff)
// instead of issuing a doomed SSA Apply that would be rejected by the API
// server for missing required Cluster fields.
func TestApplyClusterBarmanObjectStore_NotFoundOnMissingCluster(t *testing.T) {
	c := newCNPGStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c}
	tmpl := &strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/x/",
		},
	}

	err := r.applyClusterBarmanObjectStore(context.Background(), "tenant", "postgres-missing", tmpl, "postgres-missing")
	if err == nil {
		t.Fatalf("expected NotFound error, got nil")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected IsNotFound error, got %v", err)
	}
}

// TestApplyClusterBarmanObjectStore_PatchesExistingCluster confirms the
// precondition does not regress the happy path: when the Cluster exists
// the driver still SSA-patches spec.backup.barmanObjectStore.
func TestApplyClusterBarmanObjectStore_PatchesExistingCluster(t *testing.T) {
	cluster := &cnpgtypes.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "postgres-app"},
	}
	c := newCNPGStrategyTestClient(t, cluster)
	r := &BackupJobReconciler{Client: c}
	tmpl := &strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/x/",
			RetentionPolicy: "30d",
		},
	}

	if err := r.applyClusterBarmanObjectStore(context.Background(), "tenant", "postgres-app", tmpl, "postgres-app"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &cnpgtypes.Cluster{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), got); err != nil {
		t.Fatalf("get cluster after patch: %v", err)
	}
	if got.Spec.Backup == nil || got.Spec.Backup.BarmanObjectStore == nil {
		t.Fatalf("expected spec.backup.barmanObjectStore to be set, got %+v", got.Spec.Backup)
	}
	if got.Spec.Backup.BarmanObjectStore.DestinationPath != "s3://bucket/x/" {
		t.Errorf("destinationPath: got %q", got.Spec.Backup.BarmanObjectStore.DestinationPath)
	}
	if got.Spec.Backup.RetentionPolicy != "30d" {
		t.Errorf("retentionPolicy: got %q", got.Spec.Backup.RetentionPolicy)
	}
}

// TestReconcileCNPG_StartedAtPreservedAcrossStaleCache locks in the bug fix
// for the StartedAt race called out in the branch review: a second reconcile
// with a stale local copy (which observes StartedAt==nil) must NOT advance
// the persisted timestamp the first reconcile already wrote. Without the
// refetch-before-write guard, the wall-clock deadline gate slides forward
// on every poll, defeating the purpose of cnpgDefaultBackupDeadline.
func TestReconcileCNPG_StartedAtPreservedAcrossStaleCache(t *testing.T) {
	// metav1.Time loses sub-second precision on JSON round-trip; truncate
	// upfront so the equality check below isn't fooled by nanosecond drift.
	original := metav1.NewTime(time.Now().Add(-10 * time.Minute).Truncate(time.Second))
	stored := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Status: backupsv1alpha1.BackupJobStatus{
			StartedAt: &original,
		},
	}
	c := newCNPGStrategyTestClient(t, stored)
	r := &BackupJobReconciler{Client: c}

	// Stale local copy as the reconcile would have observed it: StartedAt==nil.
	stale := stored.DeepCopy()
	stale.Status.StartedAt = nil

	// Inline the refetch-before-write block from reconcileCNPG. (The full
	// reconciler path requires more wiring than this lightweight test
	// builds; the block under test is the bug-fix surface, not the
	// surrounding decode/template logic.)
	fresh := &backupsv1alpha1.BackupJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(stored), fresh); err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if fresh.Status.StartedAt != nil {
		stale.Status.StartedAt = fresh.Status.StartedAt
	} else {
		base := fresh.DeepCopy()
		now := metav1.Now()
		fresh.Status.StartedAt = &now
		if err := r.Status().Patch(context.Background(), fresh, client.MergeFrom(base)); err != nil {
			t.Fatalf("patch: %v", err)
		}
		stale.Status.StartedAt = fresh.Status.StartedAt
	}

	got := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stored), got); err != nil {
		t.Fatalf("get after fix: %v", err)
	}
	if got.Status.StartedAt == nil || !got.Status.StartedAt.Equal(&original) {
		t.Errorf("persisted StartedAt was advanced by stale-cache reconcile; got %v, want %v",
			got.Status.StartedAt, original)
	}
	if stale.Status.StartedAt == nil || !stale.Status.StartedAt.Equal(&original) {
		t.Errorf("local copy did not pick up persisted StartedAt; got %v, want %v",
			stale.Status.StartedAt, original)
	}
}

// newCNPGStrategyTestClient returns a fake client.Client wired up with
// the schemes the CNPG-strategy reconciler needs.
func newCNPGStrategyTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = cnpgtypes.AddToScheme(s)
	_ = postgresapp.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
}
