/*
Copyright 2026 The Cozystack Authors.

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

package vyos_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/vyos"
)

const testAPIKey = "test-token"

// fakeServer wires an httptest server with a single handler and returns
// a Client pointed at it.
func fakeServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *vyos.Client) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := vyos.NewClient(srv.URL, testAPIKey, vyos.WithHTTPClient(srv.Client()))

	return srv, client
}

// extractForm parses the form body of a VyOS request and returns the
// JSON value of the "data" field and the value of the "key" field.
func extractForm(t *testing.T, r *http.Request) (string, string) {
	t.Helper()

	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}

	if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("expected form Content-Type, got %q", got)
	}

	return r.PostForm.Get("data"), r.PostForm.Get("key")
}

func TestConfigure_SendsBatchedOperations(t *testing.T) {
	t.Parallel()

	var seenData string
	_, client := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configure" {
			t.Errorf("expected /configure, got %s", r.URL.Path)
		}

		data, key := extractForm(t, r)

		if key != testAPIKey {
			t.Errorf("expected key=%q, got %q", testAPIKey, key)
		}

		seenData = data

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":""}`))
	})

	ops := []vyos.Operation{
		{Op: vyos.OpSet, Path: []string{"vpn", "ipsec", "site-to-site", "peer", "203.0.113.10", "authentication", "mode"}, Value: "pre-shared-secret"},
		{Op: vyos.OpDelete, Path: []string{"protocols", "bgp"}},
	}

	if err := client.Configure(context.Background(), ops); err != nil {
		t.Fatalf("Configure returned error: %v", err)
	}

	var got []vyos.Operation
	if err := json.Unmarshal([]byte(seenData), &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(got))
	}

	if got[0].Op != vyos.OpSet || got[0].Value != "pre-shared-secret" {
		t.Errorf("unexpected first op: %+v", got[0])
	}

	if got[1].Op != vyos.OpDelete {
		t.Errorf("unexpected second op: %+v", got[1])
	}

	if got[1].Value != "" {
		t.Errorf("delete op should have empty Value, got %q", got[1].Value)
	}
}

func TestConfigure_NoOpOnEmptyBatch(t *testing.T) {
	t.Parallel()

	called := false
	_, client := fakeServer(t, func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})

	if err := client.Configure(context.Background(), nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if called {
		t.Errorf("expected no HTTP call for empty batch")
	}
}

func TestConfigure_PropagatesVyOSError(t *testing.T) {
	t.Parallel()

	_, client := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error":"set: configuration commit failed"}`))
	})

	err := client.Configure(context.Background(), []vyos.Operation{{Op: vyos.OpSet, Path: []string{"x"}}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "commit failed") {
		t.Errorf("expected error to wrap VyOS message, got: %v", err)
	}
}

func TestConfigure_FailsOnHTTPError(t *testing.T) {
	t.Parallel()

	_, client := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	err := client.Configure(context.Background(), []vyos.Operation{{Op: vyos.OpSet, Path: []string{"x"}}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "http 500") {
		t.Errorf("expected error to mention http 500, got: %v", err)
	}
}

func TestShow_ReturnsRawData(t *testing.T) {
	t.Parallel()

	_, client := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show" {
			t.Errorf("expected /show, got %s", r.URL.Path)
		}

		data, _ := extractForm(t, r)
		if !strings.Contains(data, `"op":"show"`) {
			t.Errorf("expected op=show in payload, got %s", data)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":"OK\n"}`))
	})

	data, err := client.Show(context.Background(), []string{"vpn", "ipsec", "sa"})
	if err != nil {
		t.Fatalf("Show returned error: %v", err)
	}

	if !strings.Contains(string(data), "OK") {
		t.Errorf("unexpected data: %s", string(data))
	}
}

func TestRetrieve_UsesShowConfigOp(t *testing.T) {
	t.Parallel()

	var seenOp string
	_, client := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/retrieve" {
			t.Errorf("expected /retrieve, got %s", r.URL.Path)
		}

		data, _ := extractForm(t, r)

		var payload struct {
			Op string `json:"op"`
		}

		_ = json.Unmarshal([]byte(data), &payload)
		seenOp = payload.Op

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})

	if _, err := client.Retrieve(context.Background(), []string{"vpn"}); err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}

	if seenOp != "showConfig" {
		t.Errorf("expected op=showConfig, got %q", seenOp)
	}
}

func TestPost_HandlesUnparsableBody(t *testing.T) {
	t.Parallel()

	_, client := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	})

	err := client.Configure(context.Background(), []vyos.Operation{{Op: vyos.OpSet, Path: []string{"x"}}})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}

	if !strings.Contains(err.Error(), "decode envelope") {
		t.Errorf("expected decode-envelope error, got: %v", err)
	}
}

func TestShowInterfacesDetail_ParsesEthernetObservations(t *testing.T) {
	t.Parallel()

	var seenPath string
	_, client := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show" {
			t.Errorf("expected /show, got %s", r.URL.Path)
		}

		data, _ := extractForm(t, r)

		var payload struct {
			Op   string   `json:"op"`
			Path []string `json:"path"`
		}

		_ = json.Unmarshal([]byte(data), &payload)
		seenPath = strings.Join(payload.Path, " ")

		body := "eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP\n" +
			"    link/ether 52:54:00:de:ad:01 brd ff:ff:ff:ff:ff:ff\n" +
			"eth1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP\n" +
			"    link/ether 52:54:00:de:ad:02 brd ff:ff:ff:ff:ff:ff\n"

		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":` + string(encoded) + `}`))
	})

	obs, err := client.ShowInterfacesDetail(context.Background())
	if err != nil {
		t.Fatalf("ShowInterfacesDetail returned error: %v", err)
	}

	if seenPath != "interfaces detail" {
		t.Errorf("expected path 'interfaces detail', got %q", seenPath)
	}

	if len(obs) != 2 {
		t.Fatalf("expected 2 observations, got %d: %+v", len(obs), obs)
	}

	if obs[0].Device != "eth0" || obs[0].MAC != "52:54:00:de:ad:01" {
		t.Errorf("first observation wrong: %+v", obs[0])
	}
}

func TestShowInterfacesDetail_EmptyOnNullData(t *testing.T) {
	t.Parallel()

	_, client := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	})

	obs, err := client.ShowInterfacesDetail(context.Background())
	if err != nil {
		t.Fatalf("ShowInterfacesDetail returned error: %v", err)
	}

	if len(obs) != 0 {
		t.Errorf("expected 0 observations, got %d", len(obs))
	}
}
