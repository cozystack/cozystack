// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package tap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

var sanitizeDNSRe = regexp.MustCompile(`[^a-z0-9-]+`)

// connectTarget is the parsed form of a connect request's oci:// URL.
type connectTarget struct {
	URL  string // reference without a tag
	Org  string
	Repo string
	Tag  string
	// PackageSourceName is community.<org>.<repo>; FluxSourceName is its RFC-1123 form.
	PackageSourceName string
	FluxSourceName    string
}

// parseConnectURL parses an oci:// URL (and an optional tag override) into the
// names a connect creates. It mirrors the CLI's tap parsing so the API and CLI
// agree on community naming.
func parseConnectURL(url, tagOverride string) (connectTarget, error) {
	if !strings.HasPrefix(url, "oci://") {
		return connectTarget{}, fmt.Errorf("url %q must start with oci://", url)
	}
	body := strings.TrimPrefix(url, "oci://")
	if strings.Contains(body, "@") {
		return connectTarget{}, fmt.Errorf("digest references are not supported; use a tag")
	}
	tag := tagOverride
	if colon := strings.LastIndex(body, ":"); colon >= 0 && !strings.Contains(body[colon:], "/") {
		if tag == "" {
			tag = body[colon+1:]
		}
		body = body[:colon]
	}
	segs := strings.Split(strings.Trim(body, "/"), "/")
	if len(segs) < 2 {
		return connectTarget{}, fmt.Errorf("url %q must include a host and a repository path", url)
	}
	t := connectTarget{URL: "oci://" + body, Repo: segs[len(segs)-1], Tag: tag}
	if len(segs) >= 3 {
		t.Org = segs[len(segs)-2]
	}
	if t.Tag == "" {
		t.Tag = "latest"
	}
	nameBase := t.Repo
	if t.Org != "" {
		nameBase = t.Org + "." + t.Repo
	}
	t.PackageSourceName = "community." + nameBase
	fluxBase := t.Repo
	if t.Org != "" {
		fluxBase = t.Org + "-" + t.Repo
	}
	t.FluxSourceName = "community-" + sanitizeDNSRe.ReplaceAllString(strings.ToLower(fluxBase), "-")
	return t, nil
}

var (
	gvrPackageSources = schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "packagesources"}
	gvrAppDefs        = schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "applicationdefinitions"}
	gvrOCIRepos       = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}
)

// anyOtherReferences reports whether a PackageSource other than excludeName
// still points at the given Flux source, so a tap's source is only deleted when
// nothing else uses it.
func anyOtherReferences(pss []cozyv1alpha1.PackageSource, srcName, excludeName string) bool {
	for i := range pss {
		if pss[i].Name == excludeName {
			continue
		}
		if ref := pss[i].Spec.SourceRef; ref != nil && ref.Name == srcName {
			return true
		}
	}
	return false
}

// artifactName reproduces the per-component artifact name the PackageSource
// reconciler generates (dots in the PackageSource name become dashes, joined
// with the variant and component). It is the key that links a component to the
// ApplicationDefinition whose release.chartRef.name references its artifact.
func artifactName(psName, variant, component string) string {
	return strings.ReplaceAll(psName, ".", "-") + "-" + variant + "-" + component
}

// indexAppDefsByChartRef indexes ApplicationDefinitions by their ExternalArtifact
// chartRef name, the value artifactName produces for the component they back.
func indexAppDefsByChartRef(ads []cozyv1alpha1.ApplicationDefinition) map[string]cozyv1alpha1.ApplicationDefinition {
	idx := make(map[string]cozyv1alpha1.ApplicationDefinition, len(ads))
	for _, ad := range ads {
		ref := ad.Spec.Release.ChartRef
		if ref == nil || ref.Name == "" {
			continue
		}
		idx[ref.Name] = ad
	}
	return idx
}

// buildTap computes a Tap from a PackageSource and the ApplicationDefinition
// index. A package is emitted for each component whose assembled-artifact name
// matches an ApplicationDefinition, deduplicated by application name across
// variants (privileged is ORed over occurrences).
func buildTap(ps cozyv1alpha1.PackageSource, idx map[string]cozyv1alpha1.ApplicationDefinition) corev1alpha1.Tap {
	tap := corev1alpha1.Tap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Tap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            ps.Name,
			ResourceVersion: "0",
		},
		Spec: corev1alpha1.TapSpec{
			Community: strings.HasPrefix(ps.Name, "community."),
		},
	}
	if ps.Spec.SourceRef != nil {
		tap.Spec.Source = corev1alpha1.TapSource{
			Kind: ps.Spec.SourceRef.Kind,
			Name: ps.Spec.SourceRef.Name,
		}
	}
	for _, cond := range ps.Status.Conditions {
		if cond.Type == "Ready" {
			tap.Spec.Ready = cond.Status == metav1.ConditionTrue
			tap.Spec.Message = cond.Message
		}
	}

	// Each ApplicationDefinition's chartRef names exactly one component's
	// assembled artifact (the artifact name embeds the variant), so a package
	// maps to a single (variant, component). The seen guard is only a defensive
	// backstop against an index that somehow yields the same app twice.
	seen := map[string]bool{}
	for _, v := range ps.Spec.Variants {
		for _, c := range v.Components {
			ad, ok := idx[artifactName(ps.Name, v.Name, c.Name)]
			if !ok || seen[ad.Name] {
				continue
			}
			seen[ad.Name] = true
			pkg := corev1alpha1.TapPackage{
				Name:       ad.Name,
				Kind:       ad.Spec.Application.Kind,
				Component:  c.Name,
				Privileged: c.Install != nil && c.Install.Privileged,
			}
			if d := ad.Spec.Dashboard; d != nil {
				pkg.Description = d.Description
				pkg.Category = d.Category
				pkg.Tags = d.Tags
				pkg.Icon = d.Icon
			}
			tap.Spec.Packages = append(tap.Spec.Packages, pkg)
		}
	}
	sort.Slice(tap.Spec.Packages, func(i, j int) bool {
		return tap.Spec.Packages[i].Name < tap.Spec.Packages[j].Name
	})
	return tap
}
