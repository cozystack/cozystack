// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package naming

import (
	"fmt"
	"strings"
	"testing"
)

// referenceArtifactName is the formula the PackageSource reconciler uses
// (internal/operator/packagesource_reconciler.go): dots replaced by dashes in
// every segment. ArtifactName must match it exactly, or the marketplace
// catalog and the validator silently mis-link components to their
// ApplicationDefinitions.
func referenceArtifactName(ps, variant, component string) string {
	return fmt.Sprintf("%s-%s-%s",
		strings.ReplaceAll(ps, ".", "-"),
		strings.ReplaceAll(variant, ".", "-"),
		strings.ReplaceAll(component, ".", "-"))
}

func TestArtifactNameMatchesReconciler(t *testing.T) {
	cases := [][3]string{
		{"cozystack.postgres-application", "default", "postgres"},
		// Dotted variant and component: the case the earlier single-segment
		// implementation got wrong.
		{"community.org.repo", "v1.0", "my.app"},
		{"community.acme.stack", "isp-hosted", "edge.gateway"},
	}
	for _, c := range cases {
		got := ArtifactName(c[0], c[1], c[2])
		want := referenceArtifactName(c[0], c[1], c[2])
		if got != want {
			t.Errorf("ArtifactName(%q,%q,%q) = %q, want %q", c[0], c[1], c[2], got, want)
		}
		if strings.Contains(got, ".") {
			t.Errorf("ArtifactName(%q,%q,%q) = %q still contains a dot", c[0], c[1], c[2], got)
		}
	}
}

func TestArtifactPrefixIsArtifactNameWithoutComponent(t *testing.T) {
	cases := [][3]string{
		{"example.hello", "default", "hello"},
		{"community.org.repo", "v1.0", "my.app"},
	}
	for _, c := range cases {
		prefix := ArtifactPrefix(c[0], c[1])
		full := ArtifactName(c[0], c[1], c[2])
		want := prefix + "-" + strings.ReplaceAll(c[2], ".", "-")
		if full != want {
			t.Errorf("ArtifactName(%q,%q,%q)=%q, but prefix %q + component != it", c[0], c[1], c[2], full, prefix)
		}
		if strings.Contains(prefix, ".") {
			t.Errorf("ArtifactPrefix(%q,%q)=%q still contains a dot", c[0], c[1], prefix)
		}
	}
}
