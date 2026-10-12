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
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type nullChartCase struct {
	name, chart, rd, input, want string
	schema, values               string
	paths                        []string
}

var nullChartCases = []nullChartCase{
	{name: "nullable schema default", schema: `{"type":"object","required":["name"],"properties":{"name":{"type":"string","nullable":true,"default":"example"}}}`, values: `{"name":"example"}`, input: `{"name":null}`},
	{name: "item schema defaults", schema: `{"type":"object","properties":{"entries":{"type":"object","additionalProperties":{"type":"object","required":["name"],"properties":{"name":{"type":"string","default":"example"}}}},"items":{"type":"array","items":{"type":"object","required":["name"],"properties":{"name":{"type":"string","default":"example"}}}}}}`, values: `{"entries":{},"items":[{"name":"chart-default"}]}`, input: `{"entries":{"mine":{"name":null}},"items":[{"name":null}]}`},

	{name: "nested defaults", chart: "apps/kubernetes", rd: "kubernetes", input: `{"talos":{"registryMirrors":null,"version":null},"controlPlane":{"replicas":null,"apiServer":{"resourcesPreset":null}},"nodeGroups":{"legacy":{"instanceType":null}}}`, want: `{"talos":{},"controlPlane":{"apiServer":{}},"nodeGroups":{"legacy":{"instanceType":null}}}`, paths: []string{"controlPlane.apiServer.resourcesPreset", "controlPlane.replicas", "talos.registryMirrors", "talos.version"}},
	{name: "mariadb users", chart: "apps/mariadb", rd: "mariadb", input: `{"users":{"alice":{"password":null,"maxUserConnections":null},"blank":null}}`},
	{name: "rabbitmq vhosts", chart: "apps/rabbitmq", rd: "rabbitmq", input: `{"vhosts":{"vh1":{"roles":null},"blank":null}}`},
	{name: "seaweedfs pools", chart: "extra/seaweedfs", rd: "seaweedfs", input: `{"volume":{"pools":{"ssd":{"diskType":null},"blank":null}}}`},
	{name: "vm source http", chart: "apps/vm-disk", rd: "vm-disk", input: `{"source":{"http":{"url":null}}}`},
	{name: "vm source disk", chart: "apps/vm-disk", rd: "vm-disk", input: `{"source":{"disk":{"name":null}}}`},
	{name: "vm source image", chart: "apps/vm-disk", rd: "vm-disk", input: `{"source":{"image":{"name":null}}}`},
	{name: "optional section", chart: "apps/kubernetes", rd: "kubernetes", input: `{"talos":null,"storageClass":null}`},
}

func chartSpecSchema(t *testing.T, rd string) *structuralschema.Structural {
	t.Helper()
	raw := readEmbeddedOpenAPISchema(t, "../../../../packages/system/"+rd+"-rd/cozyrds/"+rd+".yaml")
	s, err := buildSpecSchema(raw)
	if err != nil || s == nil {
		t.Fatalf("build %s schema: %v", rd, err)
	}
	return s
}

func nullCaseSchema(t *testing.T, tt nullChartCase) *structuralschema.Structural {
	t.Helper()
	if tt.schema == "" {
		return chartSpecSchema(t, tt.rd)
	}
	schema, err := buildSpecSchema(tt.schema)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func jsonValue(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNullNormalizationAdmissionAndStorage(t *testing.T) {
	for _, tt := range nullChartCases {
		for _, verb := range []string{"create", "update", "upsert"} {
			for _, reject := range []bool{false, true} {
				t.Run(tt.name+"/"+verb+"/reject="+map[bool]string{true: "true", false: "false"}[reject], func(t *testing.T) {
					existing := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes-good-name", Namespace: "tenant-foo", Labels: map[string]string{
						ApplicationKindLabel: kubernetesKind, ApplicationGroupLabel: appsv1alpha1.GroupName, ApplicationNameLabel: "good-name",
					}}, Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(`{"unchanged":"old"}`)}}}
					r := newKubernetesWarnREST(t)
					if verb == "update" {
						r = newKubernetesWarnREST(t, existing)
					}
					r.specSchema = nullCaseSchema(t, tt)
					app := &appsv1alpha1.Application{TypeMeta: metav1.TypeMeta{Kind: kubernetesKind, APIVersion: "apps.cozystack.io/v1alpha1"}, ObjectMeta: metav1.ObjectMeta{Name: "good-name", Namespace: "tenant-foo"}, Spec: &apiextv1.JSON{Raw: []byte(tt.input)}}
					original := app.DeepCopy()
					want := tt.want
					if want == "" {
						want = tt.input
					}
					expected := jsonValue(t, []byte(want))
					rec := &fakeWarningRecorder{}
					ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), app.Namespace), rec)
					called := 0
					sentinel := errors.New("admission rejected the normalized object")
					validate := func(_ context.Context, obj runtime.Object) error {
						called++
						got, ok := obj.(*appsv1alpha1.Application)
						if !ok {
							t.Fatalf("admission got %T", obj)
						}
						if !reflect.DeepEqual(jsonValue(t, got.Spec.Raw), expected) {
							t.Errorf("admission saw %s; want %s", got.Spec.Raw, want)
						}
						if reject {
							return sentinel
						}
						return nil
					}
					updateValidate := func(ctx context.Context, obj, old runtime.Object) error {
						oldApp, ok := old.(*appsv1alpha1.Application)
						if !ok {
							t.Fatalf("old object %T", old)
						}
						if !strings.Contains(string(oldApp.Spec.Raw), `"unchanged":"old"`) {
							t.Errorf("old object changed: %s", oldApp.Spec.Raw)
						}
						return validate(ctx, obj)
					}
					var err error
					if verb == "create" {
						_, err = r.Create(ctx, app, validate, &metav1.CreateOptions{})
					} else {
						_, created, e := r.Update(ctx, app.Name, newDefaultUpdatedObjectInfo(app), validate, updateValidate, verb == "upsert", &metav1.UpdateOptions{})
						err = e
						if err == nil && created != (verb == "upsert") {
							t.Errorf("created=%t for %s", created, verb)
						}
					}
					if called != 1 {
						t.Errorf("admission calls=%d; want 1", called)
					}
					if reject {
						if !errors.Is(err, sentinel) {
							t.Errorf("rejection lost: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(app, original) {
						t.Error("caller input was mutated")
					}
					got := &helmv2.HelmRelease{}
					getErr := r.c.Get(ctx, client.ObjectKeyFromObject(existing), got)
					if reject && verb != "update" {
						all := &helmv2.HelmReleaseList{}
						if err := r.c.List(ctx, all); err != nil {
							t.Fatal(err)
						}
						if len(all.Items) != 0 {
							t.Error("rejected admission wrote a HelmRelease")
						}
					} else {
						if getErr != nil {
							t.Fatal(getErr)
						}
						storedWant := expected
						if reject {
							storedWant = jsonValue(t, existing.Spec.Values.Raw)
						}
						if !reflect.DeepEqual(jsonValue(t, got.Spec.Values.Raw), storedWant) {
							t.Errorf("stored values=%s; want %v", got.Spec.Values.Raw, storedWant)
						}
					}
					var nullWarnings []string
					for _, w := range rec.warnings {
						if strings.HasPrefix(w, "spec:") {
							nullWarnings = append(nullWarnings, w)
						}
					}
					if len(tt.paths) == 0 {
						if len(nullWarnings) != 0 {
							t.Errorf("false repair warning: %v", nullWarnings)
						}
					} else if !reflect.DeepEqual(nullWarnings, []string{emptyFieldsWarning(tt.paths)}) {
						t.Errorf("warnings=%v for removed paths=%v", nullWarnings, tt.paths)
					}
				})
			}
		}
	}
}
