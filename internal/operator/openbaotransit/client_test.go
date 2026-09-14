/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package openbaotransit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	c, err := New(srv.URL, "root-token", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestEnsureTransitKey(t *testing.T) {
	var gotPath, gotToken string
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Vault-Token")
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	if err := c.EnsureTransitKey(context.Background(), "tenant-test-test"); err != nil {
		t.Fatalf("EnsureTransitKey: %v", err)
	}
	if gotPath != "/v1/transit/keys/tenant-test-test" {
		t.Errorf("path = %q", gotPath)
	}
	if gotToken != "root-token" {
		t.Errorf("token header = %q", gotToken)
	}
}

func TestEnsureTransitKeyError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"errors":["permission denied"]}`)
	})
	defer srv.Close()
	if err := c.EnsureTransitKey(context.Background(), "k"); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestWriteUnsealPolicy(t *testing.T) {
	var body map[string]string
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	if err := c.WriteUnsealPolicy(context.Background(), "tenant-test-test-unseal", "tenant-test-test"); err != nil {
		t.Fatalf("WriteUnsealPolicy: %v", err)
	}
	pol := body["policy"]
	if !strings.Contains(pol, `transit/encrypt/tenant-test-test`) || !strings.Contains(pol, `transit/decrypt/tenant-test-test`) {
		t.Errorf("policy missing encrypt/decrypt paths: %q", pol)
	}
}

func TestEnsureKubernetesAuthRole(t *testing.T) {
	var gotPath string
	var body map[string]any
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	if err := c.EnsureKubernetesAuthRole(context.Background(), "tenant-test-test", "test", "tenant-test", "tenant-test-test-unseal", "72h"); err != nil {
		t.Fatalf("EnsureKubernetesAuthRole: %v", err)
	}
	if gotPath != "/v1/auth/kubernetes/role/tenant-test-test" {
		t.Errorf("path = %q", gotPath)
	}
	if got := body["bound_service_account_names"].([]any); got[0].(string) != "test" {
		t.Errorf("bound SA names = %v", got)
	}
	if got := body["bound_service_account_namespaces"].([]any); got[0].(string) != "tenant-test" {
		t.Errorf("bound SA namespaces = %v", got)
	}
	if got := body["token_policies"].([]any); got[0].(string) != "tenant-test-test-unseal" {
		t.Errorf("token_policies = %v", got)
	}
}
