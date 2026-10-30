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

package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	restclient "k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// configMapServer answers just enough of the API to run the cache: discovery,
// a list of the defaults ConfigMap and a watch the test pushes events into.
type configMapServer struct {
	mu       sync.Mutex
	items    []corev1.ConfigMap
	listURLs []url.Values
	events   chan []byte
}

func (s *configMapServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api":
		_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
	case "/apis":
		_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
	case "/api/v1":
		_, _ = w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"configmaps","singularName":"configmap","namespaced":true,"kind":"ConfigMap","verbs":["get","list","watch"]}]}`))
	case "/api/v1/namespaces/" + InstallationDefaultsNamespace + "/configmaps":
		s.mu.Lock()
		s.listURLs = append(s.listURLs, r.URL.Query())
		s.mu.Unlock()
		if r.URL.Query().Get("watch") == "true" {
			s.serveWatch(w, r)
			return
		}
		s.mu.Lock()
		list := corev1.ConfigMapList{
			TypeMeta: metav1.TypeMeta{Kind: "ConfigMapList", APIVersion: "v1"},
			ListMeta: metav1.ListMeta{ResourceVersion: "1"},
			Items:    s.items,
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(list)
	default:
		http.NotFound(w, r)
	}
}

// serveWatch streams the initial state when the client asks for it (the reflector
// lists through a watch with sendInitialEvents), closes it with the bookmark that
// marks the end of the list, then forwards the events the test pushes.
func (s *configMapServer) serveWatch(w http.ResponseWriter, r *http.Request) {
	flusher := w.(http.Flusher)
	w.WriteHeader(http.StatusOK)
	send := func(event any) {
		line, _ := json.Marshal(event)
		_, _ = w.Write(append(line, '\n'))
		flusher.Flush()
	}
	if r.URL.Query().Get("sendInitialEvents") == "true" {
		s.mu.Lock()
		items := append([]corev1.ConfigMap(nil), s.items...)
		s.mu.Unlock()
		for _, item := range items {
			send(map[string]any{"type": "ADDED", "object": item})
		}
		send(map[string]any{"type": "BOOKMARK", "object": corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				ResourceVersion: "1",
				Annotations:     map[string]string{metav1.InitialEventsAnnotationKey: "true"},
			},
		}})
	}
	flusher.Flush()
	for {
		select {
		case line := <-s.events:
			_, _ = w.Write(append(line, '\n'))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func defaultsConfigMapAt(version, payload string) corev1.ConfigMap {
	return corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       InstallationDefaultsNamespace,
			Name:            InstallationDefaultsName,
			ResourceVersion: version,
		},
		Data: map[string]string{"defaults.json": payload},
	}
}

func startDefaultsCache(t *testing.T, server *configMapServer) client.Reader {
	t.Helper()
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := NewInstallationDefaultsCache(&restclient.Config{Host: ts.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := c.GetInformer(ctx, &corev1.ConfigMap{}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Start(ctx) }()
	syncCtx, syncCancel := context.WithTimeout(ctx, 10*time.Second)
	defer syncCancel()
	if !c.WaitForCacheSync(syncCtx) {
		t.Fatal("the defaults cache did not sync")
	}
	return c
}

func TestInstallationDefaultsCacheWatchesOnlyTheDefaultsConfigMap(t *testing.T) {
	server := &configMapServer{events: make(chan []byte, 1)}
	reader := startDefaultsCache(t, server)

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.listURLs) == 0 {
		t.Fatal("the cache never listed ConfigMaps in the defaults namespace")
	}
	if got, want := server.listURLs[0].Get("fieldSelector"), "metadata.name="+InstallationDefaultsName; got != want {
		t.Fatalf("fieldSelector = %q, want %q: the cache would hold every ConfigMap in the namespace", got, want)
	}
	err := reader.Get(context.Background(), client.ObjectKey{Namespace: InstallationDefaultsNamespace, Name: InstallationDefaultsName}, &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("a missing ConfigMap must read as NotFound so creation continues without defaults, got %v", err)
	}
}

func TestInstallationDefaultsCacheFollowsChanges(t *testing.T) {
	server := &configMapServer{
		items:  []corev1.ConfigMap{defaultsConfigMapAt("1", `{"widgets":{"size":"10Gi"}}`)},
		events: make(chan []byte, 1),
	}
	reader := startDefaultsCache(t, server)
	key := client.ObjectKey{Namespace: InstallationDefaultsNamespace, Name: InstallationDefaultsName}

	cm := &corev1.ConfigMap{}
	if err := reader.Get(context.Background(), key, cm); err != nil {
		t.Fatal(err)
	}
	if got := cm.Data["defaults.json"]; got != `{"widgets":{"size":"10Gi"}}` {
		t.Fatalf("initial payload = %q", got)
	}

	updated, err := json.Marshal(map[string]any{"type": "MODIFIED", "object": defaultsConfigMapAt("2", `{"widgets":{"size":"20Gi"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	server.events <- updated
	err = wait.PollUntilContextTimeout(context.Background(), 20*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		current := &corev1.ConfigMap{}
		if err := reader.Get(ctx, key, current); err != nil {
			return false, err
		}
		return current.Data["defaults.json"] == `{"widgets":{"size":"20Gi"}}`, nil
	})
	if err != nil {
		t.Fatalf("a change made while the API runs never reached the cache: %v", err)
	}
}
