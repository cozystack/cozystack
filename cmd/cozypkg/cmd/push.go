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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var pushCmdFlags struct {
	path         string
	source       string
	revision     string
	reproducible bool
	skipValidate bool
	helmLint     bool
}

// buildFluxPushArgs assembles the argument vector for `flux push artifact`. It
// is factored out so the exact command shape can be unit-tested without a
// registry. The shape mirrors the platform's own push
// (packages/core/installer/Makefile): the packages/ directory is the artifact
// root, with a source URL and a revision for provenance.
func buildFluxPushArgs(ref, packagesDir, source, revision string, reproducible bool) []string {
	args := []string{
		"push", "artifact", ref,
		"--path=" + packagesDir,
		"--source=" + source,
		"--revision=" + revision,
	}
	if reproducible {
		args = append(args, "--reproducible")
	}
	return args
}

// gitOutput runs a git command in dir and returns trimmed stdout, or "" on any error.
func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// deriveSource returns the git remote URL for provenance, or "" if unavailable.
func deriveSource(dir string) string {
	return gitOutput(dir, "remote", "get-url", "origin")
}

// deriveRevision returns "<describe>:<sha>", or just "<sha>", or "" if not a git tree.
func deriveRevision(dir string) string {
	sha := gitOutput(dir, "rev-parse", "HEAD")
	if sha == "" {
		return ""
	}
	if desc := gitOutput(dir, "describe", "--tags", "--always"); desc != "" && desc != sha {
		return desc + ":" + sha
	}
	return sha
}

var pushCmd = &cobra.Command{
	Use:   "push <oci-ref>",
	Short: "Validate and push an External-Apps repository as an OCI artifact",
	Long: `Push bundles the repository's packages/ tree into a single versioned OCI
artifact using the flux CLI, the same artifact shape the platform and a
'cozypkg tap' consume. The repository is validated first (unless
--skip-validate); a validation error aborts the push before anything is
published. Source URL and revision are derived from git when not given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := args[0]
		if !isOCIRef(ref) {
			return fmt.Errorf("target %q must be an oci:// reference", ref)
		}

		root := pushCmdFlags.path
		packagesDir := filepath.Join(root, "packages")
		if !isDir(packagesDir) {
			return fmt.Errorf("no packages/ directory found under %s", root)
		}

		if !pushCmdFlags.skipValidate {
			rep, err := ValidateRepo(ValidateOptions{RepoRoot: root, RunHelmLint: pushCmdFlags.helmLint})
			if err != nil {
				return err
			}
			printReport(rep, cmd.OutOrStdout())
			if rep.HasErrors() {
				errs, _, _ := rep.Counts()
				return fmt.Errorf("refusing to push: validation failed with %d error(s) (use --skip-validate to override)", errs)
			}
		}

		source := pushCmdFlags.source
		if source == "" {
			source = deriveSource(root)
		}
		if source == "" {
			return fmt.Errorf("could not determine --source (not a git checkout with an 'origin' remote); pass --source explicitly")
		}
		revision := pushCmdFlags.revision
		if revision == "" {
			revision = deriveRevision(root)
		}
		if revision == "" {
			return fmt.Errorf("could not determine --revision (not a git checkout); pass --revision explicitly")
		}

		fluxArgs := buildFluxPushArgs(ref, packagesDir, source, revision, pushCmdFlags.reproducible)
		fmt.Fprintf(cmd.OutOrStdout(), "Pushing %s (source=%s revision=%s)\n", ref, source, revision)
		flux := exec.Command("flux", fluxArgs...)
		flux.Stdout = cmd.OutOrStdout()
		flux.Stderr = cmd.ErrOrStderr()
		flux.Stdin = os.Stdin
		if err := flux.Run(); err != nil {
			return fmt.Errorf("flux push artifact failed: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pushCmd)
	pushCmd.Flags().StringVar(&pushCmdFlags.path, "path", ".", "Path to the repository root (must contain packages/)")
	pushCmd.Flags().StringVar(&pushCmdFlags.source, "source", "", "Source URL recorded in the artifact (defaults to git origin remote)")
	pushCmd.Flags().StringVar(&pushCmdFlags.revision, "revision", "", "Revision recorded in the artifact (defaults to git describe:sha)")
	pushCmd.Flags().BoolVar(&pushCmdFlags.reproducible, "reproducible", false, "Pass --reproducible to flux for deterministic artifact metadata")
	pushCmd.Flags().BoolVar(&pushCmdFlags.skipValidate, "skip-validate", false, "Skip pre-push validation (not recommended)")
	pushCmd.Flags().BoolVar(&pushCmdFlags.helmLint, "helm-lint", false, "Also run 'helm lint' during pre-push validation")
}
