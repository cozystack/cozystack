/*
Copyright 2025 The Cozystack Authors.

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

package v1alpha1

// OpenAPIModelName pins the OpenAPI model name for the apps.cozystack.io
// types. This is hand-written (not generated) on purpose.
//
// The apps API multiplexes every catalog kind (Bucket, Postgres, …) onto the
// single Application Go type via Scheme.AddKnownTypeWithName. Server-side-apply
// field management resolves a kind by matching the model's OpenAPI name against
// the name the apiserver's DefinitionNamer computed for it. Since Kubernetes
// 0.35 the DefinitionNamer keys that lookup by Scheme.ToOpenAPIDefinitionName
// (the "friendly" reversed-path form, e.g. com.github.cozystack…Application),
// while the model builder still looks it up by the Go import-path form. When a
// type has no OpenAPIModelName the two disagree, the group-version-kind
// extension is never attached to the Application model, and the SSA type
// converter ends up with no entry for any apps kind ("no corresponding type for
// apps.cozystack.io/v1alpha1, Kind=Bucket"). Declaring OpenAPIModelName makes
// GetCanonicalTypeName, ToOpenAPIDefinitionName and the generated openapi map
// key all agree on the friendly form, so the extension attaches again.
//
// code-generator's openapi-gen could emit these via --output-model-name-file,
// but that also rewrites zz_generated.model_name.go in the read-only apimachinery
// / apiextensions module-cache packages the shared gen_openapi helper always
// passes as inputs, which fails on any consumer (including CI) that vendors deps
// from the module cache. Keeping the methods here sidesteps that; add one for
// every new apps.cozystack.io type.
//
// The returned string must match apiPrefix in pkg/cmd/server/openapi.go.

func (in Application) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1.Application"
}

func (in ApplicationList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1.ApplicationList"
}

func (in ApplicationStatus) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1.ApplicationStatus"
}
