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
// reconciler emits per (packageSource, variant, component). The reconciler
// replaces dots with dashes in ALL THREE segments (Kubernetes object names
// forbid dots), so this must too — a third-party variant or component name may
// legitimately contain a dot. It must match the reconciler's own naming in
// internal/operator/packagesource_reconciler.go.
func ArtifactName(packageSourceName, variant, component string) string {
	return ArtifactPrefix(packageSourceName, variant) + "-" +
		strings.ReplaceAll(component, ".", "-")
}

// ArtifactPrefix is the per-(packageSource, variant) prefix of ArtifactName,
// i.e. ArtifactName without the trailing component segment. A community repo's
// cozyrds ApplicationDefinition templates its chartRef.name as
// "<prefix>-<component>", so the prefix (which carries the tap-time rename) can
// be injected as a value while the component segment stays a literal.
func ArtifactPrefix(packageSourceName, variant string) string {
	return strings.ReplaceAll(packageSourceName, ".", "-") + "-" +
		strings.ReplaceAll(variant, ".", "-")
}
