/*
Copyright 2026 The Cozystack Authors.

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

package apigate

import (
	"fmt"
	"sort"
)

// Classify compares the base and head snapshots and returns every sizeable
// change, ordered deterministically (new groups, then new resources, then
// breaking changes; each block sorted by group/plural/detail). An empty slice
// means the change set is not sizeable and needs no designated-owner review.
func Classify(base, head Snapshot) []Finding {
	var findings []Finding

	baseGroups := base.groups()
	// New API groups: present in head, absent in base. Reported once per group
	// against the first (sorted) resource that introduces it.
	newGroups := map[string]struct{}{}
	for _, res := range sortedResources(head) {
		if _, ok := baseGroups[res.Group]; ok {
			continue
		}
		if _, done := newGroups[res.Group]; done {
			continue
		}
		newGroups[res.Group] = struct{}{}
		findings = append(findings, Finding{
			Category: NewGroup,
			Group:    res.Group,
			Kind:     res.Kind,
			Plural:   res.Plural,
			Source:   res.Source,
			Origin:   res.Origin,
			Detail:   fmt.Sprintf("API group %q is introduced by this change", res.Group),
		})
	}

	// New resources: (group, plural) present in head, absent in base. A
	// resource whose group is itself new is already covered by the new-group
	// finding, so skip it here to avoid double-flagging.
	for _, res := range sortedResources(head) {
		if _, ok := base[res.key()]; ok {
			continue
		}
		if _, groupIsNew := newGroups[res.Group]; groupIsNew {
			continue
		}
		findings = append(findings, Finding{
			Category: NewResource,
			Group:    res.Group,
			Kind:     res.Kind,
			Plural:   res.Plural,
			Source:   res.Source,
			Origin:   res.Origin,
			Detail:   fmt.Sprintf("resource %s (%s) is added to group %q", res.Plural, displayKind(res), res.Group),
		})
	}

	// Removed API groups: present in base, absent in head. Reported once per
	// group against the first (sorted) base resource that carried it.
	headGroups := head.groups()
	removedGroups := map[string]struct{}{}
	for _, res := range sortedResources(base) {
		if _, ok := headGroups[res.Group]; ok {
			continue
		}
		if _, done := removedGroups[res.Group]; done {
			continue
		}
		removedGroups[res.Group] = struct{}{}
		findings = append(findings, Finding{
			Category: RemovedGroup,
			Group:    res.Group,
			Kind:     res.Kind,
			Plural:   res.Plural,
			Source:   res.Source,
			Origin:   res.Origin,
			Detail:   fmt.Sprintf("API group %q is removed by this change", res.Group),
		})
	}

	// Removed resources: present in base, absent in head. A resource whose
	// whole group was removed is already covered above, so skip it here.
	for _, res := range sortedResources(base) {
		if _, ok := head[res.key()]; ok {
			continue
		}
		if _, groupGone := removedGroups[res.Group]; groupGone {
			continue
		}
		findings = append(findings, Finding{
			Category: RemovedResource,
			Group:    res.Group,
			Kind:     res.Kind,
			Plural:   res.Plural,
			Source:   res.Source,
			Origin:   res.Origin,
			Detail:   fmt.Sprintf("resource %s (%s) is removed from group %q", res.Plural, displayKind(res), res.Group),
		})
	}

	// Breaking changes: resources present in both, whose schema regresses.
	for _, res := range sortedResources(head) {
		prev, ok := base[res.key()]
		if !ok {
			continue
		}
		for _, detail := range diffResource(prev, res) {
			findings = append(findings, Finding{
				Category: Breaking,
				Group:    res.Group,
				Kind:     res.Kind,
				Plural:   res.Plural,
				Source:   res.Source,
				Origin:   res.Origin,
				Detail:   detail,
			})
		}
	}

	return findings
}

// diffResource returns human-readable descriptions of every breaking change
// between the base and head form of one resource: a removed served version, or
// a breaking schema change within a shared version. New versions are additive
// and ignored.
func diffResource(base, head Resource) []string {
	var out []string
	versions := make([]string, 0, len(base.Versions))
	for v := range base.Versions {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	for _, v := range versions {
		headSchema, ok := head.Versions[v]
		if !ok {
			out = append(out, fmt.Sprintf("served version %q was removed", v))
			continue
		}
		for _, c := range diffSchema("spec", base.Versions[v], headSchema) {
			out = append(out, fmt.Sprintf("[%s] %s", v, c))
		}
	}
	return out
}

func displayKind(r Resource) string {
	if r.Kind != "" {
		return r.Kind
	}
	return r.Plural
}

// sortedResources returns a snapshot's resources in a stable order for
// deterministic output.
func sortedResources(s Snapshot) []Resource {
	out := make([]Resource, 0, len(s))
	for _, r := range s {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Plural < out[j].Plural
	})
	return out
}
