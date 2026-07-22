// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

// TestRenderCNPGTemplate_RoundTripsParametersThroughSnapshot locks in the
// fix for review blocker 1 (#4387688473): a strategy template that uses
// {{ .Parameters.foo }} for a Secret reference / endpointCA / key name
// must yield the same rendered value at restore time as it did at backup
// time. Before the fix, the restore reconciler called renderCNPGTemplate
// with parameters=nil, so any .Parameters reference rendered "<no value>"
// and the patched target Postgres app got broken Secret references that
// CNPG would later reject.
//
// Verifies the contract end-to-end: render-backup -> marshal snapshot ->
// unmarshal snapshot -> render-restore. Also asserts the buggy path
// (parameters=nil at restore) produces a recognisable broken value, so
// a regression that drops the persistence side gets caught here too.
func TestRenderCNPGTemplate_RoundTripsParametersThroughSnapshot(t *testing.T) {
	tmpl := strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/{{ .Application.metadata.name }}/",
			S3Credentials: &strategyv1alpha1.S3CredentialsTemplate{
				SecretRef: corev1.LocalObjectReference{Name: "{{ .Parameters.credsSecret }}"},
			},
			EndpointCA: &strategyv1alpha1.EndpointCARef{
				SecretRef: corev1.LocalObjectReference{Name: "{{ .Parameters.caSecret }}"},
				Key:       "ca.crt",
			},
		},
	}
	app := newPostgresApp("pg-src", "tenant")
	parameters := map[string]string{
		"credsSecret": "tenant-shared-creds",
		"caSecret":    "tenant-shared-ca",
	}

	// Backup-time render: real values flow through.
	rendBackup, err := renderCNPGTemplate(tmpl, app, parameters)
	if err != nil {
		t.Fatalf("backup-time render: %v", err)
	}
	if rendBackup.BarmanObjectStore.S3Credentials.SecretRef.Name != "tenant-shared-creds" {
		t.Fatalf("backup-time SecretRef.Name: got %q want %q",
			rendBackup.BarmanObjectStore.S3Credentials.SecretRef.Name, "tenant-shared-creds")
	}
	if rendBackup.BarmanObjectStore.EndpointCA.SecretRef.Name != "tenant-shared-ca" {
		t.Fatalf("backup-time EndpointCA.SecretRef.Name: got %q want %q",
			rendBackup.BarmanObjectStore.EndpointCA.SecretRef.Name, "tenant-shared-ca")
	}

	// Round-trip parameters through Backup.status.underlyingResources.
	raw, err := marshalCNPGBackupSnapshot(app, parameters)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	bk := &backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw}}
	_, _, gotParams, err := unmarshalCNPGBackupSnapshot(bk)
	if err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	// Restore-time render with retrieved parameters: same values out.
	rendRestore, err := renderCNPGTemplate(tmpl, app, gotParams)
	if err != nil {
		t.Fatalf("restore-time render: %v", err)
	}
	if rendRestore.BarmanObjectStore.S3Credentials.SecretRef.Name != "tenant-shared-creds" {
		t.Errorf("restore-time SecretRef.Name: got %q want %q",
			rendRestore.BarmanObjectStore.S3Credentials.SecretRef.Name, "tenant-shared-creds")
	}
	if rendRestore.BarmanObjectStore.EndpointCA.SecretRef.Name != "tenant-shared-ca" {
		t.Errorf("restore-time EndpointCA.SecretRef.Name: got %q want %q",
			rendRestore.BarmanObjectStore.EndpointCA.SecretRef.Name, "tenant-shared-ca")
	}

	// Buggy path: rendering at restore with parameters=nil produces
	// "<no value>". Catching this here means a future change that
	// silently drops parameters off the snapshot fails this test before
	// it ships a broken patch to a real Postgres app.
	rendBroken, err := renderCNPGTemplate(tmpl, app, nil)
	if err != nil {
		t.Fatalf("nil-params render: %v", err)
	}
	if rendBroken.BarmanObjectStore.S3Credentials.SecretRef.Name != "<no value>" {
		t.Errorf("expected broken render for nil params (so the test catches regressions), got %q",
			rendBroken.BarmanObjectStore.S3Credentials.SecretRef.Name)
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

// TestEffectiveWALArchiveDeadline mirrors TestEffectiveRestoreDeadline for
// the dedicated WAL-archive gate cap. The two deadlines exist independently
// so a big-DB restore can extend RestoreTimeoutSeconds without simultaneously
// loosening the gate that catches a stuck archive_command.
func TestEffectiveWALArchiveDeadline(t *testing.T) {
	cases := []struct {
		name string
		opts CNPGRestoreOptions
		want time.Duration
	}{
		{"unset falls back to default", CNPGRestoreOptions{}, cnpgWALArchiveDeadline},
		{"zero falls back to default", CNPGRestoreOptions{WALArchiveTimeoutSeconds: 0}, cnpgWALArchiveDeadline},
		{"negative falls back to default", CNPGRestoreOptions{WALArchiveTimeoutSeconds: -5}, cnpgWALArchiveDeadline},
		{"positive override", CNPGRestoreOptions{WALArchiveTimeoutSeconds: 600}, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.effectiveWALArchiveDeadline()
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

	t.Run("source app when targetApplicationRef nil", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		got := r.resolveCNPGRestoreTarget(rj, src)
		if got.AppName != "pg-src" || got.Namespace != "tenant-foo" || got.Kind != postgresAppKind {
			t.Fatalf("unexpected target: %+v", got)
		}
	})

	t.Run("override app name when targetApplicationRef differs", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		rj.Spec.TargetApplicationRef = newTypedRef(apiGroup, postgresAppKind, "pg-target")
		got := r.resolveCNPGRestoreTarget(rj, src)
		if got.AppName != "pg-target" {
			t.Fatalf("unexpected app name: %q", got.AppName)
		}
	})

	t.Run("keep source app name when targetApplicationRef matches source", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{}
		rj.Spec.TargetApplicationRef = newTypedRef(apiGroup, postgresAppKind, "pg-src")
		got := r.resolveCNPGRestoreTarget(rj, src)
		if got.AppName != "pg-src" {
			t.Fatalf("unexpected app name: %q", got.AppName)
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
		nil,
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

	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", creds, nil, nil, nil)

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
	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, nil, nil, nil)
	if got := patched.Spec.Backup.S3CredentialsSecret; got != (postgresapp.S3CredentialsSecret{}) {
		t.Errorf("spec.backup.s3CredentialsSecret must be zero when credsRef is nil; got %#v", got)
	}
}

// TestCNPGPurgeNeeded locks in the dual-guard against re-purging the
// freshly-recovered Cluster on a status-update failure. The controller used
// to rely solely on a Status condition: if the post-purge Status().Update
// raced or failed, the next reconcile would re-purge the just-restored
// Cluster, destroying recovery progress. Cross-checking that the live
// Cluster is a *freshly-recovered* one (bootstrap.recovery + created after
// the job started) makes that second purge a no-op - while still purging a
// stale recovery Cluster left over from an earlier completed restore, so a
// repeat in-place restore is not a silent no-op.
func TestCNPGPurgeNeeded(t *testing.T) {
	cases := []struct {
		name                        string
		purgedCondition             bool
		liveClusterFreshlyRecovered bool
		want                        bool
	}{
		{
			name:                        "fresh restore, old cluster still in place: purge",
			purgedCondition:             false,
			liveClusterFreshlyRecovered: false,
			want:                        true,
		},
		{
			name:                        "purge already recorded: skip",
			purgedCondition:             true,
			liveClusterFreshlyRecovered: false,
			want:                        false,
		},
		{
			name:                        "live cluster freshly recovered by this restore (status write raced): skip",
			purgedCondition:             false,
			liveClusterFreshlyRecovered: true,
			want:                        false,
		},
		{
			name:                        "stale recovery cluster from a previous completed restore: purge (repeat-restore fix)",
			purgedCondition:             false,
			liveClusterFreshlyRecovered: false,
			want:                        true,
		},
		{
			name:                        "both true: skip (post-purge steady state)",
			purgedCondition:             true,
			liveClusterFreshlyRecovered: true,
			want:                        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cnpgPurgeNeeded(tc.purgedCondition, tc.liveClusterFreshlyRecovered)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestCNPGClusterFreshlyRecovered locks in the signal that separates a
// recovery Cluster THIS restore just produced from one left over by an
// earlier completed restore. The repeat-in-place-restore bug was exactly a
// stale recovery Cluster (created before StartedAt) being mistaken for a
// freshly-recovered one and skipping the purge.
func TestCNPGClusterFreshlyRecovered(t *testing.T) {
	started := metav1.NewTime(time.Now())
	after := metav1.NewTime(started.Add(time.Minute))
	before := metav1.NewTime(started.Add(-time.Hour))

	cases := []struct {
		name        string
		hasRecovery bool
		createdAt   *metav1.Time
		startedAt   *metav1.Time
		want        bool
	}{
		{
			name:        "no recovery bootstrap: not fresh",
			hasRecovery: false,
			createdAt:   &after,
			startedAt:   &started,
			want:        false,
		},
		{
			name:        "recovery cluster created after start (our own purge re-render): fresh",
			hasRecovery: true,
			createdAt:   &after,
			startedAt:   &started,
			want:        true,
		},
		{
			name:        "recovery cluster created before start (leftover from previous restore): not fresh",
			hasRecovery: true,
			createdAt:   &before,
			startedAt:   &started,
			want:        false,
		},
		{
			name:        "recovery cluster created exactly at start: not fresh (conservative tie -> purge)",
			hasRecovery: true,
			createdAt:   &started,
			startedAt:   &started,
			want:        false,
		},
		{
			name:        "missing cluster creation timestamp: not fresh",
			hasRecovery: true,
			createdAt:   nil,
			startedAt:   &started,
			want:        false,
		},
		{
			name:        "missing job start timestamp: not fresh",
			hasRecovery: true,
			createdAt:   &after,
			startedAt:   nil,
			want:        false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cnpgClusterFreshlyRecovered(tc.hasRecovery, tc.createdAt, tc.startedAt)
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

	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, nil, sourceDatabases, sourceUsers)

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
	app.Spec.Backup.EndpointCA = postgresapp.EndpointCA{Name: "stale-ca", Key: "stale.crt"}

	// Restore with no recoveryTime, no endpointURL, no creds, no CA -
	// everything stale on the target must be wiped.
	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, nil, nil, nil)

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
	if patched.Spec.Backup.EndpointCA != (postgresapp.EndpointCA{}) {
		t.Errorf("stale endpointCA survived; got %+v", patched.Spec.Backup.EndpointCA)
	}
}

// TestBuildPostgresAppRestorePatch_ForwardsEndpointCA verifies that a
// strategy template with an EndpointCA reference flows through to the
// target app's spec.backup.endpointCA. Without this the chart's
// externalClusters[].barmanObjectStore.endpointCA stays empty and the
// recovery init job panics in CNPG's instance-manager when the S3
// endpoint serves a self-signed certificate (cozystack seaweedfs-s3 does).
func TestBuildPostgresAppRestorePatch_ForwardsEndpointCA(t *testing.T) {
	app := newPostgresApp("pg", "tenant")
	caRef := &strategyv1alpha1.EndpointCARef{
		SecretRef: corev1.LocalObjectReference{Name: "pg-cnpg-backup-ca"},
		Key:       "ca.crt",
	}
	patched := buildPostgresAppRestorePatch(app, "src", "s3://b/", "", "", nil, caRef, nil, nil)

	want := postgresapp.EndpointCA{Name: "pg-cnpg-backup-ca", Key: "ca.crt"}
	if got := patched.Spec.Backup.EndpointCA; got != want {
		t.Fatalf("endpointCA mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// TestMarshalUnmarshalCNPGBackupSnapshot round-trips the source-spec
// snapshot persisted in Backup.status.underlyingResources, including the
// BackupClassStrategy parameters that were in effect at backup time -
// without persisting those a strategy that uses {{ .Parameters.foo }} for
// a Secret reference / endpointCA / key name would re-render at restore
// time with parameters=nil and produce a broken patch.
func TestMarshalUnmarshalCNPGBackupSnapshot(t *testing.T) {
	src := newPostgresApp("pg-src", "tenant")
	src.Spec.Databases = map[string]postgresapp.Database{
		"app": {Extensions: []string{"pgcrypto"}},
	}
	src.Spec.Users = map[string]postgresapp.User{
		"app": {Password: "p"},
	}
	parameters := map[string]string{
		"credsSecret": "tenant-shared-creds",
		"region":      "eu-west-1",
	}

	raw, err := marshalCNPGBackupSnapshot(src, parameters)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw == nil {
		t.Fatalf("expected non-nil RawExtension")
	}

	bk := &backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw}}
	dbs, users, params, err := unmarshalCNPGBackupSnapshot(bk)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dbs["app"].Extensions[0] != "pgcrypto" {
		t.Errorf("databases round-trip mismatch: %#v", dbs)
	}
	if users["app"].Password != "p" {
		t.Errorf("users round-trip mismatch: %#v", users)
	}
	if params["credsSecret"] != "tenant-shared-creds" {
		t.Errorf("parameters round-trip mismatch: got %#v", params)
	}
	if params["region"] != "eu-west-1" {
		t.Errorf("parameters round-trip mismatch: got %#v", params)
	}
}

// TestMarshalUnmarshalCNPGBackupSnapshot_BackwardCompat covers a Backup
// taken by an older controller version (no parameters block in the
// snapshot JSON). The unmarshal path must treat that as "no parameter
// overrides" rather than failing - the strategy template might have used
// .Parameters at the time, and we cannot reconstruct values we never
// persisted, so the caller proceeds with parameters=nil and lets the
// rendered output fall back to whatever the template defaults emit.
func TestMarshalUnmarshalCNPGBackupSnapshot_BackwardCompat(t *testing.T) {
	legacyRaw := []byte(`{"kind":"CNPGBackupSnapshot","apiVersion":"backups.cozystack.io/v1alpha1","databases":{"app":{}},"users":{"app":{}}}`)
	bk := &backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: &runtime.RawExtension{Raw: legacyRaw}}}
	_, _, params, err := unmarshalCNPGBackupSnapshot(bk)
	if err != nil {
		t.Fatalf("unmarshal legacy snapshot: %v", err)
	}
	if len(params) != 0 {
		t.Errorf("legacy snapshot must yield empty parameters, got %#v", params)
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
			_, _, _, err := unmarshalCNPGBackupSnapshot(bk)
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

// TestApplyClusterPluginBackup_NotFoundOnMissingCluster locks in the
// precondition on applyClusterPluginBackup: when the live cnpg.io Cluster has
// not yet been rendered by the chart, the driver must surface NotFound (so the
// caller can retry-with-backoff) instead of issuing a doomed SSA Apply that
// would be rejected by the API server for missing required Cluster fields.
func TestApplyClusterPluginBackup_NotFoundOnMissingCluster(t *testing.T) {
	c := newCNPGStrategyTestClient(t)
	r := &BackupJobReconciler{Client: c}
	tmpl := &strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/x/",
		},
	}

	_, err := r.applyClusterPluginBackup(context.Background(), "tenant", "postgres-missing", tmpl, "postgres-missing")
	if err == nil {
		t.Fatalf("expected NotFound error, got nil")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected IsNotFound error, got %v", err)
	}
}

// TestRecoveryBootstrapClusterState_TerminatingCluster locks in the
// DeletionTimestamp guard. Without it, a Cluster CR mid-deletion (after
// purgeExistingCluster fired r.Delete but cnpg.io's finalizers haven't
// drained yet) would get treated as "still has bootstrap" by the
// reconciler. The caller would then either skip the next purge or wait
// for healthy on a CR that's about to disappear; in either case Helm
// might SSA-merge the chart's bootstrap.recovery onto the terminating
// CR and cnpg-operator's bootstrap-immutability check would drop the
// change, leaving the cluster on the original initdb spec.
func TestRecoveryBootstrapClusterState_TerminatingCluster(t *testing.T) {
	now := metav1.Now()
	t.Run("terminating cluster reports not-yet-recovered", func(t *testing.T) {
		cluster := &cnpgtypes.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "tenant",
				Name:              "postgres-app",
				DeletionTimestamp: &now,
				Finalizers:        []string{"cnpg.io/cluster"}, // satisfies finalizer requirement for DeletionTimestamp
			},
			Spec: cnpgtypes.ClusterSpec{
				Bootstrap: &cnpgtypes.BootstrapConfiguration{
					Recovery: &cnpgtypes.RecoverySource{Source: "pg-src"},
				},
			},
		}
		c := newCNPGStrategyTestClient(t, cluster)
		r := &RestoreJobReconciler{Client: c}
		got, createdAt, err := r.recoveryBootstrapClusterState(context.Background(), "tenant", "postgres-app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatalf("expected false for terminating cluster, got true")
		}
		if createdAt != nil {
			t.Fatalf("expected nil createdAt for terminating cluster, got %v", createdAt)
		}
	})

	t.Run("live recovery cluster reports recovered with creation timestamp", func(t *testing.T) {
		created := metav1.NewTime(time.Now().Add(-time.Hour))
		cluster := &cnpgtypes.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "postgres-app", CreationTimestamp: created},
			Spec: cnpgtypes.ClusterSpec{
				Bootstrap: &cnpgtypes.BootstrapConfiguration{
					Recovery: &cnpgtypes.RecoverySource{Source: "pg-src"},
				},
			},
		}
		c := newCNPGStrategyTestClient(t, cluster)
		r := &RestoreJobReconciler{Client: c}
		got, createdAt, err := r.recoveryBootstrapClusterState(context.Background(), "tenant", "postgres-app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatalf("expected true for live recovery cluster, got false")
		}
		if createdAt == nil {
			t.Fatalf("expected non-nil createdAt for live recovery cluster")
		}
	})

	t.Run("missing cluster reports not-yet", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t)
		r := &RestoreJobReconciler{Client: c}
		got, createdAt, err := r.recoveryBootstrapClusterState(context.Background(), "tenant", "postgres-app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatalf("expected false for missing cluster, got true")
		}
		if createdAt != nil {
			t.Fatalf("expected nil createdAt for missing cluster, got %v", createdAt)
		}
	})
}

// TestCNPGBackupWALArchived locks in the gate that defers the destructive
// in-place purge until the underlying cnpg.io/Backup is in a state that
// guarantees its closing WAL is in object storage. CNPG only flips
// Backup.status.phase=completed (and writes endWal) after barman-cloud-
// backup has confirmed the upload, so checking the two together is what
// the gate uses; an earlier version of this gate compared
// Cluster.status.lastArchivedWAL to Backup.status.endWal but
// lastArchivedWAL is not present on CNPG 1.27.x cluster status, and the
// gate silently treated the missing field as the empty string and never
// cleared.
func TestCNPGBackupWALArchived(t *testing.T) {
	const (
		ns       = "tenant"
		cluster  = "postgres-pg-src"
		backupID = "pg-src-adhoc"
		endWal   = "000000010000000000000004"
	)
	mkPlatformBackup := func() *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: backupID},
			Spec: backupsv1alpha1.BackupSpec{
				DriverMetadata: map[string]string{
					cnpgBackupNameKey:  backupID,
					cnpgClusterNameKey: cluster,
				},
			},
		}
	}
	mkCNPGBackup := func(phase, end string) *cnpgtypes.Backup {
		return &cnpgtypes.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: backupID},
			Spec:       cnpgtypes.BackupSpec{Cluster: cnpgtypes.ClusterReference{Name: cluster}},
			Status:     cnpgtypes.BackupStatus{Phase: phase, EndWal: end},
		}
	}

	t.Run("blocks while underlying cnpg.io/Backup is still running", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t, mkCNPGBackup("running", ""))
		r := &RestoreJobReconciler{Client: c}
		ready, msg, err := r.cnpgBackupWALArchived(context.Background(), mkPlatformBackup())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ready {
			t.Fatalf("expected not-ready, got ready (%s)", msg)
		}
		if msg == "" {
			t.Errorf("expected human-readable message, got empty")
		}
	})

	t.Run("blocks when underlying cnpg.io/Backup is in a failure state", func(t *testing.T) {
		// Failure state must NOT clear the gate even when endWal somehow has
		// a value - the failure could mean barman is mid-retry and the
		// closing WAL upload never landed.
		c := newCNPGStrategyTestClient(t, mkCNPGBackup("failed", endWal))
		r := &RestoreJobReconciler{Client: c}
		ready, _, err := r.cnpgBackupWALArchived(context.Background(), mkPlatformBackup())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ready {
			t.Fatalf("expected not-ready for phase=failed")
		}
	})

	t.Run("clears when phase=completed and endWal is set", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t, mkCNPGBackup(cnpgBackupPhaseComplete, endWal))
		r := &RestoreJobReconciler{Client: c}
		ready, _, err := r.cnpgBackupWALArchived(context.Background(), mkPlatformBackup())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ready {
			t.Fatalf("expected ready when phase=completed and endWal is set")
		}
	})

	t.Run("blocks when phase=completed but endWal is still empty", func(t *testing.T) {
		// Defensive: if a future CNPG version flips phase to completed
		// before persisting endWal (or a buggy build does), we want the
		// gate to stay closed rather than proceeding to a destructive
		// purge against a backup whose closing WAL we cannot prove is
		// in object storage.
		c := newCNPGStrategyTestClient(t, mkCNPGBackup(cnpgBackupPhaseComplete, ""))
		r := &RestoreJobReconciler{Client: c}
		ready, _, err := r.cnpgBackupWALArchived(context.Background(), mkPlatformBackup())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ready {
			t.Fatalf("expected not-ready when endWal is empty")
		}
	})

	t.Run("conservative on missing cnpg.io/Backup (treat as ready, retention case)", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t)
		r := &RestoreJobReconciler{Client: c}
		ready, _, err := r.cnpgBackupWALArchived(context.Background(), mkPlatformBackup())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ready {
			t.Fatalf("expected ready when underlying cnpg.io/Backup is gone (retention)")
		}
	})

	t.Run("blocks when driverMetadata is missing the backup name", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t)
		r := &RestoreJobReconciler{Client: c}
		platform := mkPlatformBackup()
		delete(platform.Spec.DriverMetadata, cnpgBackupNameKey)
		ready, _, err := r.cnpgBackupWALArchived(context.Background(), platform)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ready {
			t.Fatalf("expected not-ready when driverMetadata is missing the backup name")
		}
	})
}

// TestSetCNPGRestoreHRSuspended locks in the toggle helper used by the
// destructive purge to suspend helm-controller before mutating the
// Postgres app spec, and to resume it once the live Cluster + PVCs are
// gone. Without this guard helm-controller observes the bootstrap.enabled=
// true flip while the live Cluster still has bootstrap.initdb, attempts an
// upgrade that CNPG admission rejects, and rolls back into a fresh initdb
// cluster that races our purge.
func TestSetCNPGRestoreHRSuspended(t *testing.T) {
	const (
		ns   = "tenant"
		name = "postgres-app"
	)
	mkHR := func(suspended *bool) *unstructured.Unstructured {
		hr := &unstructured.Unstructured{}
		hr.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"})
		hr.SetNamespace(ns)
		hr.SetName(name)
		spec := map[string]interface{}{}
		if suspended != nil {
			spec["suspend"] = *suspended
		}
		_ = unstructured.SetNestedMap(hr.Object, spec, "spec")
		return hr
	}
	suspendedField := func(t *testing.T, dyn dynamic.Interface) bool {
		t.Helper()
		hr, err := dyn.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get HR: %v", err)
		}
		got, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
		return got
	}

	t.Run("flips spec.suspend false -> true", func(t *testing.T) {
		falsePtr := false
		dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t), mkHR(&falsePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setCNPGRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("suspend: %v", err)
		}
		if !suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend=true after suspend call")
		}
	})

	t.Run("flips spec.suspend true -> false", func(t *testing.T) {
		truePtr := true
		dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t), mkHR(&truePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setCNPGRestoreHRSuspended(context.Background(), ns, name, false); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend=false after resume call")
		}
	})

	t.Run("idempotent when already at desired state", func(t *testing.T) {
		// Calling suspend on an already-suspended HR (or resume on an
		// already-resumed one) must be a no-op so retries from a
		// flapping reconcile loop don't churn HR resourceVersions.
		truePtr := true
		dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t), mkHR(&truePtr))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setCNPGRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("idempotent suspend: %v", err)
		}
		if !suspendedField(t, dyn) {
			t.Fatalf("expected spec.suspend to stay true")
		}
	})

	t.Run("missing HR is a no-op", func(t *testing.T) {
		// A target Postgres app whose chart-rendered HR has not landed
		// yet (or has been deleted) must not fail the destructive flow:
		// the controller proceeds with patch+purge and the chart will
		// later create a fresh HR from the patched values.
		dyn := dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t))
		r := &RestoreJobReconciler{Interface: dyn}
		if err := r.setCNPGRestoreHRSuspended(context.Background(), ns, name, true); err != nil {
			t.Fatalf("missing HR: %v", err)
		}
	})
}

// TestCNPGClusterFullyGone locks in the wait-for-purge helper that gates
// HR resume until the live Cluster + its PVCs have actually disappeared.
// Returning early on a cluster that is mid-deletion (DeletionTimestamp
// set, finalizers still draining) lets helm race the still-terminating CR
// and we end up back in the "Only one bootstrap" admission-rejection loop.
func TestCNPGClusterFullyGone(t *testing.T) {
	const (
		ns   = "tenant"
		name = "postgres-app"
	)
	now := metav1.Now()

	t.Run("cluster + PVCs absent -> gone=true", func(t *testing.T) {
		c := newCNPGStrategyTestClient(t)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.cnpgClusterFullyGone(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gone {
			t.Fatalf("expected gone=true with no Cluster and no PVCs")
		}
	})

	t.Run("live cluster -> gone=false", func(t *testing.T) {
		cluster := &cnpgtypes.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		}
		c := newCNPGStrategyTestClient(t, cluster)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.cnpgClusterFullyGone(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gone {
			t.Fatalf("expected gone=false when cluster still exists")
		}
	})

	t.Run("terminating cluster (DeletionTimestamp) -> gone=false", func(t *testing.T) {
		cluster := &cnpgtypes.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         ns,
				Name:              name,
				DeletionTimestamp: &now,
				Finalizers:        []string{"cnpg.io/cluster"},
			},
		}
		c := newCNPGStrategyTestClient(t, cluster)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.cnpgClusterFullyGone(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gone {
			t.Fatalf("expected gone=false for terminating cluster")
		}
	})

	t.Run("cluster gone but PVC remains -> gone=false", func(t *testing.T) {
		// CNPG's PVCs are deleted alongside the Cluster but storage
		// drivers (linstor, ceph) can hold them in a Terminating state
		// for tens of seconds. Resuming the HR while a PVC with the
		// cluster label still exists lets helm re-render and immediately
		// re-attach to a leftover volume.
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      name + "-1",
				Labels:    map[string]string{cnpgClusterLabel: name},
			},
		}
		c := newCNPGStrategyTestClient(t, pvc)
		r := &RestoreJobReconciler{Client: c}
		gone, err := r.cnpgClusterFullyGone(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gone {
			t.Fatalf("expected gone=false when a labelled PVC is still around")
		}
	})
}

// testCNPGScheme returns a runtime.Scheme that knows the unstructured
// HelmRelease GVK used by the dynamic-client tests above.
func testCNPGScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmReleaseList"}, &unstructured.UnstructuredList{})
	return s
}

// TestApplyClusterPluginBackup_PatchesExistingCluster confirms the happy path:
// when the Cluster exists the driver SSA-applies an ObjectStore CR carrying the
// The serverName is the S3 WAL-archive path prefix; changing it on a live
// Cluster splits the archive across two prefixes and breaks the eventual
// restore with "WAL not found". A Cluster that already has the barman-cloud
// plugin attached must therefore keep its effective serverName — the explicit
// parameter when present, its own name (the plugin's default) when the plugin
// is attached without one — and only a Cluster with no plugin at all takes the
// strategy template's value.
func TestApplyClusterPluginBackup_PreservesLiveServerName(t *testing.T) {
	explicit := true
	cases := []struct {
		name       string
		plugins    []cnpgtypes.PluginConfiguration
		wantServer string
	}{
		{
			name: "explicit live serverName wins over the strategy's",
			plugins: []cnpgtypes.PluginConfiguration{{
				Name:          cnpgtypes.PluginName,
				IsWALArchiver: &explicit,
				Parameters:    map[string]string{barmanObjectNameParam: "postgres-app", barmanServerNameParam: "postgres-app"},
			}},
			wantServer: "postgres-app",
		},
		{
			name: "plugin attached without serverName defaults to the cluster name",
			plugins: []cnpgtypes.PluginConfiguration{{
				Name:          cnpgtypes.PluginName,
				IsWALArchiver: &explicit,
				Parameters:    map[string]string{barmanObjectNameParam: "postgres-app"},
			}},
			wantServer: "postgres-app",
		},
		{
			name:       "no plugin attached: the strategy's serverName applies",
			plugins:    nil,
			wantServer: "strategy-name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &cnpgtypes.Cluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "postgres-app", UID: "cluster-uid-123"},
			}
			cluster.Spec.Plugins = tc.plugins
			c := newCNPGStrategyTestClient(t, cluster)
			r := &BackupJobReconciler{Client: c}
			tmpl := &strategyv1alpha1.CNPGTemplate{
				BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://bucket/x/"},
			}

			got, err := r.applyClusterPluginBackup(context.Background(), "tenant", "postgres-app", tmpl, "strategy-name")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantServer {
				t.Fatalf("effective serverName = %q, want %q", got, tc.wantServer)
			}

			patched := &cnpgtypes.Cluster{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: "postgres-app"}, patched); err != nil {
				t.Fatalf("get Cluster after apply: %v", err)
			}
			if len(patched.Spec.Plugins) != 1 {
				t.Fatalf("expected exactly one plugin entry, got %+v", patched.Spec.Plugins)
			}
			if s := patched.Spec.Plugins[0].Parameters[barmanServerNameParam]; s != tc.wantServer {
				t.Fatalf("patched plugin serverName = %q, want %q", s, tc.wantServer)
			}
		})
	}
}

// barman configuration and SSA-patches the Cluster's spec.plugins to reference
// it via the barman-cloud plugin (the plugin replaces the deprecated native
// spec.backup.barmanObjectStore). serverName must land on the Cluster plugin
// parameter, never inside the ObjectStore configuration (the plugin forbids it).
func TestApplyClusterPluginBackup_PatchesExistingCluster(t *testing.T) {
	cluster := &cnpgtypes.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "postgres-app", UID: "cluster-uid-123"},
	}
	c := newCNPGStrategyTestClient(t, cluster)
	r := &BackupJobReconciler{Client: c}
	tmpl := &strategyv1alpha1.CNPGTemplate{
		BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{
			DestinationPath: "s3://bucket/x/",
			RetentionPolicy: "30d",
		},
	}

	if _, err := r.applyClusterPluginBackup(context.Background(), "tenant", "postgres-app", tmpl, "tenant-app"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The ObjectStore CR carries destinationPath + retentionPolicy and no serverName.
	store := &cnpgtypes.ObjectStore{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tenant", Name: "postgres-app"}, store); err != nil {
		t.Fatalf("get ObjectStore after apply: %v", err)
	}
	if store.Spec.Configuration.DestinationPath != "s3://bucket/x/" {
		t.Errorf("ObjectStore destinationPath: got %q", store.Spec.Configuration.DestinationPath)
	}
	if store.Spec.RetentionPolicy != "30d" {
		t.Errorf("ObjectStore retentionPolicy: got %q", store.Spec.RetentionPolicy)
	}
	// The ObjectStore must pin the barman-cloud sidecar's S3 request-checksum
	// policy to when_required, or uploads to non-AWS S3 gateways (Ceph RGW, the
	// platform's own SeaweedFS system bucket) fail with an x-amz-content-sha256
	// InvalidArgument. The chart does not render this ObjectStore in the
	// platform-managed useSystemBucket=true flow, so this Go path is the only
	// place that sets it there.
	sc := store.Spec.InstanceSidecarConfiguration
	if sc == nil || len(sc.Env) != 1 ||
		sc.Env[0].Name != "AWS_REQUEST_CHECKSUM_CALCULATION" || sc.Env[0].Value != "when_required" {
		t.Errorf("ObjectStore instanceSidecarConfiguration.env: got %+v, want [AWS_REQUEST_CHECKSUM_CALCULATION=when_required]", sc)
	}
	// The ObjectStore must be owner-referenced to the Cluster so Kubernetes GC
	// removes it when the Cluster is deleted (no orphan in the platform flow,
	// where the chart does not render this ObjectStore).
	if len(store.OwnerReferences) != 1 {
		t.Fatalf("expected exactly one ownerReference on the ObjectStore, got %+v", store.OwnerReferences)
	}
	own := store.OwnerReferences[0]
	if own.Kind != "Cluster" || own.Name != "postgres-app" || own.UID != "cluster-uid-123" {
		t.Errorf("ObjectStore ownerReference: got kind=%q name=%q uid=%q, want Cluster/postgres-app/cluster-uid-123", own.Kind, own.Name, own.UID)
	}
	if store.Spec.Configuration.ServerName != "" {
		t.Errorf("ObjectStore configuration must not carry serverName (plugin forbids it); got %q", store.Spec.Configuration.ServerName)
	}

	// The Cluster references the ObjectStore through spec.plugins, with
	// serverName on the plugin parameter and isWALArchiver=true.
	got := &cnpgtypes.Cluster{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), got); err != nil {
		t.Fatalf("get cluster after patch: %v", err)
	}
	if len(got.Spec.Plugins) != 1 {
		t.Fatalf("expected exactly one plugin entry, got %+v", got.Spec.Plugins)
	}
	p := got.Spec.Plugins[0]
	if p.Name != cnpgtypes.PluginName {
		t.Errorf("plugin name: got %q, want %q", p.Name, cnpgtypes.PluginName)
	}
	if p.IsWALArchiver == nil || !*p.IsWALArchiver {
		t.Errorf("expected isWALArchiver=true, got %v", p.IsWALArchiver)
	}
	if got := p.Parameters[barmanObjectNameParam]; got != "postgres-app" {
		t.Errorf("plugin barmanObjectName: got %q, want %q", got, "postgres-app")
	}
	if got := p.Parameters[barmanServerNameParam]; got != "tenant-app" {
		t.Errorf("plugin serverName: got %q, want %q", got, "tenant-app")
	}
}

// TestEnsureCNPGBackup_UsesPluginMethod locks in that driver-initiated backups
// go through the barman-cloud plugin. spec.method / spec.pluginConfiguration is
// the single field that routes the run: if it regressed to the default
// (barmanObjectStore) method, CNPG would run the legacy method against a Cluster
// that no longer has one and every platform backup would fail.
func TestEnsureCNPGBackup_UsesPluginMethod(t *testing.T) {
	j := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
	}
	c := newCNPGStrategyTestClient(t, j)
	r := &BackupJobReconciler{Client: c}

	bk, err := r.ensureCNPGBackup(context.Background(), j, "postgres-app")
	if err != nil {
		t.Fatalf("ensureCNPGBackup: %v", err)
	}
	if bk.Spec.Method != cnpgtypes.BackupMethodPlugin {
		t.Errorf("Backup spec.method: got %q, want %q", bk.Spec.Method, cnpgtypes.BackupMethodPlugin)
	}
	if bk.Spec.PluginConfiguration == nil || bk.Spec.PluginConfiguration.Name != cnpgtypes.PluginName {
		t.Errorf("Backup spec.pluginConfiguration: got %+v, want name=%q", bk.Spec.PluginConfiguration, cnpgtypes.PluginName)
	}
	if bk.Spec.Cluster.Name != "postgres-app" {
		t.Errorf("Backup spec.cluster.name: got %q, want postgres-app", bk.Spec.Cluster.Name)
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

// TestReconcileCNPG_StartedAtReturnsEarlyToAvoidStaleResourceVersion locks
// in the early-return after the StartedAt patch. Before the fix, reconcileCNPG
// patched StartedAt onto a refetched copy and then continued the same call
// using the local `j` whose ResourceVersion was stale, so any subsequent
// r.Status().Update on `j` (e.g. the markBackupJobFailed path or the
// ClusterNotReady condition write) carried a pre-patch RV and produced a
// Conflict on the very first poll. The fix returns RequeueAfter immediately
// after the StartedAt patch so the next reconcile reads `j` cleanly.
func TestReconcileCNPG_StartedAtReturnsEarlyToAvoidStaleResourceVersion(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group

	stored := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bj"},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
		},
	}
	c := newCNPGStrategyTestClient(t, stored)
	r := &BackupJobReconciler{Client: c}

	j := stored.DeepCopy()
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: &strategyGroup,
			Kind:     strategyv1alpha1.CNPGStrategyKind,
			Name:     "missing-strategy",
		},
	}

	result, err := r.reconcileCNPG(context.Background(), j, resolved)
	if err != nil {
		t.Fatalf("reconcileCNPG: unexpected error %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected RequeueAfter > 0 after StartedAt patch, got %+v", result)
	}

	persisted := &backupsv1alpha1.BackupJob{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(stored), persisted); err != nil {
		t.Fatalf("get persisted: %v", err)
	}
	if persisted.Status.StartedAt == nil {
		t.Errorf("expected StartedAt persisted, got nil")
	}
	// Without the early return, reconcileCNPG would have proceeded past the
	// StartedAt block, found the strategy missing, and called
	// markBackupJobFailed - persisting Phase=Failed (and, depending on RV
	// enforcement, returning Conflict). With the fix, Phase stays empty and
	// the next reconcile picks up cleanly with the post-patch RV.
	if persisted.Status.Phase != "" {
		t.Errorf("expected Phase unchanged after StartedAt early return, got %q", persisted.Status.Phase)
	}
}

// TestReconcileCNPGRestore_MissingStrategyFailsClosedAfterDeadline covers the
// restore-path blocker: a RestoreJob whose referenced CNPG Strategy CR never
// appears must NOT requeue forever. requeueRestoreStrategyNotReady gates
// terminal-vs-transient on Status.StartedAt, and the "fail closed" guarantee
// only holds if every driver stamps StartedAt before it can reach the Strategy
// NotFound branch. reconcileCNPGRestore stamps StartedAt and returns early on
// the first reconcile (well before the strategy lookup), so the deadline clock
// starts even when the job begins with StartedAt==nil; once the deadline
// elapses the next reconcile flips the RestoreJob to Phase=Failed.
//
// The existing TestRequeueRestoreStrategyNotReady_BoundedByDeadline only
// exercises the helper in isolation with StartedAt pre-populated, so it cannot
// catch a driver that reaches the NotFound branch without ever stamping
// StartedAt. This test proves the guarantee end to end at the driver call site.
func TestReconcileCNPGRestore_MissingStrategyFailsClosedAfterDeadline(t *testing.T) {
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "bk"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: "pg"},
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "missing-strategy",
			},
		},
	}
	// Status.StartedAt deliberately nil: the driver itself must start the clock.
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
	}
	// No CNPG Strategy CR seeded - the NotFound branch is the point.
	c := newCNPGStrategyTestClient(t, backup, rj)
	r := &RestoreJobReconciler{Client: c}
	ctx := context.Background()

	// Pass 1: StartedAt is nil, so the driver stamps it and requeues. It must
	// NOT fail yet - the bootstrap grace window has only just opened.
	res, err := r.reconcileCNPGRestore(ctx, rj, backup)
	if err != nil {
		t.Fatalf("pass 1 reconcileCNPGRestore: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("pass 1 must requeue after stamping StartedAt, got %+v", res)
	}
	persisted := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(rj), persisted); err != nil {
		t.Fatalf("get after pass 1: %v", err)
	}
	if persisted.Status.StartedAt == nil {
		t.Fatalf("driver did not stamp StartedAt before the Strategy NotFound branch; " +
			"the deadline clock would never start and the RestoreJob would requeue forever")
	}
	if persisted.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("must not fail within the grace window, got Phase=%q", persisted.Status.Phase)
	}

	// Simulate StrategyNotReadyDeadline of wall-clock elapsing by backdating the
	// persisted StartedAt past the deadline.
	stale := metav1.NewTime(persisted.Status.StartedAt.Add(-StrategyNotReadyDeadline - time.Minute))
	base := persisted.DeepCopy()
	persisted.Status.StartedAt = &stale
	if err := c.Status().Patch(ctx, persisted, client.MergeFrom(base)); err != nil {
		t.Fatalf("backdate StartedAt: %v", err)
	}

	// Pass 2: StartedAt is now past the deadline and the Strategy CR still does
	// not exist, so the driver call site must fail closed.
	reloaded := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(rj), reloaded); err != nil {
		t.Fatalf("get before pass 2: %v", err)
	}
	res, err = r.reconcileCNPGRestore(ctx, reloaded, backup)
	if err != nil {
		t.Fatalf("pass 2 reconcileCNPGRestore: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("terminal failure must not requeue, got %+v", res)
	}
	final := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(rj), final); err != nil {
		t.Fatalf("get after pass 2: %v", err)
	}
	if final.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("expected Phase=Failed once the deadline elapsed, got %q", final.Status.Phase)
	}
	if !strings.Contains(final.Status.Message, "missing-strategy") {
		t.Errorf("failure message should name the missing strategy, got %q", final.Status.Message)
	}
}

// TestReconcileCNPGRestore_RepeatInPlacePurgesStaleRecoveryCluster is the
// reconcile-level regression test for #3311. The pure-function tests
// (TestCNPGPurgeNeeded / TestCNPGClusterFreshlyRecovered) lock in the truth
// table, but the bug lived at the call site: a repeat in-place restore fed the
// purge decision the wrong signal and skipped the destructive purge, so the
// job reported Succeeded against untouched data. This drives the real
// reconcileCNPGRestore path with a fake client and asserts the destructive
// purge actually fires for a stale leftover recovery Cluster - and, in the
// mirror case, that a Cluster this restore just re-created is NOT re-purged
// (the status-write-race protection the guard was originally built for).
func TestReconcileCNPGRestore_RepeatInPlacePurgesStaleRecoveryCluster(t *testing.T) {
	const (
		ns          = "tenant"
		appName     = "app"
		clusterName = "postgres-app"
		cnpgBkName  = "cnpgbk"
	)
	apiGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	strategyGroup := strategyv1alpha1.GroupVersion.Group
	ctx := context.Background()

	// startedAt anchors the discriminator: a stale leftover Cluster predates
	// it, a freshly-recovered one postdates it.
	startedAt := metav1.NewTime(time.Now())

	mkBackupArtifact := func(t *testing.T) *backupsv1alpha1.Backup {
		t.Helper()
		snap, err := marshalCNPGBackupSnapshot(newPostgresApp(appName, ns), nil)
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		return &backupsv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bk"},
			Spec: backupsv1alpha1.BackupSpec{
				ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: postgresAppKind, Name: appName},
				StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: &strategyGroup, Kind: strategyv1alpha1.CNPGStrategyKind, Name: "cnpg-strategy"},
				DriverMetadata: map[string]string{
					cnpgServerNameKey:      appName,
					cnpgDestinationPathKey: "s3://bucket/" + appName + "/",
					cnpgBackupNameKey:      cnpgBkName,
				},
			},
			Status: backupsv1alpha1.BackupStatus{UnderlyingResources: snap},
		}
	}
	strategy := &strategyv1alpha1.CNPG{
		ObjectMeta: metav1.ObjectMeta{Name: "cnpg-strategy"},
		Spec: strategyv1alpha1.CNPGSpec{
			Template: strategyv1alpha1.CNPGTemplate{
				BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://bucket/"},
			},
		},
	}
	// A completed cnpg.io/Backup with endWal set clears the WAL-archive gate
	// so the reconcile can reach the purge step.
	cnpgBackup := &cnpgtypes.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cnpgBkName},
		Spec:       cnpgtypes.BackupSpec{Cluster: cnpgtypes.ClusterReference{Name: clusterName}},
		Status:     cnpgtypes.BackupStatus{Phase: cnpgBackupPhaseComplete, EndWal: "000000010000000000000003"},
	}
	mkRecoveryCluster := func(created metav1.Time) *cnpgtypes.Cluster {
		return &cnpgtypes.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: clusterName, CreationTimestamp: created},
			Spec: cnpgtypes.ClusterSpec{
				Bootstrap: &cnpgtypes.BootstrapConfiguration{
					Recovery: &cnpgtypes.RecoverySource{Source: appName},
				},
			},
		}
	}
	mkClusterPVC := func() *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: clusterName + "-1", Labels: map[string]string{cnpgClusterLabel: clusterName}},
		}
	}
	mkRestoreJob := func() *backupsv1alpha1.RestoreJob {
		sa := startedAt
		return &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "rj"},
			Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
			Status:     backupsv1alpha1.RestoreJobStatus{StartedAt: &sa, Phase: backupsv1alpha1.RestoreJobPhaseRunning},
		}
	}

	t.Run("stale leftover recovery cluster from a prior restore is purged", func(t *testing.T) {
		backup := mkBackupArtifact(t)
		// creationTimestamp an hour before StartedAt: leftover from a prior restore.
		stale := metav1.NewTime(startedAt.Add(-time.Hour))
		c := newCNPGStrategyTestClient(t, backup, mkRestoreJob(), strategy, cnpgBackup,
			newPostgresApp(appName, ns), mkRecoveryCluster(stale), mkClusterPVC())
		r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t))}

		rj := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
			t.Fatalf("get seeded RestoreJob: %v", err)
		}
		if _, err := r.reconcileCNPGRestore(ctx, rj, backup); err != nil {
			t.Fatalf("reconcileCNPGRestore: %v", err)
		}

		// The stale Cluster (and its labelled PVC) must have been purged.
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: clusterName}, &cnpgtypes.Cluster{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected stale Cluster to be purged (NotFound), got err=%v", err)
		}
		pvcs := &corev1.PersistentVolumeClaimList{}
		if err := c.List(ctx, pvcs, client.InNamespace(ns), client.MatchingLabels{cnpgClusterLabel: clusterName}); err != nil {
			t.Fatalf("list PVCs: %v", err)
		}
		if len(pvcs.Items) != 0 {
			t.Fatalf("expected cluster PVCs purged, still have %d", len(pvcs.Items))
		}
	})

	t.Run("freshly-recovered cluster from this restore is not re-purged", func(t *testing.T) {
		backup := mkBackupArtifact(t)
		// creationTimestamp a minute after StartedAt: this restore's own re-render.
		fresh := metav1.NewTime(startedAt.Add(time.Minute))
		c := newCNPGStrategyTestClient(t, backup, mkRestoreJob(), strategy, cnpgBackup,
			newPostgresApp(appName, ns), mkRecoveryCluster(fresh))
		r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(testCNPGScheme(t))}

		rj := &backupsv1alpha1.RestoreJob{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
			t.Fatalf("get seeded RestoreJob: %v", err)
		}
		if _, err := r.reconcileCNPGRestore(ctx, rj, backup); err != nil {
			t.Fatalf("reconcileCNPGRestore: %v", err)
		}

		// The freshly-recovered Cluster must survive - re-purging it would
		// destroy the recovery this restore just started (the status-write-race
		// protection the guard was built for).
		got := &cnpgtypes.Cluster{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: clusterName}, got); err != nil {
			t.Fatalf("expected freshly-recovered Cluster to survive, got err=%v", err)
		}
		if !got.DeletionTimestamp.IsZero() {
			t.Fatalf("freshly-recovered Cluster must not be marked for deletion")
		}
	})
}

// newCNPGStrategyTestClient returns a fake client.Client wired up with
// the schemes the CNPG-strategy reconciler needs.
func newCNPGStrategyTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = cnpgtypes.AddToScheme(s)
	_ = postgresapp.AddToScheme(s)
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
}
