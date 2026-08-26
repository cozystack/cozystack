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

package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

// IndexEntry is one metadata-only record in the community package index. The
// index stores no artifacts, only where to find them and who owns them.
type IndexEntry struct {
	// Name is the short name a user can `cozypkg tap <name>` / `search` by.
	Name string `json:"name"`
	// OCIRef is the artifact location, e.g. oci://ghcr.io/org/repo.
	OCIRef string `json:"ociRef"`
	// Version is the OCI tag of the listed release; a version bump updates it.
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Maintainer  string   `json:"maintainer,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Signing records the expected cosign identity for the two-lane index gate:
	// an owner version-bump must stay signed by this identity.
	Signing *IndexSigning `json:"signing,omitempty"`
}

// IndexSigning is the expected cosign OIDC signing identity for an entry.
type IndexSigning struct {
	Identity string `json:"identity,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
}

// loadIndex reads every *.yaml/*.yml entry under dir into IndexEntry values.
// A file that fails strict decoding, or an entry missing name/ociRef, is a hard
// error so the CI gate (and search) surface a malformed index rather than
// silently dropping entries.
func loadIndex(dir string) ([]IndexEntry, error) {
	if !isDir(dir) {
		return nil, fmt.Errorf("index path %q is not a directory", dir)
	}
	var entries []IndexEntry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(dir, path)
		var entry IndexEntry
		if err := yaml.UnmarshalStrict(data, &entry); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if entry.Name == "" || entry.OCIRef == "" {
			return fmt.Errorf("%s: index entry must set both name and ociRef", rel)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// filterEntries returns entries whose name, description, or tags contain term
// (case-insensitive). An empty term matches everything.
func filterEntries(entries []IndexEntry, term string) []IndexEntry {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return entries
	}
	var out []IndexEntry
	for _, e := range entries {
		hay := strings.ToLower(e.Name + " " + e.Description + " " + strings.Join(e.Tags, " "))
		if strings.Contains(hay, term) {
			out = append(out, e)
		}
	}
	return out
}

// findEntry returns the entry with the exact given name, or false.
func findEntry(entries []IndexEntry, name string) (IndexEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return IndexEntry{}, false
}

// resolveIndexDir turns an index location (a local directory or an oci://
// reference) into a local directory, returning a cleanup function.
func resolveIndexDir(indexRef string) (string, func(), error) {
	if indexRef == "" {
		return "", func() {}, fmt.Errorf("no index configured; pass --index <path|oci-ref> or set COZYPKG_INDEX")
	}
	if isOCIRef(indexRef) {
		return pullOCIArtifact(indexRef)
	}
	if !isDir(indexRef) {
		return "", func() {}, fmt.Errorf("index path %q is not a directory", indexRef)
	}
	return indexRef, func() {}, nil
}

// defaultIndexRef returns the configured index location from the flag or the
// COZYPKG_INDEX environment variable.
func defaultIndexRef(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("COZYPKG_INDEX")
}

func printEntries(entries []IndexEntry, w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tOCI REF\tDESCRIPTION")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.OCIRef, e.Description)
	}
	tw.Flush()
}

var searchCmdFlags struct {
	index string
}

var searchCmd = &cobra.Command{
	Use:   "search [term]",
	Short: "Search the community package index",
	Long: `Search queries the community package index (a directory of metadata-only
entries, local or pulled from an oci:// reference) and lists matching packages
without tapping them. Configure the index with --index or COZYPKG_INDEX.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		term := ""
		if len(args) == 1 {
			term = args[0]
		}
		indexRef := defaultIndexRef(searchCmdFlags.index)
		dir, cleanup, err := resolveIndexDir(indexRef)
		if err != nil {
			return err
		}
		defer cleanup()

		entries, err := loadIndex(dir)
		if err != nil {
			return err
		}
		matches := filterEntries(entries, term)
		if len(matches) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No packages match %q\n", term)
			return nil
		}
		printEntries(matches, cmd.OutOrStdout())
		return nil
	},
}

// resolveTapTarget resolves a tap argument to an oci:// reference. An oci://
// argument is returned unchanged; any other value is treated as a short name
// looked up in the index.
func resolveTapTarget(arg, indexRef string) (string, error) {
	if isOCIRef(arg) {
		return arg, nil
	}
	dir, cleanup, err := resolveIndexDir(indexRef)
	if err != nil {
		return "", fmt.Errorf("%q is not an oci:// reference and it could not be resolved via the index: %w", arg, err)
	}
	defer cleanup()
	entries, err := loadIndex(dir)
	if err != nil {
		return "", err
	}
	entry, ok := findEntry(entries, arg)
	if !ok {
		return "", fmt.Errorf("no index entry named %q", arg)
	}
	// Pin to the entry's recorded version: the CI gate validated and
	// cosign-verified <ociRef>:<version>, so tapping must consume that exact
	// tag rather than defaulting to latest.
	if entry.Version != "" {
		return entry.OCIRef + ":" + entry.Version, nil
	}
	return entry.OCIRef, nil
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().StringVar(&searchCmdFlags.index, "index", "", "Index location: a local directory or an oci:// reference (defaults to COZYPKG_INDEX)")
}
