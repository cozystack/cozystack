// Copyright 2024 The Cozystack Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command openapi-gen assembles the OpenAPI v3 spec for apps.cozystack.io by
// spinning up an in-process Kubernetes API server with stub storage, so the
// framework generates the exact same paths and schemas as the real server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	cozyserver "github.com/cozystack/cozystack/pkg/cmd/server"
	"github.com/cozystack/cozystack/pkg/config"
	sampleopenapi "github.com/cozystack/cozystack/pkg/generated/openapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	restclient "k8s.io/client-go/rest"
	basecompatibility "k8s.io/component-base/compatibility"
	baseversion "k8s.io/component-base/version"
	"sigs.k8s.io/yaml"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Find all ApplicationDefinition YAML files
	pattern := "packages/system/*-rd/cozyrds/*.yaml"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no files matched %q — run from repo root", pattern)
	}

	// Parse ApplicationDefinitions and build ResourceConfig
	var resources []config.Resource
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var appDef cozyv1alpha1.ApplicationDefinition
		if err := yaml.Unmarshal(data, &appDef); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if appDef.Spec.Application.Kind == "" {
			continue
		}
		resources = append(resources, config.Resource{
			Application: config.ApplicationConfig{
				Kind:          appDef.Spec.Application.Kind,
				Singular:      appDef.Spec.Application.Singular,
				Plural:        appDef.Spec.Application.Plural,
				OpenAPISchema: appDef.Spec.Application.OpenAPISchema,
			},
		})
	}

	if len(resources) == 0 {
		return fmt.Errorf("no ApplicationDefinitions found")
	}

	resourceConfig := &config.ResourceConfig{Resources: resources}

	// Register dynamic types in the apiserver scheme (same as the real server)
	if err := appsv1alpha1.RegisterDynamicTypes(apiserver.Scheme, resourceConfig); err != nil {
		return fmt.Errorf("register dynamic types: %w", err)
	}

	version := os.Getenv("VERSION")
	if version == "" {
		version = "dev"
	}

	// Create a minimal GenericAPIServer config
	serverConfig := genericapiserver.NewConfig(apiserver.Codecs)
	serverConfig.ExternalAddress = "localhost:443"
	serverConfig.LoopbackClientConfig = &restclient.Config{}
	serverConfig.FeatureGate = utilfeature.DefaultMutableFeatureGate
	if baseversion.DefaultKubeBinaryVersion != "" {
		serverConfig.EffectiveVersion = basecompatibility.NewEffectiveVersionFromString(baseversion.DefaultKubeBinaryVersion, "", "")
	}

	// OpenAPI v3 only — the v2 config is required by the framework but we only extract v3.
	kindSchemas := cozyserver.KindSchemasFromConfig(resourceConfig)
	serverConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIV3Config.Info.Title = "Cozystack apps.cozystack.io API"
	serverConfig.OpenAPIV3Config.Info.Version = version
	serverConfig.OpenAPIV3Config.PostProcessSpec = cozyserver.BuildPostProcessV3(kindSchemas)

	// Create the server
	completed := serverConfig.Complete(nil)
	server, err := completed.New("openapi-gen", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	// Install apps API group with stub REST storage
	appsStorage := map[string]rest.Storage{}
	for _, res := range resourceConfig.Resources {
		appsStorage[res.Application.Plural] = &stubREST{
			gvk: schema.GroupVersion{
				Group:   "apps.cozystack.io",
				Version: "v1alpha1",
			}.WithKind(res.Application.Kind),
			singularName: res.Application.Singular,
		}
	}
	if err := apiserver.InstallAppsAPIGroup(server, appsStorage); err != nil {
		return fmt.Errorf("install apps API group: %w", err)
	}

	// PrepareRun triggers OpenAPI spec generation
	server.PrepareRun()

	// Extract the v3 spec by hitting the server's HTTP handler directly
	v3Path := "/openapi/v3/apis/apps.cozystack.io/v1alpha1"
	req, err := http.NewRequest("GET", v3Path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return fmt.Errorf("GET %s returned %d: %s", v3Path, rec.Code, rec.Body.String())
	}

	// Pretty-print the JSON
	var raw json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		return fmt.Errorf("parse v3 response: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(raw)
}

// stubREST implements the same REST interfaces as the real application.REST
// but never handles actual requests. It exists solely so the K8s framework
// generates the correct OpenAPI paths and schemas.
type stubREST struct {
	gvk          schema.GroupVersionKind
	singularName string
}

// Compile-time interface checks — same set as the real application.REST.
var (
	_ rest.Getter          = &stubREST{}
	_ rest.Lister          = &stubREST{}
	_ rest.Creater         = &stubREST{}
	_ rest.Updater         = &stubREST{}
	_ rest.Patcher         = &stubREST{}
	_ rest.GracefulDeleter = &stubREST{}
	_ rest.Watcher         = &stubREST{}
)

func (s *stubREST) New() runtime.Object {
	obj := &appsv1alpha1.Application{}
	obj.TypeMeta = metav1.TypeMeta{
		APIVersion: s.gvk.GroupVersion().String(),
		Kind:       s.gvk.Kind,
	}
	return obj
}

func (s *stubREST) NewList() runtime.Object {
	obj := &appsv1alpha1.ApplicationList{}
	obj.TypeMeta = metav1.TypeMeta{
		APIVersion: s.gvk.GroupVersion().String(),
		Kind:       s.gvk.Kind + "List",
	}
	return obj
}

func (s *stubREST) Destroy()                              {}
func (s *stubREST) NamespaceScoped() bool                 { return true }
func (s *stubREST) GetSingularName() string               { return s.singularName }
func (s *stubREST) GroupVersionKind(schema.GroupVersion) schema.GroupVersionKind { return s.gvk }

func (s *stubREST) Get(_ context.Context, _ string, _ *metav1.GetOptions) (runtime.Object, error) {
	return nil, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) List(_ context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	return nil, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) Create(_ context.Context, _ runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	return nil, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) Update(_ context.Context, _ string, _ rest.UpdatedObjectInfo, _ rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, _ bool, _ *metav1.UpdateOptions) (runtime.Object, bool, error) {
	return nil, false, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) Delete(_ context.Context, _ string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	return nil, false, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) Watch(_ context.Context, _ *metainternalversion.ListOptions) (watch.Interface, error) {
	return nil, fmt.Errorf("stub: not implemented")
}

func (s *stubREST) ConvertToTable(_ context.Context, _ runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	return nil, fmt.Errorf("stub: not implemented")
}
