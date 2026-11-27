package backupcontroller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	cnpgtypes "github.com/cozystack/cozystack/internal/backupcontroller/cnpgtypes"
	"github.com/cozystack/cozystack/internal/backupcontroller/postgresapp"
)

func TestApplyClusterPluginBackup_RecordsImageName(t *testing.T) {
	for _, image := range []string{"ghcr.io/cloudnative-pg/postgresql:17.7", "ghcr.io/cloudnative-pg/postgresql:17.12-standard-trixie"} {
		t.Run(image, func(t *testing.T) {
			cluster := &cnpgtypes.Cluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "postgres-app", UID: "cluster-uid-123"},
			}
			cluster.Spec.ImageName = image
			c := newCNPGStrategyTestClient(t, cluster)
			r := &BackupJobReconciler{Client: c, Interface: cnpgDynamicFor(t, cluster)}
			tmpl := &strategyv1alpha1.CNPGTemplate{
				BarmanObjectStore: strategyv1alpha1.BarmanObjectStoreTemplate{DestinationPath: "s3://bucket/x/"},
			}

			got, err := r.applyClusterPluginBackup(context.Background(), "tenant", "postgres-app", tmpl, "strategy-name", true, true, "bj")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.imageName != image {
				t.Fatalf("imageName = %q, want %q", got.imageName, image)
			}
		})
	}
}

func TestCNPGBackupSnapshot_CarriesImageName(t *testing.T) {
	raw, err := marshalCNPGBackupSnapshot(newPostgresApp("pg", "tenant"), nil, "postgresql", "ghcr.io/cloudnative-pg/postgresql:17.7")
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	snap, err := unmarshalCNPGBackupSnapshot(&backupsv1alpha1.Backup{Status: backupsv1alpha1.BackupStatus{UnderlyingResources: raw}})
	if err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.ImageName != "ghcr.io/cloudnative-pg/postgresql:17.7" {
		t.Fatalf("ImageName = %q", snap.ImageName)
	}
}

func TestBuildPostgresAppRestorePatch_SetsAndClearsSourceImage(t *testing.T) {
	app := newPostgresApp("pg", "tenant")
	app.Spec.Bootstrap.SourceImageName = "ghcr.io/cloudnative-pg/postgresql:16.11"

	if got := buildPostgresAppRestorePatch(app, "src", "pg-new", "s3://b/", "", "", "", nil, nil, nil, nil, "ghcr.io/cloudnative-pg/postgresql:17.7").Spec.Bootstrap.SourceImageName; got != "ghcr.io/cloudnative-pg/postgresql:17.7" {
		t.Errorf("bootstrap.sourceImageName: got %q", got)
	}
	if got := buildPostgresAppRestorePatch(app, "src", "pg-new", "s3://b/", "", "", "", nil, nil, nil, nil, "").Spec.Bootstrap.SourceImageName; got != "" {
		t.Errorf("bootstrap.sourceImageName from a previous restore must be cleared; got %q", got)
	}
}

func TestDisablePostgresAppBootstrap_ClearsSourceImage(t *testing.T) {
	app := newPostgresApp("pg", "tenant")
	app.Spec.Bootstrap.Enabled = true
	app.Spec.Bootstrap.SourceImageName = "ghcr.io/cloudnative-pg/postgresql:17.7"
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = postgresapp.AddToScheme(s)
	r := &RestoreJobReconciler{Client: clientfake.NewClientBuilder().WithScheme(s).WithObjects(app).Build()}

	if err := r.disablePostgresAppBootstrap(context.Background(), "tenant", "pg"); err != nil {
		t.Fatalf("disable bootstrap: %v", err)
	}
	got, err := r.getPostgresApp(context.Background(), "tenant", "pg")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Spec.Bootstrap.Enabled || got.Spec.Bootstrap.SourceImageName != "" {
		t.Fatalf("bootstrap after disable: enabled=%v sourceImageName=%q", got.Spec.Bootstrap.Enabled, got.Spec.Bootstrap.SourceImageName)
	}
}

func TestCNPGImageMajor(t *testing.T) {
	for image, want := range map[string]int{
		"ghcr.io/cloudnative-pg/postgresql:17.7":                  17,
		"ghcr.io/cloudnative-pg/postgresql:13.22":                 13,
		"ghcr.io/cloudnative-pg/postgresql:17.12-standard-trixie": 17,
		"ghcr.io/cloudnative-pg/postgis:17-3.6.1-standard-trixie": 17,
		"ghcr.io/cloudnative-pg/postgresql:18.1@sha256:0123":      18,
		"ghcr.io/cloudnative-pg/postgresql":                       0,
		"":                                                        0,
	} {
		if got := cnpgImageMajor(image); got != want {
			t.Errorf("cnpgImageMajor(%q) = %d, want %d", image, got, want)
		}
	}
}
