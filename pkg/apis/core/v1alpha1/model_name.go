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

// OpenAPIModelName pins the OpenAPI model name for the core.cozystack.io types
// to the friendly dotted form, mirroring pkg/apis/apps/v1alpha1/model_name.go.
//
// Since Kubernetes 0.35 the apiserver's DefinitionNamer returns the generated
// openapi map key verbatim instead of running it through ToRESTFriendlyName.
// Without these methods the generator keys these types by their Go import path,
// so a name containing "/" reaches the published v2 document; kube-openapi's v2
// model builder RFC-6901-escapes the slashes in $ref ("~1") and then resolves
// refs by literal string match with no unescape, so any cross-type reference
// (e.g. Option -> OptionSpec) becomes an unresolvable model and the whole
// merged /openapi/v2 is rejected by every v2 consumer. Declaring
// OpenAPIModelName makes the generated map key, the $ref targets and the
// DefinitionNamer all agree on the slash-free dotted form.
//
// The returned string must be the reverse-domain form of the Go import path;
// add one for every new core.cozystack.io type.

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
