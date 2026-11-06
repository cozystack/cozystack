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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type nodeDefaultsRender struct{ name, diskURL, script string }

func nodeDefaultsREST(t *testing.T, objects ...client.Object) *REST {
	t.Helper()
	raw, err := os.ReadFile("../../../../packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var definition struct {
		Spec struct {
			Application config.ApplicationConfig
			Release     struct{ Prefix string }
		}
	}
	if err := yaml.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	r := defaultsREST(t, objects...)
	return NewREST(r.c, r.w, r.c, &config.Resource{
		Application: definition.Spec.Application,
		Release:     config.ReleaseConfig{Prefix: definition.Spec.Release.Prefix},
	})
}

func renderStoredNodeDefaults(t *testing.T, values *apiextv1.JSON) nodeDefaultsRender {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for the render integration test")
	}
	path := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(path, values.Raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("helm", "template", "kubernetes-nodes-demo-md0", "../../../../packages/apps/kubernetes-nodes",
		"--namespace", "tenant-test", "--values", path, "--set", "_cluster.cluster-domain=cozy.local")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("render stored values: %v\n%s", err, stderr.String())
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(raw)), 4096)
	var result nodeDefaultsRender
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc["kind"] == "KubevirtMachineTemplate" {
			result.name, _, _ = unstructured.NestedString(doc, "metadata", "name")
			volumes, _, err := unstructured.NestedSlice(doc, "spec", "template", "spec", "virtualMachineTemplate", "spec", "dataVolumeTemplates")
			if err != nil || len(volumes) != 1 {
				t.Fatalf("disk templates: %v, %v", volumes, err)
			}
			result.diskURL, _, _ = unstructured.NestedString(volumes[0].(map[string]any), "spec", "source", "http", "url")
		}
		if doc["kind"] == "Job" {
			containers, _, _ := unstructured.NestedSlice(doc, "spec", "template", "spec", "containers")
			for _, container := range containers {
				commands, _, _ := unstructured.NestedSlice(container.(map[string]any), "command")
				for _, command := range commands {
					if text, ok := command.(string); ok && strings.Contains(text, "TalosConfigTemplate") {
						result.script = text
					}
				}
			}
		}
	}
	if result.name == "" || result.diskURL == "" || result.script == "" {
		t.Fatalf("missing rendered node resources: %+v", result)
	}
	return result
}

func TestInstallationDefaultsKubernetesNodesPersistedRender(t *testing.T) {
	r := nodeDefaultsREST(t, defaultsConfig(`{"kubernetesnodeses":{"talos":{"imageFactoryURL":"https://factory.example.com","installerRepository":"registry.example.com/installer","registryMirrors":{"ghcr.io":{"endpoints":["https://registry.example.com"]}}}}}`))
	ctx := request.WithNamespace(context.Background(), "tenant-test")
	app := defaultsApp(`{"cluster":"demo","version":"v1.35","instanceType":"","resources":{"cpu":"2","memory":"4Gi"}}`)
	app.Name = "demo-md0"
	if _, err := r.Create(ctx, app, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: "tenant-test", Name: "kubernetes-nodes-demo-md0"}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, key, hr); err != nil {
		t.Fatal(err)
	}
	before := renderStoredNodeDefaults(t, hr.Spec.Values)
	if !strings.HasPrefix(before.diskURL, "https://factory.example.com/image/") ||
		!strings.Contains(before.script, "registry.example.com/installer") ||
		!strings.Contains(before.script, "https://registry.example.com") {
		t.Fatalf("stored defaults not rendered: %+v", before)
	}
	cm := &corev1.ConfigMap{}
	if err := r.c.Get(ctx, client.ObjectKeyFromObject(defaultsConfig("")), cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["defaults.json"] = `{"kubernetesnodeses":{"talos":{"imageFactoryURL":"https://new-factory.example.com","installerRepository":"new-registry.example.com/installer"}}}`
	if err := r.c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	obj, err := r.Get(ctx, app.Name, &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(obj), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Get(ctx, key, hr); err != nil {
		t.Fatal(err)
	}
	after := renderStoredNodeDefaults(t, hr.Spec.Values)
	if before.name != after.name || before.diskURL != after.diskURL || before.script != after.script {
		t.Fatalf("existing pool changed after configuration update: before=%+v after=%+v", before, after)
	}
	app.Name = "demo-new"
	created, err := r.Create(ctx, app, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	values := jsonValue(t, created.(*appsv1alpha1.Application).Spec.Raw).(map[string]any)
	talos := values["talos"].(map[string]any)
	if talos["imageFactoryURL"] != "https://new-factory.example.com" {
		t.Fatalf("new pool did not use new defaults: %v", talos)
	}
}

// A pool that declares its own mirrors keeps exactly those. Merging would hand it
// the platform's other registries and add the platform's fields to its docker.io
// entry, an entry neither side wrote.
func TestInstallationDefaultsKubernetesNodesMirrorsReplaceAsAWhole(t *testing.T) {
	r := nodeDefaultsREST(t, defaultsConfig(`{"kubernetesnodeses":{"talos":{"imageFactoryURL":"https://factory.example.com","registryMirrors":{"docker.io":{"endpoints":["https://platform.example.com"],"skipFallback":true},"ghcr.io":{"endpoints":["https://platform.example.com"]}}}}}`))
	ctx := request.WithNamespace(context.Background(), "tenant-test")
	app := defaultsApp(`{"cluster":"demo","version":"v1.35","instanceType":"","resources":{"cpu":"2","memory":"4Gi"},"talos":{"registryMirrors":{"docker.io":{"endpoints":["https://pool.example.com"]}}}}`)
	app.Name = "demo-md0"
	created, err := r.Create(ctx, app, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	talos := jsonValue(t, created.(*appsv1alpha1.Application).Spec.Raw).(map[string]any)["talos"].(map[string]any)
	if talos["imageFactoryURL"] != "https://factory.example.com" {
		t.Fatalf("the entry was skipped, so this test would pass without the merge rule: %v", talos)
	}
	want := jsonValue(t, []byte(`{"docker.io":{"endpoints":["https://pool.example.com"]}}`))
	if !reflect.DeepEqual(talos["registryMirrors"], want) {
		t.Fatalf("registryMirrors = %v, want %v", talos["registryMirrors"], want)
	}
}

func TestInstallationDefaultsLegacyKubernetesNodesRender(t *testing.T) {
	r := nodeDefaultsREST(t)
	ctx := request.WithNamespace(context.Background(), "tenant-test")
	app := defaultsApp(`{"cluster":"demo","version":"v1.35","instanceType":"","resources":{"cpu":"2","memory":"4Gi"}}`)
	app.Name = "demo-md0"
	old, err := r.Create(ctx, app, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: "tenant-test", Name: "kubernetes-nodes-demo-md0"}
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(ctx, key, hr); err != nil {
		t.Fatal(err)
	}
	stored := hr.Spec.Values.DeepCopy()
	before := renderStoredNodeDefaults(t, stored)
	if err := r.c.Create(ctx, defaultsConfig(`{"kubernetesnodeses":{"talos":{"imageFactoryURL":"https://factory.example.com","installerRepository":"registry.example.com/installer","registryMirrors":{"ghcr.io":{"endpoints":["https://registry.example.com"]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	current, err := r.Get(ctx, app.Name, &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertSpec(t, current.(*appsv1alpha1.Application).Spec, string(old.(*appsv1alpha1.Application).Spec.Raw))
	if err := r.c.Get(ctx, key, hr); err != nil {
		t.Fatal(err)
	}
	assertSpec(t, hr.Spec.Values, string(stored.Raw))
	if _, _, err := r.Update(ctx, app.Name, rest.DefaultUpdatedObjectInfo(current), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Get(ctx, key, hr); err != nil {
		t.Fatal(err)
	}
	after := renderStoredNodeDefaults(t, hr.Spec.Values)
	if before != after {
		t.Fatalf("legacy pool changed after installation defaults were introduced: before=%+v after=%+v", before, after)
	}
}
