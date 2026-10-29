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
	"strings"
	"testing"

	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestApplicationDefaultsHTTPPreviewAndApply(t *testing.T) {
	resource := redisConfig
	resource.Application.OpenAPISchema = `{"type":"object","properties":{"replicas":{"type":"integer","default":1},"setting":{"type":"string"},"developer":{"type":"string","default":"chart"},"later":{"type":"boolean"}}}`
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cozy-system", Name: "cozystack-application-defaults"}, Data: map[string]string{"defaults.json": `{"redises":{"replicas":3}}`}}
	ts, cli := startAppsServer(t, &config.ResourceConfig{Resources: []config.Resource{resource}}, cm)
	request := func(method, path, contentType string) (int, map[string]any) {
		t.Helper()
		body := `{"apiVersion":"apps.cozystack.io/v1alpha1","kind":"Redis","metadata":{"name":"cache","namespace":"tenant-a"},"spec":{"setting":"user"}}`
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if res.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return res.StatusCode, decoded
	}
	base := "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/redises"
	status, preview := request(http.MethodPost, base+"?dryRun=All", "application/json")
	if status != http.StatusCreated {
		t.Fatalf("preview status %d", status)
	}
	spec := preview["spec"].(map[string]any)
	if spec["replicas"] != float64(3) || spec["developer"] != "chart" {
		t.Fatalf("preview spec: %v", spec)
	}
	key := client.ObjectKey{Namespace: "tenant-a", Name: "redis-cache"}
	if err := cli.Get(context.Background(), key, &helmv2.HelmRelease{}); !apierrors.IsNotFound(err) {
		t.Fatalf("preview persisted a release: %v", err)
	}
	status, _ = request(http.MethodPatch, base+"/cache?fieldManager=apply-client&force=true", "application/apply-patch+yaml")
	if status != http.StatusCreated {
		t.Fatalf("apply create status %d", status)
	}
	hr := &helmv2.HelmRelease{}
	if err := cli.Get(context.Background(), key, hr); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(hr.Spec.Values.Raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["replicas"] != float64(3) || stored["setting"] != "user" {
		t.Fatalf("stored spec: %v", stored)
	}
	if _, ok := stored["developer"]; ok {
		t.Fatal("API stamped developer defaults")
	}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(cm), cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["defaults.json"] = `{"redises":{"replicas":9,"later":true}}`
	if err := cli.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	status, _ = request(http.MethodPatch, base+"/cache?fieldManager=apply-client&force=true", "application/apply-patch+yaml")
	if status != http.StatusOK {
		t.Fatalf("apply update status %d", status)
	}
	if err := cli.Get(context.Background(), key, hr); err != nil {
		t.Fatal(err)
	}
	stored = nil
	if err := json.Unmarshal(hr.Spec.Values.Raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["replicas"] != float64(3) {
		t.Fatalf("reapply changed saved defaults: %s", hr.Spec.Values.Raw)
	}
	if _, ok := stored["later"]; ok {
		t.Fatal("reapply added new installation defaults")
	}
}
