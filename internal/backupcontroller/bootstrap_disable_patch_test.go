package backupcontroller

import (
	"bytes"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cozystack/cozystack/internal/backupcontroller/postgresapp"
)

// disablePostgresAppBootstrap flips Bootstrap.Enabled from true to false through
// a client.MergeFrom merge patch. If the field keeps `json:"...,omitempty"`, the
// false zero value is dropped from the patched object, so the computed merge
// patch DELETES the key (`{"enabled":null}`) instead of setting it false — the
// exact trap the sibling Backup.UseSystemBucket field carries a comment against.
// It only converges today because the chart default for bootstrap.enabled is
// false; if that ever changes the restore reports Succeeded while recovery stays
// wedged. Pin that the wire patch sets enabled:false. Re-adding omitempty to the
// Enabled field turns this red.
func TestDisableBootstrapMergePatchSetsEnabledFalse(t *testing.T) {
	app := &postgresapp.Postgres{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-target", Namespace: "tenant-root"},
	}
	app.Spec.Bootstrap.Enabled = true

	patched := app.DeepCopy()
	patched.Spec.Bootstrap.Enabled = false

	data, err := client.MergeFrom(app).Data(patched)
	if err != nil {
		t.Fatalf("build merge patch: %v", err)
	}
	if bytes.Contains(data, []byte(`"enabled":null`)) {
		t.Fatalf("merge patch deletes the enabled key instead of setting false; the server would fall back to a default: %s", data)
	}
	if !bytes.Contains(data, []byte(`"enabled":false`)) {
		t.Fatalf("merge patch must carry enabled:false so the server clears bootstrap; got: %s", data)
	}
}
