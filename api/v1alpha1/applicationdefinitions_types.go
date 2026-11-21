/*
Copyright 2025.

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
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// ApplicationDefinition is the Schema for the applicationdefinitions API
type ApplicationDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApplicationDefinitionSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ApplicationDefinitionList contains a list of ApplicationDefinitions
type ApplicationDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApplicationDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApplicationDefinition{}, &ApplicationDefinitionList{})
}

type ApplicationDefinitionSpec struct {
	// Application configuration
	Application ApplicationDefinitionApplication `json:"application"`
	// Release configuration
	Release ApplicationDefinitionRelease `json:"release"`

	// Secret selectors
	Secrets ApplicationDefinitionResources `json:"secrets,omitempty"`
	// Service selectors
	Services ApplicationDefinitionResources `json:"services,omitempty"`
	// Ingress selectors
	Ingresses ApplicationDefinitionResources `json:"ingresses,omitempty"`

	// Dashboard configuration for this resource
	Dashboard *ApplicationDefinitionDashboard `json:"dashboard,omitempty"`

	// Consumption declares what an application of this kind reserves from
	// its tenant's quota budget, computed from its values. Unset means the
	// kind has not declared it, which is not the same as consuming nothing:
	// a kind that consumes nothing sets exempt.
	// +optional
	Consumption *ApplicationDefinitionConsumption `json:"consumption,omitempty"`
}

// ApplicationDefinitionConsumption is how an application's reservation is
// computed from its values, as CEL over the values defaulted against the
// kind's openAPISchema.
//
// An expression sees the variable `values`, every binding in let, and four
// functions: instanceType(name) and preset(name), which return a map of
// quantities keyed by quota key, quantity(x), which takes an int or a
// quantity string, and fail(reason), which raises an evaluation error with
// that reason, for a spec the kind's chart would refuse to render. CEL's
// logical operators absorb an error when the other operand decides the
// result, so fail() refuses the spec only from a branch of a conditional. A
// quantity is scaled with q.mul(n). An expression may
// return an int, a double, a quantity string or a quantity; a key that
// evaluates to zero reserves nothing.
// Its fields are owned together: expressions refer to bindings by name, so a
// partial update could leave an expression whose binding is gone.
// +structType=atomic
// +kubebuilder:validation:XValidation:rule="!(has(self.exempt) && self.exempt) || (!has(self.__let__) && !has(self.resources))",message="an exempt declaration carries neither let nor resources"
// +kubebuilder:validation:XValidation:rule="(has(self.exempt) && self.exempt) || (has(self.resources) && size(self.resources) > 0)",message="a declaration either is exempt or names at least one resource"
type ApplicationDefinitionConsumption struct {
	// Exempt declares that the kind consumes nothing from a tenant's budget.
	// +optional
	Exempt bool `json:"exempt,omitempty"`

	// Let binds intermediate expressions, evaluated in order. A binding
	// reads only the bindings before it; a resources expression reads them
	// all. The list is replaced as a whole, since a binding left behind by a
	// partial update could refer to one that is gone.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self.all(b, self.exists_one(c, c.name == b.name))",message="binding names must be unique"
	Let []ApplicationDefinitionConsumptionBinding `json:"let,omitempty"`

	// Resources maps a quota key, as a Tenant's resourceQuotas spells it,
	// to the expression computing how much of it the application reserves.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(k) > 0)",message="a quota key must not be empty"
	Resources map[string]string `json:"resources,omitempty"`
}

// ApplicationDefinitionConsumptionBinding names one intermediate expression.
type ApplicationDefinitionConsumptionBinding struct {
	// Name is how later expressions refer to the binding. `values` is taken
	// by the application's values, and CEL's reserved words cannot name a
	// variable.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="!(self in ['values', 'true', 'false', 'null', 'in', 'as', 'break', 'const', 'continue', 'else', 'for', 'function', 'if', 'import', 'let', 'loop', 'package', 'namespace', 'return', 'var', 'void', 'while'])",message="the name is reserved"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

type ApplicationDefinitionApplication struct {
	// Kind of the application, used for UI and API
	Kind string `json:"kind"`
	// OpenAPI schema for the application, used for API validation
	OpenAPISchema string `json:"openAPISchema"`
	// Plural name of the application, used for UI and API
	Plural string `json:"plural"`
	// Singular name of the application, used for UI and API
	Singular string `json:"singular"`
}

type ApplicationDefinitionRelease struct {
	// Reference to the chart source
	ChartRef *helmv2.CrossNamespaceSourceReference `json:"chartRef"`
	// Labels for the release
	Labels map[string]string `json:"labels,omitempty"`
	// Prefix for the release name. Release names are "<prefix><app name>" and the
	// tenant CA trust anchor is projected to "<release>.tenant-ca", where the dot
	// is a separator no release name may contain — so the prefix must be dot-free.
	// It is restricted to lowercase DNS-1123 characters, which excludes the dot by
	// construction.
	// +kubebuilder:validation:Pattern=`^[a-z0-9-]*$`
	Prefix string `json:"prefix"`

	// WaitStrategy maps to HelmReleaseSpec.WaitStrategy.Name — a deliberate
	// scalar simplification of the upstream {name} object, since there is only
	// one value to set. One of poller|legacy. When healthCheckExprs is set and
	// this is empty, the generated HelmRelease defaults to poller, because
	// healthCheckExprs are only evaluated under the poller wait strategy.
	// +optional
	// +kubebuilder:validation:Enum=poller;legacy
	WaitStrategy string `json:"waitStrategy,omitempty"`

	// HealthCheckExprs maps to HelmReleaseSpec.HealthCheckExprs — CEL health
	// expressions for the custom resource(s) this application renders, so the
	// HelmRelease reports Ready only when the CR is actually healthy instead of
	// as soon as helm applies it. OpenAPI validates only the struct shape, so
	// mistakes surface in helm-controller, and they do not all fail the same
	// way:
	//   - An expression that does not parse, an empty current expression, or
	//     two entries for the same group and kind stall the HelmRelease with
	//     reason InvalidCELExpression before any Helm action runs.
	//   - An expression that errors on the object, such as a read of a
	//     misspelled field or a non-boolean result, does not end the wait: the
	//     CR reads as Unknown on every poll, and the Helm action waits out its
	//     timeout with the CEL error in the message. Reading a status field
	//     errors this way, even inside has(), until the CR's controller writes
	//     a status; the wait goes on and can still succeed once it does.
	//   - A current expression that evaluates cleanly but never becomes true,
	//     such as a comparison against the wrong value, leaves the CR
	//     InProgress, and the Helm action waits out its timeout with no CEL
	//     error to point at the expression.
	//   - A group or kind that does not match the CR the chart renders leaves
	//     the expressions unused, and that CR is judged by the built-in
	//     kstatus rules instead.
	// A CR whose CRD is not installed fails the Helm action before any wait,
	// whatever these expressions say.
	// helm-controller parses and validates these expressions and creates a
	// status reader factory before choosing how to wait. The factory constructs
	// the concrete status reader when the selected wait path creates its
	// resource manager. Invalid configuration can
	// therefore stall the HelmRelease with reason InvalidCELExpression even when
	// the release.cozystack.io/helm-install-disable-wait annotation is set or the
	// legacy wait strategy is selected. Once setup succeeds, only helm-controller's
	// poller evaluates expressions: while a Helm action waits under the poller
	// strategy, and for Helm hooks, which are still waited on when the annotation
	// disables wait. Without the annotation, the legacy strategy never evaluates
	// them. With it, hooks are evaluated whatever the strategy, because disabling
	// wait takes precedence.
	// +optional
	HealthCheckExprs []kustomize.CustomHealthCheck `json:"healthCheckExprs,omitempty"`
}

// ApplicationDefinitionResourceSelector extends metav1.LabelSelector with resourceNames support.
// A resource matches this selector only if it satisfies ALL criteria:
// - Label selector conditions (matchExpressions and matchLabels)
// - AND has a name that matches one of the names in resourceNames (if specified)
//
// The resourceNames field supports Go templates with the following variables available:
// - {{ .name }}: The name of the managing application (from apps.cozystack.io/application.name)
// - {{ .kind }}: The lowercased kind of the managing application (from apps.cozystack.io/application.kind)
// - {{ .namespace }}: The namespace of the resource being processed
//
// Example YAML:
//
//	secrets:
//	  include:
//	  - matchExpressions:
//	    - key: badlabel
//	      operator: DoesNotExist
//	    matchLabels:
//	      goodlabel: goodvalue
//	    resourceNames:
//	    - "{{ .name }}-secret"
//	    - "{{ .kind }}-{{ .name }}-tls"
//	    - "specificname"
type ApplicationDefinitionResourceSelector struct {
	metav1.LabelSelector `json:",inline"`
	// ResourceNames is a list of resource names to match
	// If specified, the resource must have one of these exact names to match the selector
	// +optional
	ResourceNames []string `json:"resourceNames,omitempty"`
}

type ApplicationDefinitionResources struct {
	// Exclude contains an array of resource selectors that target resources.
	// If a resource matches the selector in any of the elements in the array, it is
	// hidden from the user, regardless of the matches in the include array.
	Exclude []*ApplicationDefinitionResourceSelector `json:"exclude,omitempty"`
	// Include contains an array of resource selectors that target resources.
	// If a resource matches the selector in any of the elements in the array, and
	// matches none of the selectors in the exclude array that resource is marked
	// as a tenant resource and is visible to users.
	Include []*ApplicationDefinitionResourceSelector `json:"include,omitempty"`
}

// ---- Dashboard types ----

// DashboardTab enumerates allowed UI tabs.
// +kubebuilder:validation:Enum=workloads;ingresses;services;secrets;yaml
type DashboardTab string

const (
	DashboardTabWorkloads DashboardTab = "workloads"
	DashboardTabIngresses DashboardTab = "ingresses"
	DashboardTabServices  DashboardTab = "services"
	DashboardTabSecrets   DashboardTab = "secrets"
	DashboardTabYAML      DashboardTab = "yaml"
)

// ApplicationDefinitionDashboard describes how this resource appears in the UI.
type ApplicationDefinitionDashboard struct {
	// Human-readable name shown in the UI (e.g., "Bucket")
	Singular string `json:"singular"`
	// Plural human-readable name (e.g., "Buckets")
	Plural string `json:"plural"`
	// Hard-coded name used in the UI (e.g., "bucket")
	// +optional
	Name string `json:"name,omitempty"`
	// Whether this resource is singular (not a collection) in the UI
	// +optional
	SingularResource bool `json:"singularResource,omitempty"`
	// Order weight for sorting resources in the UI (lower first)
	// +optional
	Weight int `json:"weight,omitempty"`
	// Short description shown in catalogs or headers (e.g., "S3 compatible storage")
	// +optional
	Description string `json:"description,omitempty"`
	// Icon encoded as a string (e.g., inline SVG, base64, or data URI)
	// +optional
	Icon string `json:"icon,omitempty"`
	// Category used to group resources in the UI (e.g., "Storage", "Networking")
	Category string `json:"category"`
	// Free-form tags for search and filtering
	// +optional
	Tags []string `json:"tags,omitempty"`
	// Which tabs to show for this resource
	// +optional
	Tabs []DashboardTab `json:"tabs,omitempty"`
	// Order of keys in the YAML view
	// +optional
	KeysOrder [][]string `json:"keysOrder,omitempty"`
	// Whether this resource is a module (tenant module)
	// +optional
	Module bool `json:"module,omitempty"`
}
