// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// inventoryHarness stands up a TLS inventory the client will actually verify,
// wiring the server's own certificate in as the CA the controller trusts.
func inventoryHarness(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *corev1.Secret, string) {
	t.Helper()

	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: inventoryCASecret, Namespace: "cozy-forklift"},
		Data:       map[string][]byte{inventoryCAKey: ca},
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatalf("writing the token file: %v", err)
	}
	return srv, secret, tokenFile
}

// Reading the whole task here would tie every VM's fate to every other one: one
// mistyped managed-object reference is the likeliest error on this API, and it
// would fail the VM being reconciled with a message naming a different machine,
// where a missing VM is meant to fail alone. It is also what keeps the cost
// linear rather than quadratic in the number of VMs, on every pass, for as long
// as anything is still transferring.
func TestSourceTopologyAsksOnlyAboutTheVMBeingReconciled(t *testing.T) {
	var mu sync.Mutex
	var asked []string

	srv, caSecret, tokenFile := inventoryHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/vms/vm-2") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"vm-2","name":"web-02",
			  "networks":[{"kind":"Network","id":"net-b"}],
			  "disks":[{"datastore":{"kind":"Datastore","id":"ds-b"}}]}`))
			return
		}
		// Every other VM of the task is absent from the inventory. If the
		// implementation walks the whole spec, this is what turns one tenant
		// typo into a task-wide failure — so the count assert below is the
		// real subject of this test.
		w.WriteHeader(http.StatusNotFound)
	})

	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"},
		migrationv1alpha1.VMImportRequest{ID: "vm-2", Name: "web-02"},
		migrationv1alpha1.VMImportRequest{ID: "vm-3", Name: "web-03"},
	)
	src := readySource("vcenter", "tenant-foo")

	provider := newObject(providerGVK)
	provider.SetName(sourceProviderName("vcenter"))
	provider.SetNamespace("tenant-foo")
	provider.SetUID(types.UID("provider-uid"))

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(tk, src, provider, caSecret).Build()
	r := &VMImportTaskReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: record.NewFakeRecorder(10),
		Inventory: &inventoryClient{
			BaseURL: srv.URL, Namespace: "cozy-forklift", Reader: c, TokenFile: tokenFile,
		},
	}

	nets, stores, err := r.sourceTopology(context.Background(), tk, src,
		&migrationv1alpha1.VMImportRequest{ID: "vm-2", Name: "web-02"})
	if err != nil {
		t.Fatalf("sourceTopology: %v", err)
	}
	if len(nets) != 1 || nets[0] != "net-b" {
		t.Errorf("networks = %v, want [net-b]", nets)
	}
	if len(stores) != 1 || stores[0] != "ds-b" {
		t.Errorf("datastores = %v, want [ds-b]", stores)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Fatalf("issued %d inventory calls for one VM (%v); a task of N VMs would cost N×N per pass", len(asked), asked)
	}
	if !strings.HasSuffix(asked[0], "/vms/vm-2") {
		t.Errorf("asked about %q, want the VM being reconciled", asked[0])
	}
}
