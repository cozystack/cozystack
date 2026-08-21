// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package naming holds the single definition of the per-component artifact name
// that the PackageSource reconciler generates. The marketplace Tap resource,
// the cozypkg validator, and the tap materializer all resolve a component to
// its ApplicationDefinition through this name, so it lives in one place to keep
// them from drifting apart.
package naming

import "strings"

// ArtifactName reproduces the assembled-artifact name the PackageSource
// reconciler emits per (packageSource, variant, component): the PackageSource
// name with dots replaced by dashes, joined with the variant and component.
// It must match the reconciler's own naming in
// internal/operator/packagesource_reconciler.go.
func ArtifactName(packageSourceName, variant, component string) string {
	return strings.ReplaceAll(packageSourceName, ".", "-") + "-" + variant + "-" + component
}
