// Package transient runs a chainsaw built from the e2e sandbox's patched tree
// against a fake apiserver that answers the asserted object's GET with
// HTTP 500 and an empty reason, which is what kube-apiserver sends for an etcd
// error such as `etcdserver: leader changed`.
// Release v0.2.15 ends an assert on the first such answer; the image carries
// a patch that keeps it polling until its own timeout.
package transient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const assertTimeout = 10 * time.Second

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func status(code int, reason, message string) map[string]any {
	return map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"code": code, "reason": reason, "message": message,
	}
}

// fakeAPIServer serves just enough discovery for chainsaw to resolve
// v1/ConfigMap, and answers GET default/probe with 500 for the first
// `failures` requests (all of them when failures < 0), then with the object.
func fakeAPIServer(t *testing.T, failures int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var gets atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"major": "1", "minor": "33", "gitVersion": "v1.33.0"})
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIVersions", "versions": []string{"v1"},
			"serverAddressByClientCIDRs": []map[string]string{{"clientCIDR": "0.0.0.0/0", "serverAddress": "127.0.0.1"}},
		})
	})
	mux.HandleFunc("/apis", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{}})
	})
	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, _ *http.Request) {
		verbs := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1",
			"resources": []map[string]any{
				{"name": "namespaces", "singularName": "namespace", "namespaced": false, "kind": "Namespace", "verbs": verbs},
				{"name": "configmaps", "singularName": "configmap", "namespaced": true, "kind": "ConfigMap", "verbs": verbs},
			},
		})
	})
	mux.HandleFunc("/api/v1/namespaces/default", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "Namespace", "apiVersion": "v1",
			"metadata": map[string]any{"name": "default"},
			"status":   map[string]any{"phase": "Active"},
		})
	})
	mux.HandleFunc("/api/v1/namespaces/default/configmaps/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, status(http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method))
			return
		}
		n := gets.Add(1)
		if failures < 0 || n <= failures {
			writeJSON(w, http.StatusInternalServerError, status(http.StatusInternalServerError, "", "etcdserver: leader changed"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": "ConfigMap", "apiVersion": "v1",
			"metadata": map[string]any{"name": "probe", "namespace": "default"},
			"data":     map[string]string{"ready": "true"},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, status(http.StatusNotFound, "NotFound", r.URL.Path))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &gets
}

func runChainsaw(t *testing.T, server string) (time.Duration, string, error) {
	t.Helper()
	bin := os.Getenv("CHAINSAW")
	// The image build sets CHAINSAW; `make go-module-tests` reaches this module
	// too, with no patched binary to test.
	if bin == "" {
		t.Skip("CHAINSAW is not set to the chainsaw binary under test")
	}
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: %s
contexts:
- name: fake
  context:
    cluster: fake
    user: fake
current-context: fake
users:
- name: fake
  user: {}
`, server)), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(dir, "suite")
	if err := os.Mkdir(suite, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suite, "chainsaw-test.yaml"), []byte(fmt.Sprintf(`apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: transient
spec:
  namespace: default
  skipDelete: true
  steps:
  - try:
    - assert:
        timeout: %s
        resource:
          apiVersion: v1
          kind: ConfigMap
          metadata:
            name: probe
            namespace: default
          data:
            ready: "true"
`, assertTimeout)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "test", "--test-dir", suite, "--no-color")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	return time.Since(start), string(out), err
}

// A run of 500s shorter than the timeout must not fail the assert: the poll
// carries on and passes once the object comes back.
func TestAssertSurvivesTransientServerErrors(t *testing.T) {
	srv, gets := fakeAPIServer(t, 5)
	elapsed, out, err := runChainsaw(t, srv.URL)
	if err != nil {
		t.Fatalf("chainsaw failed after %s and %d GETs of the object, want a pass once the 500s stop:\n%s", elapsed, gets.Load(), out)
	}
	if gets.Load() <= 5 {
		t.Fatalf("chainsaw passed after only %d GETs of the object, so it never saw the 500s:\n%s", gets.Load(), out)
	}
}

// When the 500s never stop, the assert still fails, and it fails by its own
// timeout rather than on the first error.
func TestAssertFailsAtItsTimeoutOnPersistentServerErrors(t *testing.T) {
	srv, gets := fakeAPIServer(t, -1)
	elapsed, out, err := runChainsaw(t, srv.URL)
	if err == nil {
		t.Fatalf("chainsaw passed against an apiserver that only answers 500:\n%s", out)
	}
	if elapsed < assertTimeout || gets.Load() < 2 {
		t.Fatalf("chainsaw failed after %s and %d GETs of the object, want the %s assert timeout and more than one GET:\n%s", elapsed, gets.Load(), assertTimeout, out)
	}
}
