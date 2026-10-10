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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	restclient "k8s.io/client-go/rest"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

const (
	appsDefPrefix = "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1."
	redisPath     = "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/redises"
	redisItemPath = "/apis/apps.cozystack.io/v1alpha1/namespaces/{namespace}/redises/{name}"
)

func redisAPI(t *testing.T, ts *httptest.Server) dynamic.ResourceInterface {
	t.Helper()
	return dynamic.NewForConfigOrDie(&restclient.Config{Host: ts.URL}).Resource(schema.GroupVersionResource{
		Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "redises",
	}).Namespace("tenant-a")
}

func redisObj(replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": appsv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Redis",
		"metadata":   map[string]any{"name": "app", "namespace": "tenant-a"},
		"spec":       map[string]any{"replicas": replicas},
	}}
}

func getWithAccept(t *testing.T, url, accept string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("GET %s: read body: %v", url, err)
	}
	return resp, body
}

// The application storage keeps no managedFields of its own: the object is
// rebuilt from the HelmRelease on every read. Server-side apply therefore
// sees no prior owner and never reports a conflict between field managers,
// and the last apply wins.
func TestAppsAPIServerSideApplyTracksNoFieldOwners(t *testing.T) {
	ts, _ := startAppsServer(t, &config.ResourceConfig{Resources: []config.Resource{redisConfig}})
	api := redisAPI(t, ts)
	ctx := context.Background()

	if _, err := api.Create(ctx, redisObj(0), metav1.CreateOptions{FieldManager: "creator"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.Apply(ctx, "app", redisObj(1), metav1.ApplyOptions{FieldManager: "manager-a"}); err != nil {
		t.Fatalf("apply from manager-a: %v", err)
	}
	if _, err := api.Apply(ctx, "app", redisObj(2), metav1.ApplyOptions{FieldManager: "manager-b"}); err != nil {
		t.Fatalf("non-forced apply from manager-b over manager-a's field: %v", err)
	}

	got, err := api.Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); r != 2 {
		t.Errorf("spec.replicas = %d, want 2 from the last apply", r)
	}
	if mf := got.GetManagedFields(); len(mf) != 0 {
		t.Errorf("managedFields = %+v, want none", mf)
	}
}

// Only JSON and YAML are served. CBOR is refused with 406 and a client that
// lists JSON after it gets JSON. Protobuf is negotiated before the encoder
// finds out that Application has no protobuf form, so the JSON fallback is
// never reached and the client gets 406 as well.
func TestAppsAPIContentNegotiation(t *testing.T) {
	ts, _ := startAppsServer(t, &config.ResourceConfig{Resources: []config.Resource{redisConfig}})
	if _, err := redisAPI(t, ts).Create(context.Background(), redisObj(1), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, tc := range []struct {
		accept, wantType string
		wantStatus       int
	}{
		{"application/json", "application/json", http.StatusOK},
		{"application/yaml", "application/yaml", http.StatusOK},
		{"application/cbor", "", http.StatusNotAcceptable},
		{"application/vnd.kubernetes.protobuf", "", http.StatusNotAcceptable},
		{"application/cbor, application/json;q=0.9", "application/json", http.StatusOK},
		{"application/vnd.kubernetes.protobuf, application/json;q=0.9", "", http.StatusNotAcceptable},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			resp, body := getWithAccept(t, ts.URL+redisPath+"/app", tc.accept)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body %q", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantType != "" && resp.Header.Get("Content-Type") != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), tc.wantType)
			}
		})
	}
}

// kubectl get --watch asks for Table output; every event must carry a Table
// with the row of the changed object.
func TestAppsAPIWatchAsTable(t *testing.T) {
	ts, _ := startAppsServer(t, &config.ResourceConfig{Resources: []config.Resource{redisConfig}})
	api := redisAPI(t, ts)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+redisPath+"?watch=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: status %d", resp.StatusCode)
	}

	if _, err := api.Create(context.Background(), redisObj(1), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	if !sc.Scan() {
		t.Fatalf("watch: no event: %v", sc.Err())
	}
	var ev struct {
		Type   string `json:"type"`
		Object struct {
			Kind              string `json:"kind"`
			APIVersion        string `json:"apiVersion"`
			ColumnDefinitions []struct {
				Name string `json:"name"`
			} `json:"columnDefinitions"`
			Rows []struct {
				Cells []any `json:"cells"`
			} `json:"rows"`
		} `json:"object"`
	}
	if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
		t.Fatalf("decode %q: %v", sc.Bytes(), err)
	}
	if ev.Type != "ADDED" || ev.Object.Kind != "Table" || ev.Object.APIVersion != "meta.k8s.io/v1" {
		t.Fatalf("event = %s %s %s, want ADDED meta.k8s.io/v1 Table", ev.Type, ev.Object.APIVersion, ev.Object.Kind)
	}
	if len(ev.Object.Rows) != 1 || len(ev.Object.Rows[0].Cells) == 0 || ev.Object.Rows[0].Cells[0] != "app" {
		t.Fatalf("rows = %+v, want one row for app", ev.Object.Rows)
	}
	if len(ev.Object.ColumnDefinitions) == 0 || ev.Object.ColumnDefinitions[0].Name != "NAME" {
		t.Errorf("columnDefinitions = %+v, want NAME first", ev.Object.ColumnDefinitions)
	}
}

type openAPISchema struct {
	Ref                  string                   `json:"$ref"`
	Type                 any                      `json:"type"`
	Properties           map[string]openAPISchema `json:"properties"`
	Items                *openAPISchema           `json:"items"`
	AnyOf                []openAPISchema          `json:"anyOf"`
	PreserveUnknown      bool                     `json:"x-kubernetes-preserve-unknown-fields"`
	IntOrString          bool                     `json:"x-kubernetes-int-or-string"`
	GroupVersionKind     []map[string]string      `json:"x-kubernetes-group-version-kind"`
	AdditionalProperties any                      `json:"additionalProperties"`
}

var openAPIKinds = &config.ResourceConfig{Resources: []config.Resource{
	{
		Application: config.ApplicationConfig{
			Kind: "Postgres", Singular: "postgres", Plural: "postgreses",
			OpenAPISchema: `{"type":"object","properties":{"replicas":{"type":"integer"},"size":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}`,
		},
		Release: config.ReleaseConfig{Prefix: "postgres-"},
	},
	redisConfig,
}}

// checkKindDefinitions asserts what the OpenAPI post-processors in openapi.go
// make of the shared Application schemas: one object, list and status
// definition per kind with its own GVK, list items pointing at the kind, the
// kind's spec schema published open, and the base Application definitions
// gone.
func checkKindDefinitions(t *testing.T, defs map[string]openAPISchema, refPrefix string) {
	t.Helper()
	for _, base := range []string{"Application", "ApplicationList", "ApplicationStatus"} {
		if _, ok := defs[appsDefPrefix+base]; ok {
			t.Errorf("base definition %s still published", base)
		}
	}
	for _, kind := range []string{"Postgres", "Redis"} {
		obj, ok := defs[appsDefPrefix+kind]
		if !ok {
			t.Errorf("%s: no definition", kind)
			continue
		}
		if len(obj.GroupVersionKind) != 1 || obj.GroupVersionKind[0]["kind"] != kind || obj.GroupVersionKind[0]["group"] != appsv1alpha1.GroupName {
			t.Errorf("%s: gvk = %v", kind, obj.GroupVersionKind)
		}
		if !obj.Properties["spec"].PreserveUnknown {
			t.Errorf("%s: spec is not published open: %+v", kind, obj.Properties["spec"])
		}
		if ref := obj.Properties["status"].Ref; ref != refPrefix+appsDefPrefix+kind+"Status" {
			t.Errorf("%s: status $ref = %q", kind, ref)
		}
		if _, ok := defs[appsDefPrefix+kind+"Status"]; !ok {
			t.Errorf("%s: no status definition", kind)
		}
		list, ok := defs[appsDefPrefix+kind+"List"]
		if !ok {
			t.Errorf("%s: no list definition", kind)
			continue
		}
		if len(list.GroupVersionKind) != 1 || list.GroupVersionKind[0]["kind"] != kind+"List" {
			t.Errorf("%sList: gvk = %v", kind, list.GroupVersionKind)
		}
		if items := list.Properties["items"].Items; items == nil || items.Ref != refPrefix+appsDefPrefix+kind {
			t.Errorf("%sList: items = %+v", kind, items)
		}
	}
	if _, ok := defs[appsDefPrefix+"Postgres"].Properties["spec"].Properties["replicas"]; !ok {
		t.Errorf("Postgres spec lost the declared replicas property")
	}
}

func TestAppsAPIOpenAPIV2(t *testing.T) {
	ts, _ := startAppsServer(t, openAPIKinds)
	resp, body := getWithAccept(t, ts.URL+"/openapi/v2", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var doc struct {
		Definitions map[string]openAPISchema              `json:"definitions"`
		Paths       map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	checkKindDefinitions(t, doc.Definitions, "#/definitions/")

	size := doc.Definitions[appsDefPrefix+"Postgres"].Properties["spec"].Properties["size"]
	if len(size.AnyOf) != 0 || !size.IntOrString || size.Type != "string" {
		t.Errorf("v2 int-or-string anyOf not flattened: %+v", size)
	}

	raw, ok := doc.Paths[redisItemPath]["patch"]
	if !ok {
		t.Fatal("no PATCH operation for a redis")
	}
	var patch struct {
		Consumes []string `json:"consumes"`
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(patch.Consumes, "application/strategic-merge-patch+json") || !slices.Contains(patch.Consumes, "application/merge-patch+json") {
		t.Errorf("PATCH consumes = %v, want merge-patch without strategic-merge-patch", patch.Consumes)
	}
}

func TestAppsAPIOpenAPIV3(t *testing.T) {
	ts, _ := startAppsServer(t, openAPIKinds)
	resp, body := getWithAccept(t, ts.URL+"/openapi/v3/apis/apps.cozystack.io/v1alpha1", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var doc struct {
		Components struct {
			Schemas map[string]openAPISchema `json:"schemas"`
		} `json:"components"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	checkKindDefinitions(t, doc.Components.Schemas, "#/components/schemas/")

	if size := doc.Components.Schemas[appsDefPrefix+"Postgres"].Properties["spec"].Properties["size"]; len(size.AnyOf) != 2 {
		t.Errorf("v3 must keep the int-or-string anyOf: %+v", size)
	}

	raw, ok := doc.Paths[redisItemPath]["patch"]
	if !ok {
		t.Fatal("no PATCH operation for a redis")
	}
	var patch struct {
		RequestBody struct {
			Content map[string]any `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	var types []string
	for ct := range patch.RequestBody.Content {
		types = append(types, ct)
	}
	if slices.Contains(types, "application/strategic-merge-patch+json") || !slices.Contains(types, "application/merge-patch+json") {
		t.Errorf("PATCH request body types = %v, want merge-patch without strategic-merge-patch", types)
	}
}
