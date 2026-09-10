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

package healthchecks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fluxcd/pkg/apis/kustomize"
	"github.com/fluxcd/pkg/runtime/cel"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	jsonutil "k8s.io/apimachinery/pkg/util/json"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

var repoRoot = filepath.Join("..", "..")

type fixture struct {
	Name   string          `json:"name"`
	Object json.RawMessage `json:"object"`
	Want   string          `json:"want"`
	Error  string          `json:"error"`
}

type release struct {
	Kind string `json:"kind"`
	Spec struct {
		WaitStrategy struct {
			Name string `json:"name"`
		} `json:"waitStrategy"`
		HealthChecks []kustomize.CustomHealthCheck `json:"healthCheckExprs"`
	} `json:"spec"`
}

func TestRenderedHealthChecks(t *testing.T) {
	data, err := os.ReadFile("testdata/status.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string][]fixture
	if err := yaml.UnmarshalStrict(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, chart := range []struct {
		name, path, template string
		values               []string
		families             map[string]string
	}{
		{
			name: "harbor", path: "packages/apps/harbor", template: "templates/harbor.yaml",
			values:   []string{"_namespace.host=example.org", "_namespace.ingress=nginx", "_namespace.gateway=", "_namespace.seaweedfs=seaweedfs", "_cluster.solver=http01"},
			families: map[string]string{"postgresql.cnpg.io/v1/Cluster": "cnpg"},
		},
		{
			name: "tenant-etcd", path: "packages/apps/tenant", template: "templates/etcd.yaml",
			values:   []string{"etcd=true"},
			families: map[string]string{"etcd-operator.cozystack.io/v1alpha2/EtcdCluster": "etcd"},
		},
		{
			name: "tenant-gateway", path: "packages/apps/tenant", template: "templates/gateway.yaml",
			values:   []string{"gateway=true", "host="},
			families: map[string]string{"gateway.cozystack.io/v1alpha1/TenantGateway": "gateway"},
		},
		{
			name: "monitoring", path: "packages/extra/monitoring", template: "templates/helmrelease.yaml",
			families: map[string]string{
				"operator.victoriametrics.com/v1/VLCluster":           "vm",
				"operator.victoriametrics.com/v1beta1/VMCluster":      "vm",
				"operator.victoriametrics.com/v1beta1/VMAgent":        "vm",
				"operator.victoriametrics.com/v1beta1/VMAlert":        "vm",
				"operator.victoriametrics.com/v1beta1/VMAlertmanager": "vm",
				"grafana.integreatly.org/v1beta1/Grafana":             "grafana",
				"postgresql.cnpg.io/v1/Cluster":                       "cnpg",
			},
		},
	} {
		t.Run(chart.name, func(t *testing.T) {
			hr := renderRelease(t, chart.name, chart.path, chart.template, chart.values)
			if hr.Spec.WaitStrategy.Name != "poller" {
				t.Fatalf("health checks require poller, got %q", hr.Spec.WaitStrategy.Name)
			}
			if len(hr.Spec.HealthChecks) != len(chart.families) {
				t.Fatalf("got %d health checks, want %d", len(hr.Spec.HealthChecks), len(chart.families))
			}
			factory, err := cel.NewStatusReader(hr.Spec.HealthChecks)
			if err != nil {
				t.Fatal(err)
			}
			reader := factory(nil)
			if reader.Supports(schema.GroupKind{Group: "unrelated.example.org", Kind: "Cluster"}) {
				t.Fatal("health checks matched an unrelated API group")
			}
			for _, check := range hr.Spec.HealthChecks {
				key := check.APIVersion + "/" + check.Kind
				family, ok := chart.families[key]
				if !ok {
					t.Fatalf("unexpected health check %s", key)
				}
				if !reader.Supports(schema.FromAPIVersionAndKind(check.APIVersion, check.Kind).GroupKind()) {
					t.Fatalf("status reader does not match %s", key)
				}
				t.Run(check.Kind, func(t *testing.T) {
					evaluator, err := cel.NewStatusEvaluator(&check.HealthCheckExpressions)
					if err != nil {
						t.Fatal(err)
					}
					cases := fixtures[family]
					if len(cases) == 0 {
						t.Fatalf("no fixtures for %s", family)
					}
					for _, tc := range cases {
						t.Run(tc.Name, func(t *testing.T) {
							obj := &unstructured.Unstructured{}
							if err := jsonutil.Unmarshal(tc.Object, &obj.Object); err != nil {
								t.Fatal(err)
							}
							obj.SetAPIVersion(check.APIVersion)
							obj.SetKind(check.Kind)
							obj.SetName("fixture")
							obj.SetNamespace("tenant-root")
							if obj.GetGeneration() == 0 {
								obj.SetGeneration(2)
							}
							got, err := evaluator.Evaluate(context.Background(), obj)
							read, readErr := reader.ReadStatusForObject(context.Background(), nil, obj)
							if readErr != nil {
								t.Fatal(readErr)
							}
							if tc.Error != "" {
								if err == nil || !strings.Contains(err.Error(), tc.Error) {
									t.Fatalf("got result %v, error %v; want error containing %q", got, err, tc.Error)
								}
								if read.Status.String() != "Unknown" || read.Error == nil || !strings.Contains(read.Error.Error(), tc.Error) {
									t.Fatalf("status reader got %+v, want Unknown with evaluation error", read)
								}
								return
							}
							if err != nil {
								t.Fatal(err)
							}
							if got.Status.String() != tc.Want {
								t.Fatalf("got %s, want %s; object: %s", got.Status, tc.Want, tc.Object)
							}
							if read.Error != nil || read.Status.String() != tc.Want {
								t.Fatalf("status reader got %+v, want %s", read, tc.Want)
							}
						})
					}
				})
			}
		})
	}
}

func renderRelease(t *testing.T, name, chart, template string, values []string) release {
	t.Helper()
	args := []string{"template", name, filepath.Join(repoRoot, chart), "--namespace", "tenant-root", "--show-only", template}
	for _, value := range values {
		args = append(args, "--set", value)
	}
	cmd := exec.Command("helm", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var releases []release
	for {
		var doc release
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if doc.Kind == "HelmRelease" {
			releases = append(releases, doc)
		}
	}
	if len(releases) != 1 {
		t.Fatalf("rendered %d HelmReleases, want 1", len(releases))
	}
	return releases[0]
}

func TestControllerEvaluatorPin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "internal/fluxinstall/manifests/fluxcd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	found := false
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(&obj.Object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if obj.GetKind() != "Deployment" {
			continue
		}
		containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		if err != nil {
			t.Fatal(err)
		}
		for _, container := range containers {
			c := container.(map[string]interface{})
			if c["name"] == "helm-controller" {
				found = true
				if c["image"] != "ghcr.io/fluxcd/helm-controller:v1.5.0" {
					t.Fatalf("controller image changed to %s; refresh the evaluator pins from its go.mod", c["image"])
				}
			}
		}
	}
	if !found {
		t.Fatal("embedded helm-controller image not found")
	}
	// Match helm-controller v1.5.0/go.mod, including MVS-selected CEL dependencies.
	for path, want := range map[string]string{
		"github.com/fluxcd/pkg/runtime":             "v0.100.2",
		"github.com/fluxcd/pkg/apis/kustomize":      "v1.15.0",
		"github.com/fluxcd/cli-utils":               "v0.37.1-flux.1",
		"github.com/google/cel-go":                  "v0.26.1",
		"k8s.io/apimachinery":                       "v0.35.0",
		"cel.dev/expr":                              "v0.24.0",
		"github.com/antlr4-go/antlr/v4":             "v4.13.1",
		"google.golang.org/protobuf":                "v1.36.11",
		"google.golang.org/genproto/googleapis/api": "v0.0.0-20251202230838-ff82c1b0f217",
		"google.golang.org/genproto/googleapis/rpc": "v0.0.0-20260120174246-409b4a993575",
	} {
		cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", path)
		got, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", path, err, got)
		}
		if strings.TrimSpace(string(got)) != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}
