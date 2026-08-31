// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// Package tapconst holds the label, annotation, and finalizer keys shared
// between the marketplace API (which creates a community tap's Flux source) and
// the operator's tap materializer (which turns that source into PackageSources).
// They live here so the two sides can never drift on a load-bearing key.
package tapconst

const (
	// Prefix is retained only until the reserved-name check and the apiserver
	// Tap compute stop referencing it; tapped repositories keep their own
	// declared names, so nothing renames into this prefix any more.
	//
	// Deprecated: tap-ness is identified by Label, not by this name prefix.
	Prefix = "community."
	// Label marks a Flux source (and the PackageSources materialized from it) as
	// belonging to a tap. It is load-bearing: the untap/disconnect guard, the
	// tapped-vs-official badge, and idempotent re-tap all key on this marker
	// (not on a name prefix), because a tapped repository keeps its own declared
	// name. It lives here as the single definition shared by every side.
	Label = "apps.cozystack.io/marketplace-tap"
	// NameAnnotation records the tap's own OCIRepository object name, set when a
	// tap is connected, so the dashboard orphan-disconnect can find the source.
	NameAnnotation = "apps.cozystack.io/tap-name"
	// SourceAnnotation links a materialized PackageSource back to its tap's Flux
	// source name, so the source's finalizer can clean them up and a re-tap can
	// recognise its own PackageSources.
	SourceAnnotation = "apps.cozystack.io/tap-source"
	// MaterializedRevisionAnnotation records the artifact revision last
	// materialized, so an unchanged revision is not re-pulled.
	MaterializedRevisionAnnotation = "apps.cozystack.io/materialized-revision"
	// MaterializeErrorAnnotation records why the last materialization failed
	// (e.g. a name collision with a core component), so the failure surfaces on
	// the Tap resource and dashboard instead of only in the operator log.
	MaterializeErrorAnnotation = "apps.cozystack.io/tap-error"
	// Finalizer keeps the OCIRepository until its materialized PackageSources
	// are cleaned up.
	Finalizer = "apps.cozystack.io/tap-materializer"
)
