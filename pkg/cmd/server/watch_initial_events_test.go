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
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	restclient "k8s.io/client-go/rest"
	basecompatibility "k8s.io/component-base/compatibility"
	baseversion "k8s.io/component-base/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	cozyserver "github.com/cozystack/cozystack/pkg/cmd/server"
	"github.com/cozystack/cozystack/pkg/config"
	applicationstorage "github.com/cozystack/cozystack/pkg/registry/apps/application"
)

// startAppsServer serves the apps group through the real apiserver handler
// chain, with the application storage backed by a fake client.
func startAppsServer(t *testing.T) (*httptest.Server, client.Client) {
	t.Helper()
	rc := &config.ResourceConfig{Resources: []config.Resource{{
		Application: config.ApplicationConfig{Kind: "Redis", Singular: "redis", Plural: "redises"},
		Release:     config.ReleaseConfig{Prefix: "redis-"},
	}}}
	if err := appsv1alpha1.RegisterDynamicTypes(apiserver.Scheme, rc); err != nil {
		t.Fatal(err)
	}

	backing := runtime.NewScheme()
	if err := helmv2.AddToScheme(backing); err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().WithScheme(backing).Build()

	cfg := genericapiserver.NewConfig(apiserver.Codecs)
	cfg.ExternalAddress = "localhost:443"
	cfg.LoopbackClientConfig = &restclient.Config{}
	cfg.FeatureGate = utilfeature.DefaultMutableFeatureGate
	if baseversion.DefaultKubeBinaryVersion != "" {
		cfg.EffectiveVersion = basecompatibility.NewEffectiveVersionFromString(baseversion.DefaultKubeBinaryVersion, "", "")
	}
	cozyserver.ConfigureOpenAPI(cfg, cozyserver.KindSchemasFromConfig(rc), "test", "test")
	srv, err := cfg.Complete(nil).New("watch-initial-events-test", genericapiserver.NewEmptyDelegate())
	if err != nil {
		t.Fatal(err)
	}
	storage := map[string]rest.Storage{
		"redises": applicationstorage.NewREST(cli, cli, &rc.Resources[0]),
	}
	if err := apiserver.InstallAppsAPIGroup(srv, storage); err != nil {
		t.Fatal(err)
	}
	srv.PrepareRun()
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, cli
}

// watchStream opens a watch with the given query, then creates and updates a
// release behind it, and returns the stream lines received within the timeout.
func watchStream(t *testing.T, ts *httptest.Server, cli client.Client, query string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/redises?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: status %d", resp.StatusCode)
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name:      "redis-cache",
		Namespace: "tenant-a",
		Labels: map[string]string{
			applicationstorage.ApplicationKindLabel:  "Redis",
			applicationstorage.ApplicationGroupLabel: appsv1alpha1.GroupName,
		},
	}}
	if err := cli.Create(context.Background(), hr); err != nil {
		t.Fatalf("create HelmRelease: %v", err)
	}
	// The storage sends a pending initial-events-end bookmark ahead of the
	// first live event, so drive one.
	hr.Annotations = map[string]string{"touched": "1"}
	if err := cli.Update(context.Background(), hr); err != nil {
		t.Fatalf("update HelmRelease: %v", err)
	}

	var out []string
	for line := range lines {
		out = append(out, line)
	}
	return out
}

// A plain watch has SendInitialEvents defaulted on by the handler but no
// bookmarks; from k8s.io/apiserver v0.36 the handler panics if the storage
// answers it with the initial-events-end bookmark, so none may appear.
func TestWatchHandler_PlainWatchGetsNoInitialEventsEndBookmark(t *testing.T) {
	ts, cli := startAppsServer(t)
	lines := watchStream(t, ts, cli, "watch=true")
	sawModified := false
	for _, l := range lines {
		if strings.Contains(l, metav1.InitialEventsAnnotationKey) {
			t.Fatalf("plain watch received the initial-events-end bookmark: %s", l)
		}
		sawModified = sawModified || strings.Contains(l, `"type":"MODIFIED"`)
	}
	if !sawModified {
		t.Fatalf("plain watch saw no MODIFIED event, stream: %v", lines)
	}
}

func TestWatchHandler_WatchListGetsInitialEventsEndBookmark(t *testing.T) {
	ts, cli := startAppsServer(t)
	lines := watchStream(t, ts, cli, "watch=true&sendInitialEvents=true&allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan")
	for _, l := range lines {
		if strings.Contains(l, `"type":"BOOKMARK"`) && strings.Contains(l, metav1.InitialEventsAnnotationKey) {
			return
		}
	}
	t.Fatalf("watch list received no initial-events-end bookmark, stream: %v", lines)
}
