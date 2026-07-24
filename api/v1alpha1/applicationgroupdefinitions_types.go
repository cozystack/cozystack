/*
Copyright 2026.

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

import (
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.group`
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.group",message="metadata.name must equal spec.group"

// ApplicationGroupDefinition registers an additional API group to be served
// by the cozystack aggregated API server. ApplicationDefinitions may then
// select the group via spec.application.group; every kind of such an
// ApplicationDefinition is served under the registered group instead of the
// default apps.cozystack.io.
//
// The object name must equal spec.group, which makes group registrations
// unique by construction (object names are unique) and the group immutable
// (object names are immutable). All groups are currently served at the fixed
// version v1alpha1, matching the built-in apps.cozystack.io group; a versions
// field can be added compatibly once multi-version serving exists.
type ApplicationGroupDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationGroupDefinitionSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ApplicationGroupDefinitionList contains a list of ApplicationGroupDefinitions
type ApplicationGroupDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApplicationGroupDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApplicationGroupDefinition{}, &ApplicationGroupDefinitionList{})
}

type ApplicationGroupDefinitionSpec struct {
	// Group is the API group name to serve, e.g. "apps.example.com". It must
	// be a DNS-1123 subdomain containing at least one dot: requiring a domain
	// shape keeps single-label built-in Kubernetes groups ("apps", "batch",
	// ...) out of reach by construction. Groups under cozystack.io and k8s.io
	// are reserved for the platform and for Kubernetes respectively; the
	// default apps.cozystack.io group needs no registration and is always
	// served.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`
	// +kubebuilder:validation:XValidation:rule="self != 'cozystack.io' && !self.endsWith('.cozystack.io')",message="groups under cozystack.io are reserved"
	// +kubebuilder:validation:XValidation:rule="self != 'k8s.io' && !self.endsWith('.k8s.io')",message="groups under k8s.io are reserved"
	Group string `json:"group"`
}

// applicationGroupPattern mirrors the CRD validation pattern on Group above:
// a DNS-1123 subdomain with at least one dot, so single-label built-in
// Kubernetes groups ("apps", "batch", ...) are unrepresentable.
var applicationGroupPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)

// ValidateApplicationGroup applies the ApplicationGroupDefinition group rules
// in Go: shape, length, and the reserved cozystack.io / k8s.io namespaces.
// The CRD carries the same rules as CEL markers on
// ApplicationGroupDefinitionSpec.Group, but consumers of registrations (the
// cozystack apiserver deciding which groups to serve, the controller creating
// APIServices) must not trust cluster state they did not write, so they
// re-check here and skip invalid registrations.
func ValidateApplicationGroup(group string) error {
	if len(group) > 253 {
		return fmt.Errorf("group %q exceeds 253 characters", group)
	}
	if !applicationGroupPattern.MatchString(group) {
		return fmt.Errorf("group %q is not a dotted DNS-1123 subdomain", group)
	}
	if group == "cozystack.io" || strings.HasSuffix(group, ".cozystack.io") {
		return fmt.Errorf("group %q is reserved (cozystack.io namespace)", group)
	}
	if group == "k8s.io" || strings.HasSuffix(group, ".k8s.io") {
		return fmt.Errorf("group %q is reserved (k8s.io namespace)", group)
	}
	return nil
}
