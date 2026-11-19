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

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	restclient "k8s.io/client-go/rest"
	basecompatibility "k8s.io/component-base/compatibility"
	baseversion "k8s.io/component-base/version"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	cozyserver "github.com/cozystack/cozystack/pkg/cmd/server"
)

const (
	sdnGroupVersionPath  = "apis/sdn.cozystack.io/v1alpha1"
	coreGroupVersionPath = "apis/core.cozystack.io/v1alpha1"
)

// startStaticGroupsServer serves the static groups through the real handler
// chain and OpenAPI config, with storage backed by fake clients.
func startStaticGroupsServer(t *testing.T, serveSDN bool) *httptest.Server {
	t.Helper()
	corev1alpha1.RegisterStaticTypes(apiserver.Scheme)

	cfg := genericapiserver.NewConfig(apiserver.Codecs)
	cfg.ExternalAddress = "localhost:443"
	cfg.LoopbackClientConfig = &restclient.Config{}
	cfg.FeatureGate = utilfeature.DefaultMutableFeatureGate
	if baseversion.DefaultKubeBinaryVersion != "" {
		cfg.EffectiveVersion = basecompatibility.NewEffectiveVersionFromString(baseversion.DefaultKubeBinaryVersion, "", "")
	}
	cozyserver.ConfigureOpenAPI(cfg, nil, "test", "test")
	srv, err := cfg.Complete(nil).New("serve-sdn-test", genericapiserver.NewEmptyDelegate())
	if err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().Build()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := apiserver.InstallStaticAPIGroups(srv, cli, cli, dyn, serveSDN); err != nil {
		t.Fatal(err)
	}
	srv.PrepareRun()
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (int, []byte) {
	t.Helper()
	// kube-aggregator's proxy refuses to follow a backend redirect, so a 3xx
	// here must surface as the status, not be followed.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// The OpenAPI v3 index is what kube-aggregator merges across backends, so a
// group-version listed here is claimed by this server whatever the APIService
// says.
func openAPIV3Index(t *testing.T, ts *httptest.Server) map[string]string {
	t.Helper()
	code, body := get(t, ts, "/openapi/v3")
	if code != http.StatusOK {
		t.Fatalf("GET /openapi/v3: %d %s", code, body)
	}
	var index struct {
		Paths map[string]struct {
			ServerRelativeURL string `json:"serverRelativeURL"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatal(err)
	}
	urls := map[string]string{}
	for k, v := range index.Paths {
		urls[k] = v.ServerRelativeURL
	}
	return urls
}

func TestServeSDNOffKeepsGroupOutOfOpenAPI(t *testing.T) {
	ts := startStaticGroupsServer(t, false)

	index := openAPIV3Index(t, ts)
	if _, ok := index[coreGroupVersionPath]; !ok {
		t.Fatalf("OpenAPI v3 index lacks %s: %v", coreGroupVersionPath, index)
	}
	for k := range index {
		if strings.HasPrefix(k, "apis/sdn.cozystack.io") {
			t.Errorf("OpenAPI v3 index lists %s with --serve-sdn=false", k)
		}
	}

	code, body := get(t, ts, "/openapi/v2")
	if code != http.StatusOK {
		t.Fatalf("GET /openapi/v2: %d %s", code, body)
	}
	var v2 struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &v2); err != nil {
		t.Fatal(err)
	}
	coreListed := false
	for p := range v2.Paths {
		if strings.HasPrefix(p, "/apis/core.cozystack.io/") {
			coreListed = true
		}
		if strings.HasPrefix(p, "/apis/sdn.cozystack.io") {
			t.Errorf("OpenAPI v2 lists %s with --serve-sdn=false", p)
		}
	}
	if !coreListed {
		t.Fatalf("OpenAPI v2 lists no core.cozystack.io path: %d paths", len(v2.Paths))
	}

	if code, _ := get(t, ts, "/apis/sdn.cozystack.io/v1alpha1"); code != http.StatusNotFound {
		t.Errorf("GET /apis/sdn.cozystack.io/v1alpha1 = %d, want 404", code)
	}
}

func TestServeSDNOnPublishesSecurityGroupSchema(t *testing.T) {
	ts := startStaticGroupsServer(t, true)

	url, ok := openAPIV3Index(t, ts)[sdnGroupVersionPath]
	if !ok {
		t.Fatalf("OpenAPI v3 index lacks %s", sdnGroupVersionPath)
	}
	code, body := get(t, ts, url)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, code, body)
	}
	if !strings.Contains(string(body), `"/apis/sdn.cozystack.io/v1alpha1/namespaces/{namespace}/securitygroups"`) {
		t.Errorf("%s has no securitygroups path", url)
	}

	// A stale hash is answered with a redirect; this is the response
	// kube-aggregator rejects when it routes a hash taken from one backend's
	// index to the other backend.
	stale := "/openapi/v3/" + sdnGroupVersionPath + "?hash=0"
	if code, _ := get(t, ts, stale); code != http.StatusMovedPermanently {
		t.Errorf("GET %s = %d, want 301", stale, code)
	}
}
