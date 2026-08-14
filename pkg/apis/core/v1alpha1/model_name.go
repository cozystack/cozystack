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

package v1alpha1

// OpenAPIModelName pins the OpenAPI model name for the core.cozystack.io types.
// This is hand-written (not generated) on purpose.
//
// Without it, openapi-gen keys the generated definition map on the Go import
// path ("github.com/cozystack/cozystack/pkg/apis/core/v1alpha1.Option"), and
// since Kubernetes 0.35 the apiserver's DefinitionNamer.GetDefinitionName
// returns whatever name it is given verbatim instead of converting it to the
// "friendly" reversed-path form. The published document then keys the definition
// on the raw path while every $ref to it is JSON pointer escaped
// ("#/definitions/github.com~1cozystack~1…v1alpha1.OptionSpec", ~1 being a
// slash), and clients resolve a $ref by trimming "#/definitions/" without
// unescaping. The two spellings never meet, so the reference dangles and
// client-side validation fails on every resource in the group, not only the one
// named in the error:
//
//	error validating data: SchemaError(…core/v1alpha1.Option.spec):
//	unknown model in reference: "github.com~1cozystack~1…v1alpha1.OptionSpec"
//
// Declaring the dotted name here makes GetCanonicalTypeName,
// Scheme.ToOpenAPIDefinitionName and the generated openapi map key all agree on
// a name with no slash in it, so nothing needs escaping and every $ref resolves.
// The same agreement is what lets DefinitionNamer attach the
// x-kubernetes-group-version-kind extension, which server-side apply needs to
// resolve a kind (see the apps package, where its absence broke SSA instead).
//
// code-generator's openapi-gen could emit these via --output-model-name-file,
// but that also rewrites zz_generated.model_name.go in the read-only apimachinery
// / apiextensions module-cache packages the shared gen_openapi helper always
// passes as inputs, which fails on any consumer (including CI) that vendors deps
// from the module cache. Keeping the methods here sidesteps that.
//
// The +k8s:openapi-model-package marker in doc.go makes openapi-gen emit
// Type{}.OpenAPIModelName() for every type in this package it generates a schema
// or a reference for, so a new core.cozystack.io type without a method here
// fails the build rather than silently reintroducing a Go-path name. The
// returned string must be the dotted form Scheme.ToOpenAPIDefinitionName derives
// from the type's Go package path, which is the marker's value plus the type
// name. See https://github.com/cozystack/cozystack/issues/3806.

func (in Option) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.Option"
}

func (in OptionItem) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.OptionItem"
}

func (in OptionList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.OptionList"
}

func (in OptionSpec) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.OptionSpec"
}

func (in TenantModule) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantModule"
}

func (in TenantModuleList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantModuleList"
}

func (in TenantModuleStatus) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantModuleStatus"
}

func (in TenantNamespace) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantNamespace"
}

func (in TenantNamespaceList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantNamespaceList"
}

func (in TenantSecret) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantSecret"
}

func (in TenantSecretList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.core.v1alpha1.TenantSecretList"
}
