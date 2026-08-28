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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/naming"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	"github.com/spf13/cobra"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// ociPullTimeout bounds a `flux pull artifact` so an unreachable registry
// fails with a legible error instead of hanging indefinitely.
const ociPullTimeout = 2 * time.Minute

// Severity classifies a validation finding.
type Severity string

const (
	// SeverityError marks a hard failure that blocks publication.
	SeverityError Severity = "ERROR"
	// SeverityWarning marks something suspicious that does not block on its own.
	SeverityWarning Severity = "WARNING"
	// SeverityInfo is informational only (e.g. a privileged component was noted).
	SeverityInfo Severity = "INFO"
)

// Finding is a single validation observation about a repository.
type Finding struct {
	Severity Severity
	// Code is a short machine-readable slug for the finding class.
	Code string
	// Location is the repo-relative file or resource the finding refers to.
	Location string
	// Message is the human-readable description.
	Message string
}

// Report accumulates findings from a validation run.
type Report struct {
	Findings []Finding
}

func (r *Report) add(sev Severity, code, location, format string, args ...interface{}) {
	r.Findings = append(r.Findings, Finding{
		Severity: sev,
		Code:     code,
		Location: location,
		Message:  fmt.Sprintf(format, args...),
	})
}

// HasErrors reports whether any finding is a hard error.
func (r *Report) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Counts returns the number of error, warning and info findings.
func (r *Report) Counts() (errs, warns, infos int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityError:
			errs++
		case SeverityWarning:
			warns++
		case SeverityInfo:
			infos++
		}
	}
	return
}

// ValidateOptions configures a repository validation run.
type ValidateOptions struct {
	// RepoRoot is a local directory holding the External-Apps repository tree.
	RepoRoot string
	// RunHelmLint controls whether `helm lint` is executed for each component chart.
	RunHelmLint bool
	// KnownSources is an allowlist of PackageSource names that dependsOn entries
	// may reference even though they are not defined inside this repository
	// (e.g. sources shipped by the platform itself).
	KnownSources []string
	// AllowReservedNames disables the reserved-prefix check. It exists only as a
	// caller-side escape hatch: the index publication gate never sets it, so a
	// submitter cannot hand it to themselves to claim a platform/marketplace name.
	AllowReservedNames bool
}

// reservedNamePrefixes are PackageSource name prefixes a third-party repository
// may not claim: "cozystack." is the platform's own namespace, and
// "community." is the namespace the tap materializer reserves for renamed
// community sources. Forbidding both is the structural guarantee that a
// third-party package can never shadow an official or already-tapped one.
var reservedNamePrefixes = []string{"cozystack.", tapconst.Prefix}

type loadedPackageSource struct {
	File string
	PS   cozyv1alpha1.PackageSource
}

type loadedAppDef struct {
	File string
	AD   cozyv1alpha1.ApplicationDefinition
}

type typeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// splitYAMLDocuments splits a multi-document YAML byte slice into individual
// documents, dropping empty ones. It uses the apimachinery reader so a block
// scalar containing a separator-looking line does not fracture a document (the
// same reader the operator's materializer uses, so both decode identically).
func splitYAMLDocuments(data []byte) [][]byte {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var out [][]byte
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Malformed stream; stop here and let the per-document decode below
			// surface a schema error for whatever was already read.
			break
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		out = append(out, doc)
	}
	return out
}

// artifactName links a component to its ApplicationDefinition via the assembled
// artifact name; it delegates to the shared naming helper so the validator and
// the marketplace backend cannot drift on this convention.
func artifactName(psName, variant, component string) string {
	return naming.ArtifactName(psName, variant, component)
}

// artifactRoot returns the directory that plays the role of the OCI artifact
// root. A source repository nests everything under packages/; a pulled OCI
// artifact has that prefix stripped, so we fall back to the given root.
func artifactRoot(root string) string {
	p := filepath.Join(root, "packages")
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return root
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// loadManifests walks the repository tree and decodes every PackageSource and
// ApplicationDefinition manifest it finds. Decode failures are recorded as
// findings rather than aborting the walk.
func loadManifests(root string, r *Report) ([]loadedPackageSource, []loadedAppDef) {
	var pss []loadedPackageSource
	var ads []loadedAppDef

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
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
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, doc := range splitYAMLDocuments(data) {
			var tm typeMeta
			if err := yaml.Unmarshal(doc, &tm); err != nil {
				continue
			}
			switch tm.Kind {
			case "PackageSource":
				var ps cozyv1alpha1.PackageSource
				if err := yaml.UnmarshalStrict(doc, &ps); err != nil {
					r.add(SeverityError, "schema", rel, "PackageSource failed strict decode: %v", err)
					continue
				}
				pss = append(pss, loadedPackageSource{File: rel, PS: ps})
			case "ApplicationDefinition":
				// cozyrds assets are emitted verbatim, but be defensive: a file
				// still carrying Helm template actions cannot be decoded offline.
				if strings.Contains(string(doc), "{{") {
					continue
				}
				var ad cozyv1alpha1.ApplicationDefinition
				if err := yaml.UnmarshalStrict(doc, &ad); err != nil {
					r.add(SeverityError, "schema", rel, "ApplicationDefinition failed strict decode: %v", err)
					continue
				}
				ads = append(ads, loadedAppDef{File: rel, AD: ad})
			}
		}
		return nil
	})

	return pss, ads
}

// ValidateRepo runs all offline checks against a local repository tree and
// returns the accumulated report. It only returns an error for problems that
// prevent validation from running at all (e.g. the path is not a directory).
func ValidateRepo(opts ValidateOptions) (*Report, error) {
	r := &Report{}
	if !isDir(opts.RepoRoot) {
		return nil, fmt.Errorf("repository path %q is not a directory", opts.RepoRoot)
	}

	pss, ads := loadManifests(opts.RepoRoot, r)
	if len(pss) == 0 {
		r.add(SeverityError, "no-packagesource", "", "no PackageSource manifest found under %s", opts.RepoRoot)
		return r, nil
	}

	psNames := map[string]bool{}
	expectedArtifacts := map[string]bool{}
	for _, l := range pss {
		psNames[l.PS.Name] = true
		for _, v := range l.PS.Spec.Variants {
			for _, c := range v.Components {
				expectedArtifacts[artifactName(l.PS.Name, v.Name, c.Name)] = true
			}
		}
	}

	artRoot := artifactRoot(opts.RepoRoot)
	for _, l := range pss {
		validatePackageSource(artRoot, l, r, opts)
	}
	checkDependsOn(pss, psNames, r, opts)
	checkAppDefs(ads, expectedArtifacts, r)

	return r, nil
}

func validatePackageSource(artRoot string, l loadedPackageSource, r *Report, opts ValidateOptions) {
	ps := l.PS
	if ps.Name == "" {
		r.add(SeverityError, "ps-name", l.File, "PackageSource has empty metadata.name")
	} else if !opts.AllowReservedNames {
		for _, p := range reservedNamePrefixes {
			if strings.HasPrefix(ps.Name, p) {
				r.add(SeverityError, "ps-name-reserved", l.File, "PackageSource %s uses reserved name prefix %q; that namespace is platform/marketplace-managed, pick a neutral name (e.g. <org>.<repo>)", ps.Name, p)
				break
			}
		}
	}
	if ps.Spec.SourceRef != nil {
		k := ps.Spec.SourceRef.Kind
		if k != "GitRepository" && k != "OCIRepository" {
			r.add(SeverityError, "sourceref-kind", l.File, "PackageSource %s sourceRef.kind %q must be GitRepository or OCIRepository", ps.Name, k)
		}
	}
	if len(ps.Spec.Variants) == 0 {
		r.add(SeverityError, "no-variants", l.File, "PackageSource %s has no variants", ps.Name)
		return
	}

	seenVariant := map[string]bool{}
	seenArtifacts := map[string]string{} // artifact name -> variant/component, to catch normalisation collisions
	for _, v := range ps.Spec.Variants {
		if v.Name == "" {
			r.add(SeverityError, "variant-name", l.File, "PackageSource %s has a variant with empty name", ps.Name)
		}
		if seenVariant[v.Name] {
			r.add(SeverityError, "variant-dup", l.File, "PackageSource %s has duplicate variant %q", ps.Name, v.Name)
		}
		seenVariant[v.Name] = true

		libNames := map[string]bool{}
		for _, lib := range v.Libraries {
			if lib.Path == "" {
				r.add(SeverityError, "lib-path", l.File, "PackageSource %s variant %s has a library with empty path", ps.Name, v.Name)
				continue
			}
			name := lib.Name
			if name == "" {
				name = filepath.Base(lib.Path)
			}
			libNames[name] = true
			dir := filepath.Join(artRoot, lib.Path)
			if !isDir(dir) {
				r.add(SeverityError, "lib-missing", l.File, "PackageSource %s variant %s library path %q not found on disk", ps.Name, v.Name, lib.Path)
			}
		}

		seenComp := map[string]bool{}
		for _, c := range v.Components {
			if c.Name == "" {
				r.add(SeverityError, "comp-name", l.File, "PackageSource %s variant %s has a component with empty name", ps.Name, v.Name)
			}
			if seenComp[c.Name] {
				r.add(SeverityError, "comp-dup", l.File, "PackageSource %s variant %s has duplicate component %q", ps.Name, v.Name, c.Name)
			}
			seenComp[c.Name] = true

			// Dot-to-dash normalisation in the artifact name can make two
			// distinct (variant, component) pairs collide (e.g. "v1.0"/"c" and
			// "v1"/"0-c"); the reconciler would then overwrite one OutputArtifact
			// with the other. Reject the collision at validation time.
			art := artifactName(ps.Name, v.Name, c.Name)
			if prev, ok := seenArtifacts[art]; ok {
				r.add(SeverityError, "artifact-name-collision", l.File, "PackageSource %s: %s and %s both normalise to artifact name %q", ps.Name, prev, v.Name+"/"+c.Name, art)
			} else {
				seenArtifacts[art] = v.Name + "/" + c.Name
			}

			for _, ln := range c.Libraries {
				if !libNames[ln] {
					r.add(SeverityError, "comp-lib-undeclared", l.File, "PackageSource %s variant %s component %q references library %q not declared at variant level", ps.Name, v.Name, c.Name, ln)
				}
			}

			if c.Install != nil && c.Install.Privileged {
				r.add(SeverityWarning, "privileged", l.File, "PackageSource %s variant %s component %q is privileged (install.privileged: true)", ps.Name, v.Name, c.Name)
			}

			if c.Path == "" {
				r.add(SeverityError, "comp-path", l.File, "PackageSource %s variant %s component %q has empty path", ps.Name, v.Name, c.Name)
				continue
			}
			dir := filepath.Join(artRoot, c.Path)
			if !isDir(dir) {
				r.add(SeverityError, "comp-missing", l.File, "PackageSource %s variant %s component %q path %q not found on disk", ps.Name, v.Name, c.Name, c.Path)
				continue
			}
			if !isFile(filepath.Join(dir, "Chart.yaml")) {
				r.add(SeverityError, "chart-yaml", l.File, "PackageSource %s variant %s component %q directory %q has no Chart.yaml", ps.Name, v.Name, c.Name, c.Path)
				continue
			}
			if opts.RunHelmLint {
				helmLint(artRoot, v, c, dir, r, l.File)
			}
		}
	}
}

func checkDependsOn(pss []loadedPackageSource, psNames map[string]bool, r *Report, opts ValidateOptions) {
	known := map[string]bool{}
	for _, k := range opts.KnownSources {
		known[k] = true
	}
	for _, l := range pss {
		for _, v := range l.PS.Spec.Variants {
			for _, dep := range v.DependsOn {
				if psNames[dep] || known[dep] {
					continue
				}
				if strings.HasPrefix(dep, "cozystack.") {
					r.add(SeverityInfo, "dependson-shipped", l.File, "PackageSource %s variant %s dependsOn %q assumed platform-shipped (not defined in this repository)", l.PS.Name, v.Name, dep)
					continue
				}
				r.add(SeverityWarning, "dependson-unresolved", l.File, "PackageSource %s variant %s dependsOn %q is neither defined in this repository nor a known platform source", l.PS.Name, v.Name, dep)
			}
		}
	}
}

func checkAppDefs(ads []loadedAppDef, expected map[string]bool, r *Report) {
	for _, l := range ads {
		ref := l.AD.Spec.Release.ChartRef
		if ref == nil {
			r.add(SeverityError, "appdef-chartref", l.File, "ApplicationDefinition %s has no release.chartRef", l.AD.Name)
			continue
		}
		if ref.Kind != "ExternalArtifact" {
			continue
		}
		if !expected[ref.Name] {
			r.add(SeverityWarning, "appdef-dangling", l.File, "ApplicationDefinition %s chartRef %q does not match any <packagesource>-<variant>-<component> defined in this repository", l.AD.Name, ref.Name)
		}
	}
}

// helmLint stages a component chart (with its declared variant libraries) into
// a temporary directory and runs `helm lint` on it. Staging mirrors the
// ArtifactGenerator copy operations so library-dependent charts lint the same
// way they are assembled at reconcile time.
func helmLint(artRoot string, v cozyv1alpha1.Variant, c cozyv1alpha1.Component, chartDir string, r *Report, file string) {
	stageDir := chartDir
	if len(c.Libraries) > 0 {
		tmp, err := os.MkdirTemp("", "cozypkg-lint-")
		if err != nil {
			r.add(SeverityError, "helm-lint", file, "component %q: failed to create temp dir for library staging: %v", c.Name, err)
			return
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dst := filepath.Join(tmp, filepath.Base(chartDir))
		if err := copyTree(chartDir, dst); err != nil {
			r.add(SeverityError, "helm-lint", file, "component %q: failed to stage chart: %v", c.Name, err)
			return
		}
		libByName := map[string]cozyv1alpha1.Library{}
		for _, lib := range v.Libraries {
			name := lib.Name
			if name == "" {
				name = filepath.Base(lib.Path)
			}
			libByName[name] = lib
		}
		for _, ln := range c.Libraries {
			lib, ok := libByName[ln]
			if !ok {
				continue
			}
			libSrc := filepath.Join(artRoot, lib.Path)
			if err := copyTree(libSrc, filepath.Join(dst, "charts", ln)); err != nil {
				r.add(SeverityError, "helm-lint", file, "component %q: failed to stage library %q: %v", c.Name, ln, err)
				return
			}
		}
		stageDir = dst
	}

	out, err := exec.Command("helm", "lint", stageDir).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		r.add(SeverityError, "helm-lint", file, "helm lint failed for component %q:\n%s", c.Name, trimmed)
		return
	}
	if strings.Contains(trimmed, "[WARNING]") {
		r.add(SeverityWarning, "helm-lint", file, "helm lint reported warnings for component %q:\n%s", c.Name, trimmed)
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

var ociRefPattern = regexp.MustCompile(`^oci://`)

func isOCIRef(target string) bool {
	return ociRefPattern.MatchString(target)
}

// pullOCIArtifact downloads an OCI artifact into a temporary directory using
// the flux CLI and returns the directory plus a cleanup function.
func pullOCIArtifact(ref string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "cozypkg-validate-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	ctx, cancel := context.WithTimeout(context.Background(), ociPullTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "flux", "pull", "artifact", ref, "--output", dir).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		cleanup()
		return "", func() {}, fmt.Errorf("timed out after %s pulling artifact %q", ociPullTimeout, ref)
	}
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to pull artifact %q: %v\n%s", ref, err, strings.TrimSpace(string(out)))
	}
	return dir, cleanup, nil
}

func printReport(r *Report, w io.Writer) {
	for _, f := range r.Findings {
		loc := f.Location
		if loc == "" {
			loc = "-"
		}
		fmt.Fprintf(w, "[%s] %s (%s): %s\n", f.Severity, loc, f.Code, f.Message)
	}
	errs, warns, infos := r.Counts()
	fmt.Fprintf(w, "\nSummary: %d error(s), %d warning(s), %d info\n", errs, warns, infos)
}

var validateCmdFlags struct {
	helmLint           bool
	requireSignature   bool
	certIdentity       string
	certIssuer         string
	knownSources       []string
	allowReservedNames bool
}

var digestRe = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// verifyCosignSignature verifies an OCI artifact's keyless cosign signature
// against the expected certificate identity and OIDC issuer, shelling out to
// the cosign binary. This is the trust anchor the index CI gate relies on: a
// version bump must stay signed by the entry's recorded identity. It returns
// the verified manifest digest so the caller can pull that exact content and
// close the tag-mutation window between verify and pull; the digest is empty
// if it could not be parsed (the caller then falls back to the tag).
func verifyCosignSignature(ref, identity, issuer string) (string, error) {
	if _, err := exec.LookPath("cosign"); err != nil {
		return "", fmt.Errorf("--require-signature needs the cosign binary in PATH: %w", err)
	}
	if identity == "" || issuer == "" {
		return "", fmt.Errorf("--require-signature needs --certificate-identity and --certificate-oidc-issuer for keyless verification")
	}
	cosignRef := strings.TrimPrefix(ref, "oci://")
	ctx, cancel := context.WithTimeout(context.Background(), ociPullTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "cosign", "verify", cosignRef,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", issuer,
		"--output", "json").CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timed out verifying the signature of %q", ref)
	}
	if err != nil {
		return "", fmt.Errorf("cosign verification failed for %q: %v\n%s", ref, err, strings.TrimSpace(string(out)))
	}
	// Extract the verified manifest digest from the JSON payload
	// (.critical.image."docker-manifest-digest").
	return parseVerifiedDigest(out), nil
}

// parseVerifiedDigest pulls the docker-manifest-digest out of cosign's JSON
// verification output, or returns "" if it is not present.
func parseVerifiedDigest(out []byte) string {
	var payloads []struct {
		Critical struct {
			Image struct {
				Digest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	if err := json.Unmarshal(out, &payloads); err == nil {
		for _, p := range payloads {
			if digestRe.MatchString(p.Critical.Image.Digest) {
				return p.Critical.Image.Digest
			}
		}
	}
	// Fallback: any sha256 digest present in the output.
	return digestRe.FindString(string(out))
}

var validateCmd = &cobra.Command{
	Use:   "validate <repository-path-or-oci-ref>",
	Short: "Validate an External-Apps repository offline",
	Long: `Validate lints an External-Apps repository the same way publication
would, without installing anything into a cluster.

The argument is either a local path to a repository checkout or an oci://
reference to a published artifact (pulled with the flux CLI). Validation
decodes every PackageSource and ApplicationDefinition, resolves each
component and library path to a chart directory on disk, checks that
ApplicationDefinition chart references match a component, resolves dependsOn
entries, and flags privileged components. With --helm-lint it additionally
runs "helm lint" on every component chart.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		root := target

		if isOCIRef(target) {
			pullRef := target
			if validateCmdFlags.requireSignature {
				digest, err := verifyCosignSignature(target, validateCmdFlags.certIdentity, validateCmdFlags.certIssuer)
				if err != nil {
					return err
				}
				// Pull the exact content cosign verified, closing the window in
				// which the tag could be moved between verify and pull.
				if digest != "" {
					if parsed, perr := parseOCIRef(target); perr == nil {
						pullRef = parsed.URL + "@" + digest
					}
				}
			}
			dir, cleanup, err := pullOCIArtifact(pullRef)
			if err != nil {
				return err
			}
			defer cleanup()
			root = dir
		} else if validateCmdFlags.requireSignature {
			return fmt.Errorf("--require-signature requires an oci:// reference, not a local path")
		}

		rep, err := ValidateRepo(ValidateOptions{
			RepoRoot:           root,
			RunHelmLint:        validateCmdFlags.helmLint,
			KnownSources:       validateCmdFlags.knownSources,
			AllowReservedNames: validateCmdFlags.allowReservedNames,
		})
		if err != nil {
			return err
		}
		printReport(rep, cmd.OutOrStdout())
		if rep.HasErrors() {
			errs, _, _ := rep.Counts()
			return fmt.Errorf("validation failed: %d error(s)", errs)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
	validateCmd.Flags().BoolVar(&validateCmdFlags.helmLint, "helm-lint", false, "Run 'helm lint' on every component chart (requires the helm binary)")
	validateCmd.Flags().BoolVar(&validateCmdFlags.requireSignature, "require-signature", false, "Require a valid keyless cosign signature on the OCI artifact (needs the cosign binary and an oci:// reference)")
	validateCmd.Flags().StringVar(&validateCmdFlags.certIdentity, "certificate-identity", "", "Expected cosign certificate identity for --require-signature")
	validateCmd.Flags().StringVar(&validateCmdFlags.certIssuer, "certificate-oidc-issuer", "", "Expected cosign certificate OIDC issuer for --require-signature")
	validateCmd.Flags().StringArrayVar(&validateCmdFlags.knownSources, "known-source", nil, "PackageSource name that dependsOn entries may reference without being defined in the repository (can be repeated)")
	validateCmd.Flags().BoolVar(&validateCmdFlags.allowReservedNames, "allow-reserved-names", false, "Permit reserved PackageSource name prefixes (cozystack./community.); the index gate never sets this")
}
