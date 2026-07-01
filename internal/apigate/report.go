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
	"strings"
)

// categoryTitles gives each category a stable, human-readable heading.
var categoryTitles = map[Category]string{
	NewGroup:    "New API group",
	NewResource: "New resource",
	Breaking:    "Breaking change to existing API",
}

// Report renders findings for CI consumption. format "markdown" (default)
// produces a PR-comment-friendly summary; any other value produces plain text.
func Report(findings []Finding, format string) string {
	if len(findings) == 0 {
		return "No sizeable API changes detected.\n"
	}
	var b strings.Builder
	md := format != "text"

	if md {
		b.WriteString("### Sizeable API change detected\n\n")
		b.WriteString("This change touches the API surface in a way that requires review from a designated API owner.\n\n")
	} else {
		b.WriteString("Sizeable API change detected:\n\n")
	}

	for _, cat := range []Category{NewGroup, NewResource, Breaking} {
		group := findingsOf(findings, cat)
		if len(group) == 0 {
			continue
		}
		if md {
			fmt.Fprintf(&b, "#### %s\n\n", categoryTitles[cat])
		} else {
			fmt.Fprintf(&b, "%s:\n", categoryTitles[cat])
		}
		for _, f := range group {
			bullet := md
			line := fmt.Sprintf("%s %s — %s (%s)", identity(f), f.Detail, f.Origin, f.Source)
			if bullet {
				fmt.Fprintf(&b, "- %s\n", line)
			} else {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func identity(f Finding) string {
	kind := f.Kind
	if kind == "" {
		kind = f.Plural
	}
	return fmt.Sprintf("**%s/%s**", f.Group, kind)
}

func findingsOf(findings []Finding, cat Category) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Category == cat {
			out = append(out, f)
		}
	}
	return out
}
