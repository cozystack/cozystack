// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package tapconst holds the label, annotation, and finalizer keys shared
// between the marketplace API (which creates a community tap's Flux source) and
// the operator's tap materializer (which turns that source into PackageSources).
// They live here so the two sides can never drift on a load-bearing key.
package tapconst

const (
	// Prefix namespaces a community-tapped PackageSource name
	// (community.<org>.<repo>) so a third-party package can never shadow an
	// official one. It is load-bearing: the untap/disconnect guard, the
	// community-vs-official badge, and the anti-shadowing guarantee all key on
	// it, so it lives here as the single definition shared by every side.
	Prefix = "community."
	// Label marks a Flux source (and the PackageSources materialized from it)
	// as belonging to a dashboard-created community tap.
	Label = "apps.cozystack.io/marketplace-tap"
	// NameAnnotation records the community.<org>.<repo> base name for the tap on
	// the OCIRepository, set by the API when a tap is connected.
	NameAnnotation = "apps.cozystack.io/tap-name"
	// SourceAnnotation links a materialized PackageSource back to its tap's Flux
	// source name, so the source's finalizer can clean them up.
	SourceAnnotation = "apps.cozystack.io/tap-source"
	// MaterializedRevisionAnnotation records the artifact revision last
	// materialized, so an unchanged revision is not re-pulled.
	MaterializedRevisionAnnotation = "apps.cozystack.io/materialized-revision"
	// Finalizer keeps the OCIRepository until its materialized PackageSources
	// are cleaned up.
	Finalizer = "apps.cozystack.io/tap-materializer"
)
