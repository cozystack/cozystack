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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const specHashKey = "monitoring.cozystack.io/grafana-spec-hash"

type monitoringRender struct {
	grafana map[string]interface{}
	command string
	env     map[string]string
}

func renderMonitoring(t *testing.T, chart string, values []string) monitoringRender {
	t.Helper()
	args := []string{"template", "monitoring-system", chart, "--namespace", "tenant-root", "--api-versions", "v1.edp.epam.com/v1"}
	for _, value := range append([]string{"_namespace.host=example.org", "_namespace.ingress=nginx", "_cluster.root-host=example.org"}, values...) {
		args = append(args, "--set", value)
	}
	cmd := exec.Command("helm", args...)
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v: %s", err, err.(*exec.ExitError).Stderr)
	}
	result := monitoringRender{env: map[string]string{}}
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	for {
		var obj map[string]interface{}
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		switch obj["kind"] {
		case "Grafana":
			result.grafana = obj
		case "Job":
			containers, _, _ := unstructured.NestedSlice(obj, "spec", "template", "spec", "containers")
			container := containers[0].(map[string]interface{})
			command := container["command"].([]interface{})
			if command[0] != "/bin/sh" || command[1] != "-c" || len(command) != 3 {
				t.Fatalf("unexpected hook command: %v", command)
			}
			result.command = command[2].(string)
			for _, item := range container["env"].([]interface{}) {
				env := item.(map[string]interface{})
				if value, ok := env["value"].(string); ok {
					result.env[env["name"].(string)] = value
				} else if strings.HasPrefix(env["name"].(string), "GF_ADMIN_") {
					result.env[env["name"].(string)] = "fixture-admin"
				}
			}
		}
	}
	if result.grafana == nil || result.command == "" {
		t.Fatal("render must contain both Grafana and readiness Job")
	}
	return result
}

func TestGrafanaSpecHash(t *testing.T) {
	chart := filepath.Join(repoRoot, "packages/system/monitoring")
	baseline := renderMonitoring(t, chart, nil)
	for _, tc := range []struct {
		name   string
		values []string
		same   bool
	}{
		{"repeat", nil, true},
		{"unrelated-value", []string{"alerta.replicas=3"}, true},
		{"image", []string{"grafana.image=example.org/grafana:broken"}, false},
		{"resources", []string{"grafana.resources.requests.cpu=200m"}, false},
		{"host", []string{"host=changed.example.org"}, false},
		{"mirror", []string{"_cluster.images-registry=registry.example.org"}, false},
		{"inline-oidc", []string{"oidc.mode=CustomConfig", "oidc.customConfig.config.client_id=fixture"}, false},
		{"external-secret-reference", []string{"oidc.mode=CustomConfig", "oidc.customConfig.secretRef.name=external-ini"}, false},
		{"system", []string{"oidc.mode=System", "_cluster.oidc-enabled=true"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderMonitoring(t, chart, tc.values)
			if same := rendered.env["EXPECTED_GRAFANA_SPEC_HASH"] == baseline.env["EXPECTED_GRAFANA_SPEC_HASH"]; same != tc.same {
				t.Fatalf("hash equality = %v, want %v", same, tc.same)
			}
			spec := rendered.grafana["spec"].(map[string]interface{})
			marker, _, _ := unstructured.NestedString(spec, "deployment", "spec", "template", "metadata", "annotations", specHashKey)
			if marker == "" || marker != rendered.env["EXPECTED_GRAFANA_SPEC_HASH"] {
				t.Fatal("Job and Grafana markers differ or are empty")
			}
			// The original template has no Pod metadata. Removing the added map
			// recovers the exact pre-marker input rather than hashing the marker.
			unstructured.RemoveNestedField(spec, "deployment", "spec", "template", "metadata")
			data, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			if hex.EncodeToString(hash[:]) != marker {
				t.Fatal("marker is not the full pre-marker desired-spec hash")
			}
		})
	}
	first := renderMonitoring(t, chart, []string{"oidc.mode=CustomConfig", "oidc.customConfig.config.client_id=fixture", "oidc.customConfig.config.name=example", "oidc.users[0].email=alice@example.org", "oidc.users[0].role=Viewer"})
	second := renderMonitoring(t, chart, []string{"oidc.users[0].role=Admin", "oidc.users[0].email=alice@example.org", "oidc.customConfig.config.name=example", "oidc.customConfig.config.client_id=fixture", "oidc.mode=CustomConfig"})
	if first.env["EXPECTED_GRAFANA_SPEC_HASH"] != second.env["EXPECTED_GRAFANA_SPEC_HASH"] {
		t.Fatal("map insertion order and a role-only edit must not restart Grafana")
	}
}

func TestGrafanaMalformedBase(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"grafana", "job"} {
		for _, base := range []string{"invalid: [", "- item", "scalar", "Error: fixture\nspec: {}", "kind: Grafana", "spec: null", "spec: []"} {
			t.Run(caller+"/"+base, func(t *testing.T) {
				chart := t.TempDir()
				for _, name := range []string{"Chart.yaml", "values.yaml", "templates/_helpers.tpl", "templates/grafana/grafana.yaml", "templates/grafana/oidc-users-job.yaml", "images/kubectl.tag"} {
					data, err := os.ReadFile(filepath.Join(root, "packages/system/monitoring", name))
					if err != nil {
						t.Fatal(err)
					}
					if name == "templates/grafana/grafana.yaml" {
						footer := ""
						if caller == "grafana" {
							footer = string(data[strings.LastIndex(string(data), "{{- $grafana := include"):])
						}
						data = []byte("{{- define \"monitoring.grafana.base\" -}}\n" + base + "\n{{- end }}\n" + footer)
					}
					if caller == "grafana" && name == "templates/grafana/oidc-users-job.yaml" {
						continue
					}
					writeTestFile(t, filepath.Join(chart, name), data, 0600)
				}
				if err := os.MkdirAll(filepath.Join(chart, "charts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "packages/library/cozy-lib"), filepath.Join(chart, "charts/cozy-lib")); err != nil {
					t.Fatal(err)
				}
				output, err := exec.Command("helm", "template", "fixture", chart).CombinedOutput()
				if err == nil || !strings.Contains(string(output), "Grafana base must be a resource map with a spec map") {
					t.Fatalf("malformed base should fail before hashing: %v\n%s", err, output)
				}
			})
		}
	}
}

type grafanaScenario struct {
	Name                string
	GrafanaPatch        string
	DeploymentPatch     string
	APIError            string
	APIKind             string
	ElapsedDuringHealth int
	Malformed           bool
	HTTPFailure         bool
	AdminFailure        bool
	Recover             bool
	ChangeAfterHealth   string
	WantSuccess         bool
	WantHealth          bool
}

type grafanaFakeState struct {
	Seconds, Sleeps, HealthCalls int
	Events                       []string
}

func TestGrafanaRenderedObserver(t *testing.T) {
	for _, binary := range []string{"dash", "jq"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("rendered observer tests require %s: %v", binary, err)
		}
	}
	rendered := renderMonitoring(t, filepath.Join(repoRoot, "packages/system/monitoring"), nil)
	for _, tc := range []grafanaScenario{
		{Name: "ready", WantSuccess: true, WantHealth: true},
		{Name: "old-marker-and-healthy-old-pods", DeploymentPatch: `{"spec":{"template":{"metadata":{"annotations":{"monitoring.cozystack.io/grafana-spec-hash":"old"}}}}}`},
		{Name: "stale-cr-marker", GrafanaPatch: `{"spec":{"deployment":{"spec":{"template":{"metadata":{"annotations":{"monitoring.cozystack.io/grafana-spec-hash":"old"}}}}}}`},
		{Name: "foreign-owner", DeploymentPatch: `{"metadata":{"ownerReferences":[{"apiVersion":"grafana.integreatly.org/v1beta1","kind":"Grafana","name":"grafana","controller":true,"uid":"foreign"}]}}`},
		{Name: "missing-owner", DeploymentPatch: `{"metadata":{"ownerReferences":null}}`},
		{Name: "missing-deployment-uid", DeploymentPatch: `{"metadata":{"uid":null}}`},
		{Name: "missing-cr-uid", GrafanaPatch: `{"metadata":{"uid":null}}`},
		{Name: "deleting-cr", GrafanaPatch: `{"metadata":{"deletionTimestamp":"2026-09-10T00:00:00Z"}}`},
		{Name: "deleting-deployment", DeploymentPatch: `{"metadata":{"deletionTimestamp":"2026-09-10T00:00:00Z"}}`},
		{Name: "unobserved-generation", DeploymentPatch: `{"status":{"observedGeneration":6}}`},
		{Name: "zero-ready-after-complete-stage", DeploymentPatch: `{"status":{"readyReplicas":0,"availableReplicas":0}}`},
		{Name: "one-new-pod-old-capacity-still-healthy", DeploymentPatch: `{"status":{"updatedReplicas":1}}`},
		{Name: "extra-old-replicas", DeploymentPatch: `{"status":{"replicas":3}}`},
		{Name: "paused", DeploymentPatch: `{"spec":{"paused":true}}`},
		{Name: "zero-replicas", DeploymentPatch: `{"spec":{"replicas":0}}`},
		{Name: "missing-ready-counter", DeploymentPatch: `{"status":{"readyReplicas":null}}`},
		{Name: "unknown-condition", DeploymentPatch: `{"status":{"conditions":[{"type":"Available","status":"Unknown"}]}}`},
		{Name: "progress-deadline", DeploymentPatch: `{"status":{"conditions":[{"type":"Available","status":"True"},{"type":"Progressing","status":"False"}]}}`},
		{Name: "replica-failure", DeploymentPatch: `{"status":{"conditions":[{"type":"Available","status":"True"},{"type":"ReplicaFailure","status":"True"}]}}`},
		{Name: "missing-status", DeploymentPatch: `{"status":null}`},
		{Name: "missing-object", APIError: "404"},
		{Name: "missing-deployment", APIError: "404", APIKind: "deployment"},
		{Name: "forbidden-deployment", APIError: "403", APIKind: "deployment"},
		{Name: "unauthorized", APIError: "401"},
		{Name: "forbidden", APIError: "403"},
		{Name: "api-timeout", APIError: "timeout"},
		{Name: "malformed-json", Malformed: true},
		{Name: "readiness-completes-after-deadline", ElapsedDuringHealth: 600, WantHealth: true},
		{Name: "http-fails", HTTPFailure: true, WantHealth: true},
		{Name: "generation-changes-during-http", ChangeAfterHealth: "generation", WantHealth: true},
		{Name: "owner-replaced-during-http", ChangeAfterHealth: "uid", WantHealth: true},
		{Name: "marker-changes-during-http", ChangeAfterHealth: "marker", WantHealth: true},
		{Name: "rollout-recovers", DeploymentPatch: `{"status":{"updatedReplicas":1}}`, Recover: true, WantSuccess: true, WantHealth: true},
		{Name: "http-recovers", HTTPFailure: true, Recover: true, WantSuccess: true, WantHealth: true},
	} {
		t.Run(tc.Name, func(t *testing.T) { runGrafanaScript(t, rendered, tc, false) })
	}
}

func TestGrafanaUsersContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		manage bool
	}{
		{"none", nil, false},
		{"none-with-users", []string{"oidc.users[0].email=alice@example.org", "oidc.users[0].role=Viewer"}, false},
		{"empty-system", []string{"oidc.mode=System", "_cluster.oidc-enabled=true"}, false},
		{"empty-inline", []string{"oidc.mode=CustomConfig", "oidc.customConfig.config.client_id=fixture"}, false},
		{"external-secret", []string{"oidc.mode=CustomConfig", "oidc.customConfig.secretRef.name=external-ini"}, false},
		{"active-system", []string{"oidc.mode=System", "_cluster.oidc-enabled=true", "oidc.users[0].email=alice@example.org", "oidc.users[0].role=Viewer"}, true},
		{"active-inline", []string{"oidc.mode=CustomConfig", "oidc.customConfig.config.client_id=fixture", "oidc.users[0].email=alice@example.org", "oidc.users[0].role=Viewer"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderMonitoring(t, filepath.Join(repoRoot, "packages/system/monitoring"), tc.values)
			_, credentials := rendered.env["GF_ADMIN_PASSWORD"]
			if credentials != tc.manage {
				t.Fatal("admin credential references must follow users-map ownership")
			}
			runGrafanaScript(t, rendered, grafanaScenario{WantSuccess: true, WantHealth: true}, tc.manage)
			if tc.manage {
				runGrafanaScript(t, rendered, grafanaScenario{HTTPFailure: true, WantHealth: true}, false)
				runGrafanaScript(t, rendered, grafanaScenario{AdminFailure: true, WantHealth: true}, true)
			}
		})
	}
}

func runGrafanaScript(t *testing.T, rendered monitoringRender, scenario grafanaScenario, wantAdmin bool) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "scenario.json"), scenario)
	writeJSON(t, filepath.Join(dir, "state.json"), grafanaFakeState{})
	data, err := os.ReadFile("testdata/grafana-rollout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]interface{}
	if err := yaml.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "fixture.json"), fixture)
	for _, name := range []string{"kubectl", "curl", "date", "sleep"} {
		writeTestFile(t, filepath.Join(dir, "bin", name), []byte("#!/bin/sh\nexec \"$GRAFANA_TEST_BINARY\" -test.run=^TestGrafanaCommandHelper$ -- "+name+" \"$@\"\n"), 0700)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dash", "-c", rendered.command)
	cmd.Env = append(os.Environ(), "GRAFANA_COMMAND_HELPER=1", "GRAFANA_TEST_BINARY="+executable, "GRAFANA_TEST_DIR="+dir, "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, value := range rendered.env {
		if name != "HOME" {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+dir, "POD_NAMESPACE=tenant-root")
	output, err := cmd.CombinedOutput()
	if (err == nil) != scenario.WantSuccess {
		t.Fatalf("script error %v, want success %v:\n%s", err, scenario.WantSuccess, output)
	}
	var state grafanaFakeState
	readJSON(t, filepath.Join(dir, "state.json"), &state)
	if (state.HealthCalls > 0) != scenario.WantHealth {
		t.Fatalf("health calls = %d, events: %v", state.HealthCalls, state.Events)
	}
	admin := false
	for i, event := range state.Events {
		if strings.HasPrefix(event, "admin ") {
			admin = true
			if i < 5 || !strings.HasPrefix(state.Events[i-1], "get deployment") && !strings.HasPrefix(state.Events[i-1], "admin ") {
				t.Fatalf("admin call before completed readiness sample: %v", state.Events)
			}
		}
	}
	if admin != wantAdmin {
		t.Fatalf("admin calls = %v, want %v: %v", admin, wantAdmin, state.Events)
	}
	if scenario.WantSuccess && wantAdmin {
		joined := strings.Join(state.Events, "\n")
		for _, call := range []string{"admin POST /api/admin/users", "admin POST /api/orgs/1/users", "admin PATCH /api/orgs/1/users/2", "admin DELETE /api/orgs/1/users/3"} {
			if !strings.Contains(joined, call) {
				t.Fatalf("missing %s: %v", call, state.Events)
			}
		}
		if strings.Contains(joined, "admin DELETE /api/orgs/1/users/1") || strings.Contains(joined, "admin DELETE /api/orgs/1/users/2") {
			t.Fatal("pruned the admin or a desired member")
		}
	}
	if !scenario.WantSuccess && !scenario.AdminFailure && !strings.Contains(string(output), "within 10m") {
		t.Fatalf("failure did not reach bounded readiness deadline: %s", output)
	}
	if scenario.Recover && state.Sleeps == 0 {
		t.Fatal("recovery fixture passed before the unavailable sample")
	}
}

// These subprocesses supply observations; all readiness decisions execute in
// the actual rendered shell and jq, not in this fixture transport.
func TestGrafanaCommandHelper(t *testing.T) {
	if os.Getenv("GRAFANA_COMMAND_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	dir := os.Getenv("GRAFANA_TEST_DIR")
	var scenario grafanaScenario
	var state grafanaFakeState
	var fixture map[string]interface{}
	readJSON(t, filepath.Join(dir, "scenario.json"), &scenario)
	readJSON(t, filepath.Join(dir, "state.json"), &state)
	readJSON(t, filepath.Join(dir, "fixture.json"), &fixture)
	exitCode, response := 0, ""
	switch args[0] {
	case "date":
		response = fmt.Sprint(state.Seconds)
	case "sleep":
		state.Seconds += 300
		state.Sleeps++
	case "kubectl":
		if len(args) != 5 || !reflect.DeepEqual(args[1:4], []string{"--request-timeout=5s", "get", "--raw"}) {
			t.Fatalf("unexpected API command: %v", args)
		}
		kind := ""
		switch args[4] {
		case "/apis/grafana.integreatly.org/v1beta1/namespaces/tenant-root/grafanas/grafana":
			kind = "grafana"
		case "/apis/apps/v1/namespaces/tenant-root/deployments/grafana-deployment":
			kind = "deployment"
		default:
			t.Fatalf("unnamed or unexpected GET: %v", args)
		}
		state.Events = append(state.Events, "get "+kind)
		obj := fixture[kind].(map[string]interface{})
		hashPath := []string{"spec", "template", "metadata", "annotations", specHashKey}
		if kind == "grafana" {
			hashPath = append([]string{"spec", "deployment"}, hashPath...)
		}
		if err := unstructured.SetNestedField(obj, os.Getenv("EXPECTED_GRAFANA_SPEC_HASH"), hashPath...); err != nil {
			t.Fatal(err)
		}
		if !(scenario.Recover && state.Sleeps > 0) {
			patch := scenario.DeploymentPatch
			if kind == "grafana" {
				patch = scenario.GrafanaPatch
			}
			if patch != "" {
				var changes map[string]interface{}
				if err := json.Unmarshal([]byte(patch), &changes); err != nil {
					t.Fatal(err)
				}
				mergeFixture(obj, changes)
			}
		}
		if scenario.ChangeAfterHealth != "" && state.HealthCalls > 0 {
			switch scenario.ChangeAfterHealth {
			case "generation":
				if kind == "deployment" {
					obj["metadata"].(map[string]interface{})["generation"] = 7 + state.HealthCalls
					obj["status"].(map[string]interface{})["observedGeneration"] = 7 + state.HealthCalls
				}
			case "uid":
				uid := fmt.Sprintf("replaced-%d", state.HealthCalls)
				if kind == "grafana" {
					obj["metadata"].(map[string]interface{})["uid"] = uid
				} else {
					obj["metadata"].(map[string]interface{})["ownerReferences"].([]interface{})[0].(map[string]interface{})["uid"] = uid
				}
			case "marker":
				_ = unstructured.SetNestedField(obj, "changed-during-health", hashPath...)
			}
		}
		data, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		response = string(data)
		if scenario.Malformed {
			response = "{"
		}
		if scenario.APIError != "" && (scenario.APIKind == "" || scenario.APIKind == kind) {
			response, exitCode = "", 1
		}
	case "curl":
		url := args[len(args)-1]
		if strings.HasSuffix(url, "/api/health") {
			state.HealthCalls++
			state.Seconds += scenario.ElapsedDuringHealth
			state.Events = append(state.Events, "health")
			if scenario.HTTPFailure && !(scenario.Recover && state.Sleeps > 0) {
				exitCode = 22
			}
		} else {
			method, output := "GET", ""
			for i := 1; i < len(args)-1; i++ {
				switch args[i] {
				case "--request":
					method = args[i+1]
				case "--output":
					output = args[i+1]
				}
			}
			path := strings.TrimPrefix(url, os.Getenv("GRAFANA_URL"))
			state.Events = append(state.Events, "admin "+method+" "+path)
			body := `{}`
			if strings.HasPrefix(path, "/api/users/lookup?") {
				body = `{"id":2}`
			} else if method == "GET" && path == "/api/orgs/1/users" {
				body = `[{"userId":1,"login":"fixture-admin","email":"admin@example.org"},{"userId":2,"login":"alice@example.org","email":"alice@example.org"},{"userId":3,"login":"stale@example.org","email":"stale@example.org"}]`
			}
			writeTestFile(t, output, []byte(body), 0600)
			response = "200"
			if scenario.AdminFailure {
				response = "500"
			}
		}
	default:
		t.Fatalf("unknown fake executable %q", args[0])
	}
	writeJSON(t, filepath.Join(dir, "state.json"), state)
	fmt.Print(response)
	os.Exit(exitCode)
}

func mergeFixture(dst, patch map[string]interface{}) {
	for key, value := range patch {
		if nested, ok := value.(map[string]interface{}); ok {
			child, ok := dst[key].(map[string]interface{})
			if !ok {
				child = map[string]interface{}{}
				dst[key] = child
			}
			mergeFixture(child, nested)
		} else {
			dst[key] = value
		}
	}
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, data, 0600)
}

func readJSON(t *testing.T, path string, dst interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatal(err)
	}
}
