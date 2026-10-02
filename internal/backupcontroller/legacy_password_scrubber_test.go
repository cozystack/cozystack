package backupcontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func backupWithSnapshot(name, raw string) *backupsv1alpha1.Backup {
	b := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-a"},
	}
	if raw != "" {
		b.Status.UnderlyingResources = &runtime.RawExtension{Raw: []byte(raw)}
	}
	return b
}

func legacyCNPGSnapshot() string {
	return `{"kind":"CNPGBackupSnapshot","apiVersion":"` + cnpgBackupSnapshotAPIVersion + `",` +
		`"databases":{"app":{"roles":{"admin":["alice"]}}},` +
		`"users":{"alice":{"password":"hunter2","replication":true},"bob":{"password":"s3cret"},"carol":{}},` +
		`"parameters":{"retention":"30d"},"futureField":{"n":12345678901234567890}}`
}

func newScrubberClient(objs ...client.Object) client.WithWatch {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	return clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func getSnapshot(t *testing.T, c client.Client, name string) map[string]any {
	t.Helper()
	var b backupsv1alpha1.Backup
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: name}, &b); err != nil {
		t.Fatalf("get Backup %s: %v", name, err)
	}
	if b.Status.UnderlyingResources == nil {
		return nil
	}
	var snap map[string]any
	dec := json.NewDecoder(bytes.NewReader(b.Status.UnderlyingResources.Raw))
	dec.UseNumber()
	if err := dec.Decode(&snap); err != nil {
		t.Fatalf("decode snapshot of %s: %v", name, err)
	}
	return snap
}

func TestLegacyPasswordScrubber_RemovesPasswordsAndKeepsTheRest(t *testing.T) {
	c := newScrubberClient(backupWithSnapshot("legacy", legacyCNPGSnapshot()))

	if err := (&LegacyPasswordScrubber{Client: c}).Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	snap := getSnapshot(t, c, "legacy")
	users := snap["users"].(map[string]any)
	for name, u := range users {
		if _, ok := u.(map[string]any)["password"]; ok {
			t.Errorf("user %s still carries a password", name)
		}
	}
	if len(users) != 3 {
		t.Errorf("users = %v, want alice, bob and carol kept", users)
	}
	if got := users["alice"].(map[string]any)["replication"]; got != true {
		t.Errorf("alice.replication = %v, want true", got)
	}
	if got := snap["parameters"].(map[string]any)["retention"]; got != "30d" {
		t.Errorf("parameters.retention = %v, want 30d", got)
	}
	if _, ok := snap["databases"].(map[string]any)["app"]; !ok {
		t.Errorf("databases.app was dropped: %v", snap["databases"])
	}
	if got := snap["futureField"].(map[string]any)["n"]; got != json.Number("12345678901234567890") {
		t.Errorf("unknown field lost precision or was dropped: %v", got)
	}

	// The restore path must still accept the scrubbed snapshot.
	var b backupsv1alpha1.Backup
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: "legacy"}, &b)
	if _, err := unmarshalCNPGBackupSnapshot(&b); err != nil {
		t.Errorf("scrubbed snapshot no longer decodes for restore: %v", err)
	}
}

func TestLegacyPasswordScrubber_LeavesOtherBackupsUntouched(t *testing.T) {
	cases := map[string]string{
		"clean-cnpg":    `{"kind":"CNPGBackupSnapshot","apiVersion":"` + cnpgBackupSnapshotAPIVersion + `","users":{"alice":{}}}`,
		"other-kind":    `{"kind":"SomeOtherSnapshot","apiVersion":"` + cnpgBackupSnapshotAPIVersion + `","users":{"alice":{"password":"keep"}}}`,
		"other-version": `{"kind":"CNPGBackupSnapshot","apiVersion":"example.com/v9","users":{"alice":{"password":"keep"}}}`,
		"no-snapshot":   ``,
	}
	var objs []client.Object
	for name, raw := range cases {
		objs = append(objs, backupWithSnapshot(name, raw))
	}
	c := newScrubberClient(objs...)
	before := map[string]string{}
	for name := range cases {
		var b backupsv1alpha1.Backup
		_ = c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: name}, &b)
		before[name] = b.ResourceVersion
	}

	if err := (&LegacyPasswordScrubber{Client: c}).Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for name := range cases {
		var b backupsv1alpha1.Backup
		_ = c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: name}, &b)
		if b.ResourceVersion != before[name] {
			t.Errorf("%s was patched but carries nothing to scrub", name)
		}
	}
}

func TestLegacyPasswordScrubber_OneBadBackupDoesNotStopTheRest(t *testing.T) {
	c := newScrubberClient(
		backupWithSnapshot("not-an-object", `["not","an","object"]`),
		backupWithSnapshot("a-patch-fails", legacyCNPGSnapshot()),
		backupWithSnapshot("z-legacy", legacyCNPGSnapshot()),
	)
	failing := interceptor.NewClient(c, interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetName() == "a-patch-fails" {
				return errors.New("apiserver unavailable")
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
	})

	if err := (&LegacyPasswordScrubber{Client: failing}).Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail the manager, got %v", err)
	}

	users := getSnapshot(t, c, "z-legacy")["users"].(map[string]any)
	if _, ok := users["alice"].(map[string]any)["password"]; ok {
		t.Error("legacy Backup was not scrubbed after an earlier Backup failed")
	}
}

func TestLegacyPasswordScrubber_ListFailureDoesNotStopTheManager(t *testing.T) {
	failing := interceptor.NewClient(newScrubberClient(), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("forbidden")
		},
	})
	if err := (&LegacyPasswordScrubber{Client: failing}).Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail the manager, got %v", err)
	}
}
