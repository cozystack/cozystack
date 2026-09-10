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

// Package httpcache holds the values type of the HTTPCache application.
//
// Deprecated: Cozystack no longer ships the HTTPCache application. The chart
// and its ResourceDefinition were removed, so no new instance can be created
// through the catalog and types.go can no longer be regenerated — its
// values-gen input is gone and the file is frozen as it last shipped.
//
// The package is kept because api/apps/v1alpha1 is a published Go module:
// deleting a package from it breaks every consumer that bumps its pin,
// cozystack/terraform-provider-cozystack among them, which still offers a
// cozystack_httpcache resource. Instances deployed before the removal keep
// running and stay addressable, so that resource keeps working against them.
//
// Note that the type is not what gives those instances their schema. The
// aggregated API serves the OpenAPI schema recorded in the in-cluster
// ApplicationDefinition (see internal/apigate), so a cluster carrying
// HTTPCache instances is unaffected by anything in this package.
package httpcache
