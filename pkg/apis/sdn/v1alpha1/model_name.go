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

// OpenAPIModelName pins the OpenAPI model name for the sdn.cozystack.io types
// to the friendly dotted form. See pkg/apis/core/v1alpha1/model_name.go for the
// mechanism (the Kubernetes 0.35 DefinitionNamer no longer dots slash-named
// keys, which makes the aggregated /openapi/v2 unparseable by v2 consumers).
//
// The returned string must be the reverse-domain form of the Go import path;
// add one for every new sdn.cozystack.io type.

func (in SecurityGroup) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.SecurityGroup"
}

func (in SecurityGroupList) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.SecurityGroupList"
}

func (in SecurityGroupSpec) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.SecurityGroupSpec"
}

func (in ApplicationReference) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.ApplicationReference"
}

func (in IngressRule) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.IngressRule"
}

func (in EgressRule) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.EgressRule"
}

func (in PortRule) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.PortRule"
}

func (in PortProtocol) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.PortProtocol"
}

func (in FQDNSelector) OpenAPIModelName() string {
	return "com.github.cozystack.cozystack.pkg.apis.sdn.v1alpha1.FQDNSelector"
}
