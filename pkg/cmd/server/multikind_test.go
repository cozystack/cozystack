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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	restclient "k8s.io/client-go/rest"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// Every apps kind is registered on the one Application Go type, and a stock
// conversion picks the first kind registered for it. Each request path must
// answer with the kind the client asked for. Every kind is exercised because
// the kinds share the process-wide scheme with other tests, so which one is
// registered first, and therefore invisible to this check, is not fixed.
func TestAppsAPIKeepsKindAcrossSharedGoType(t *testing.T) {
	rc := &config.ResourceConfig{Resources: []config.Resource{
		{
			Application: config.ApplicationConfig{Kind: "Postgres", Singular: "postgres", Plural: "postgreses"},
			Release:     config.ReleaseConfig{Prefix: "postgres-"},
		},
		redisConfig,
	}}
	ts, _ := startAppsServer(t, rc)
	dyn := dynamic.NewForConfigOrDie(&restclient.Config{Host: ts.URL})

	for _, res := range rc.Resources {
		kind, plural := res.Application.Kind, res.Application.Plural
		t.Run(kind, func(t *testing.T) {
			api := dyn.Resource(schema.GroupVersionResource{
				Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: plural,
			}).Namespace("tenant-a")
			ctx := context.Background()
			wantKind := func(op, got, want string) {
				t.Helper()
				if got != want {
					t.Errorf("%s: kind = %q, want %q", op, got, want)
				}
			}
			obj := func(replicas int64) *unstructured.Unstructured {
				return &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": appsv1alpha1.SchemeGroupVersion.String(),
					"kind":       kind,
					"metadata":   map[string]any{"name": "app", "namespace": "tenant-a"},
					"spec":       map[string]any{"replicas": replicas},
				}}
			}

			created, err := api.Create(ctx, obj(1), metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			wantKind("create", created.GetKind(), kind)

			got, err := api.Get(ctx, "app", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			wantKind("get", got.GetKind(), kind)

			list, err := api.List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			wantKind("list", list.GetKind(), kind+"List")

			w, err := api.Watch(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatalf("watch: %v", err)
			}
			defer w.Stop()

			if err := unstructured.SetNestedField(got.Object, int64(3), "spec", "replicas"); err != nil {
				t.Fatal(err)
			}
			updated, err := api.Update(ctx, got, metav1.UpdateOptions{})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			wantKind("update", updated.GetKind(), kind)

			deadline := time.After(10 * time.Second)
			for sawModified := false; !sawModified; {
				select {
				case ev := <-w.ResultChan():
					u, ok := ev.Object.(*unstructured.Unstructured)
					if !ok {
						t.Fatalf("watch: unexpected object %T", ev.Object)
					}
					wantKind("watch "+string(ev.Type), u.GetKind(), kind)
					sawModified = ev.Type == watch.Modified
				case <-deadline:
					t.Fatal("watch: no MODIFIED event")
				}
			}

			applied, err := api.Apply(ctx, "app", obj(5), metav1.ApplyOptions{FieldManager: "test", Force: true})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			wantKind("apply", applied.GetKind(), kind)

			req, err := http.NewRequest(http.MethodGet, ts.URL+"/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/"+plural, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("table: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatalf("table: read body: %v", err)
			}
			var table map[string]any
			if err := json.Unmarshal(body, &table); err != nil {
				t.Fatalf("table: decode %q: %v", body, err)
			}
			if resp.StatusCode != http.StatusOK || table["kind"] != "Table" {
				t.Errorf("table: status %d kind %v", resp.StatusCode, table["kind"])
			}
		})
	}
}
