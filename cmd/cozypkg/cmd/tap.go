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
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

const (
	// cozySystemNamespace is where tap creates the Flux source, alongside the
	// platform and External-Apps sources.
	cozySystemNamespace = "cozy-system"
)

var tapCmdFlags struct {
	secret       string
	tag          string
	kubeconfig   string
	index        string
	skipValidate bool
}

// ociRef is the parsed form of an oci:// reference.
type ociRef struct {
	// URL is the reference without any tag/digest, e.g. oci://ghcr.io/org/repo.
	URL string
	Org string
	// Repo is the last path segment.
	Repo string
	Tag  string
}

var sanitizeName = regexp.MustCompile(`[^a-z0-9-]+`)

// parseOCIRef splits an oci:// reference into its URL (without tag), org, repo,
// and tag. A missing tag defaults to "latest".
func parseOCIRef(ref string) (ociRef, error) {
	if !strings.HasPrefix(ref, "oci://") {
		return ociRef{}, fmt.Errorf("reference %q must start with oci://", ref)
	}
	body := strings.TrimPrefix(ref, "oci://")

	tag := ""
	// Digest pins are not tag-addressable; tap is tag-based for now.
	if strings.Contains(body, "@") {
		return ociRef{}, fmt.Errorf("digest references are not supported by tap yet; use a tag (oci://host/org/repo:tag)")
	}
	if colon := strings.LastIndex(body, ":"); colon >= 0 && !strings.Contains(body[colon:], "/") {
		tag = body[colon+1:]
		body = body[:colon]
	}

	segs := strings.Split(strings.Trim(body, "/"), "/")
	if len(segs) < 2 {
		return ociRef{}, fmt.Errorf("reference %q must include at least a host and a repository path", ref)
	}
	repo := segs[len(segs)-1]
	org := ""
	if len(segs) >= 3 {
		org = segs[len(segs)-2]
	}
	if tag == "" {
		tag = "latest"
	}
	return ociRef{URL: "oci://" + body, Org: org, Repo: repo, Tag: tag}, nil
}

// fluxSourceName is the RFC-1123 name of the OCIRepository tap creates.
func fluxSourceName(r ociRef) string {
	base := r.Repo
	if r.Org != "" {
		base = r.Org + "-" + r.Repo
	}
	return "community-" + sanitizeName.ReplaceAllString(strings.ToLower(base), "-")
}

// tapPackageSourceName derives the cluster-scoped PackageSource name. When a
// repository carries a single PackageSource the name is community.<org>.<repo>;
// with several, the original name is appended to keep them distinct.
func tapPackageSourceName(r ociRef, originalName string, single bool) string {
	base := tapconst.Prefix
	if r.Org != "" {
		base += r.Org + "."
	}
	base += r.Repo
	if single {
		return base
	}
	orig := strings.TrimPrefix(originalName, tapconst.Prefix)
	return base + "." + orig
}

// buildTapOCIRepository mirrors the operator's generateOCIRepository: an
// OCIRepository in cozy-system, 5m interval, optional tag reference, optional
// pull-credential secretRef (the private-tap credential, symmetric with #2472).
func buildTapOCIRepository(name string, r ociRef, secret string) *sourcev1.OCIRepository {
	obj := &sourcev1.OCIRepository{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.OCIRepositoryKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cozySystemNamespace,
			// Mark the source as a community tap and record its base name, so
			// the dashboard's orphan-disconnect (deleteOrphanTapSource) and the
			// operator's materializer recognise a CLI-created source too.
			Labels:      map[string]string{tapconst.Label: "true"},
			Annotations: map[string]string{tapconst.NameAnnotation: tapPackageSourceName(r, "", true)},
		},
		Spec: sourcev1.OCIRepositorySpec{
			URL:      r.URL,
			Interval: metav1.Duration{Duration: 5 * time.Minute},
			Reference: &sourcev1.OCIRepositoryRef{
				Tag: r.Tag,
			},
		},
	}
	if secret != "" {
		obj.Spec.SecretRef = &fluxmeta.LocalObjectReference{Name: secret}
	}
	return obj
}

// rewritePackageSourceForTap renames a PackageSource with the community-scoped
// name and repoints its sourceRef at the OCIRepository tap created, preserving
// the variants/components that came from the artifact.
func rewritePackageSourceForTap(ps *cozyv1alpha1.PackageSource, newName, sourceName, sourcePath string) {
	ps.SetName(newName)
	ps.SetResourceVersion("")
	ps.SetUID("")
	if sourcePath == "" {
		sourcePath = "/"
	}
	ps.Spec.SourceRef = &cozyv1alpha1.PackageSourceRef{
		Kind:      sourcev1.OCIRepositoryKind,
		Name:      sourceName,
		Namespace: cozySystemNamespace,
		Path:      sourcePath,
	}
}

func tapScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(sourcev1.AddToScheme(scheme))
	return scheme
}

func newClusterClient(kubeconfig string) (client.Client, error) {
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = ctrl.GetConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return client.New(config, client.Options{Scheme: tapScheme()})
}

var tapCmd = &cobra.Command{
	Use:   "tap <oci-ref>",
	Short: "Register an external External-Apps repository as a PackageSource",
	Long: `Tap registers an external repository published as an OCI artifact: it
creates a Flux OCIRepository pointing at the artifact and the PackageSource(s)
the artifact carries, named under the community. prefix so they cannot shadow
official packages. Nothing is installed until 'cozypkg add'. Tapping is
idempotent. Creating cluster-scoped resources requires cluster-admin.

With --secret the OCIRepository is given a pull-credential secretRef (the
admin pre-creates the Secret in cozy-system), so a private repository taps in
one command.

Trust: tap validates the artifact's structure but does NOT verify its cosign
signature. Signature verification is the community index's job at publication
time ('cozypkg validate --require-signature' in the index CI gate, pinned to
the entry's recorded identity), and can additionally be enforced at pull time
via Flux OCIRepository verification. Tapping a third-party repository runs its
charts in your management cluster: tap only sources you trust.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		target, err := resolveTapTarget(args[0], defaultIndexRef(tapCmdFlags.index))
		if err != nil {
			return err
		}
		ref, err := parseOCIRef(target)
		if err != nil {
			return err
		}
		if tapCmdFlags.tag != "" {
			ref.Tag = tapCmdFlags.tag
		}

		// Pull first: an unreachable/invalid ref must fail before any cluster
		// resource is created (no half-created source).
		fullRef := ref.URL + ":" + ref.Tag
		dir, cleanup, err := pullOCIArtifact(fullRef)
		if err != nil {
			return err
		}
		defer cleanup()

		if !tapCmdFlags.skipValidate {
			rep, err := ValidateRepo(ValidateOptions{RepoRoot: dir})
			if err != nil {
				return err
			}
			if rep.HasErrors() {
				printReport(rep, cmd.OutOrStdout())
				errs, _, _ := rep.Counts()
				return fmt.Errorf("refusing to tap: artifact failed validation with %d error(s) (use --skip-validate to override)", errs)
			}
		}

		report := &Report{}
		sources, _ := loadManifests(dir, report)
		if len(sources) == 0 {
			return fmt.Errorf("artifact at %s carries no PackageSource manifest", fullRef)
		}

		srcName := fluxSourceName(ref)
		ociRepo := buildTapOCIRepository(srcName, ref, tapCmdFlags.secret)

		single := len(sources) == 1
		toApply := make([]client.Object, 0, len(sources)+1)
		toApply = append(toApply, ociRepo)
		names := make([]string, 0, len(sources))
		for i := range sources {
			ps := sources[i].PS.DeepCopy()
			newName := tapPackageSourceName(ref, ps.GetName(), single)
			origPath := ""
			if ps.Spec.SourceRef != nil {
				origPath = ps.Spec.SourceRef.Path
			}
			rewritePackageSourceForTap(ps, newName, srcName, origPath)
			toApply = append(toApply, ps)
			names = append(names, newName)
		}

		k8sClient, err := newClusterClient(tapCmdFlags.kubeconfig)
		if err != nil {
			return err
		}

		// Refuse to silently retarget an existing tap: two repositories that
		// share an org/repo path on different hosts derive the same source name,
		// and a force-apply would repoint the first at the second.
		existing := &sourcev1.OCIRepository{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: srcName, Namespace: cozySystemNamespace}, existing); err == nil {
			if existing.Spec.URL != "" && existing.Spec.URL != ref.URL {
				return fmt.Errorf("a different repository (%s) is already tapped as %q; run 'cozypkg untap %s' before tapping %s", existing.Spec.URL, srcName, names[0], ref.URL)
			}
		}

		// Only roll back objects this invocation actually created; a re-tap is
		// idempotent and must not delete resources that pre-existed it.
		created := make([]client.Object, 0, len(toApply))
		patchOptions := []client.PatchOption{client.FieldOwner("cozypkg"), client.ForceOwnership}
		for _, obj := range toApply {
			preExisted := objectExists(ctx, k8sClient, obj)
			if err := k8sClient.Patch(ctx, obj, client.Apply, patchOptions...); err != nil {
				for _, done := range created {
					_ = k8sClient.Delete(ctx, done)
				}
				return fmt.Errorf("failed to apply %T %s: %w", obj, obj.GetName(), err)
			}
			if !preExisted {
				created = append(created, obj)
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Tapped %s\n  OCIRepository/%s (%s:%s)\n", fullRef, srcName, ref.URL, ref.Tag)
		for _, n := range names {
			fmt.Fprintf(cmd.OutOrStdout(), "  PackageSource/%s\n", n)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Install with: cozypkg add %s\n", names[0])
		return nil
	},
}

var untapCmd = &cobra.Command{
	Use:   "untap <packagesource-name>",
	Short: "Remove a tapped repository",
	Long: `Untap removes a community-tapped PackageSource and its Flux source.
Already-installed Packages are left untouched (delete them explicitly with
cozypkg del). Only community.* sources can be untapped; official sources are
refused.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		name := args[0]
		if !strings.HasPrefix(name, tapconst.Prefix) {
			return fmt.Errorf("refusing to untap %q: only community.* sources can be untapped", name)
		}

		k8sClient, err := newClusterClient(tapCmdFlags.kubeconfig)
		if err != nil {
			return err
		}

		ps := &cozyv1alpha1.PackageSource{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, ps); err != nil {
			return fmt.Errorf("failed to get PackageSource %s: %w", name, err)
		}

		// Warn if a Package by this name is installed (add.go names the Package
		// after its PackageSource).
		pkg := &cozyv1alpha1.Package{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, pkg); err == nil && !untapConfirmFlag {
			return fmt.Errorf("package %s is still installed from this source; delete it with 'cozypkg del %s' first, or pass --yes to untap anyway (the Package stays installed)", name, name)
		}

		srcName := ""
		if ps.Spec.SourceRef != nil {
			srcName = ps.Spec.SourceRef.Name
		}

		if err := k8sClient.Delete(ctx, ps); err != nil {
			return fmt.Errorf("failed to delete PackageSource %s: %w", name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed PackageSource/%s\n", name)

		// Delete the Flux source only if no other PackageSource references it.
		if srcName != "" && strings.HasPrefix(srcName, "community-") {
			if !sourceStillReferenced(ctx, k8sClient, srcName, name) {
				oci := &sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{Name: srcName, Namespace: cozySystemNamespace}}
				if err := k8sClient.Delete(ctx, oci); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not delete OCIRepository/%s: %v\n", srcName, err)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Removed OCIRepository/%s\n", srcName)
				}
			}
		}
		return nil
	},
}

// objectExists reports whether obj already exists on the cluster, so an
// idempotent re-tap does not roll back resources it did not create. It fails
// safe: only a definite NotFound means "this run created it" (eligible for
// rollback). Any other error (RBAC, throttling, timeout) is treated as
// possibly-pre-existing so rollback never deletes an object it did not create.
func objectExists(ctx context.Context, k8sClient client.Client, obj client.Object) bool {
	probe, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return true
	}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), probe)
	return !apierrors.IsNotFound(err)
}

// sourceStillReferenced reports whether any PackageSource other than excludeName
// still points at the given Flux source.
func sourceStillReferenced(ctx context.Context, k8sClient client.Client, srcName, excludeName string) bool {
	var list cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &list); err != nil {
		// On a list error, keep the source (safer than deleting a shared one).
		return true
	}
	for i := range list.Items {
		if list.Items[i].Name == excludeName {
			continue
		}
		if ref := list.Items[i].Spec.SourceRef; ref != nil && ref.Name == srcName {
			return true
		}
	}
	return false
}

var untapConfirmFlag bool

func init() {
	rootCmd.AddCommand(tapCmd)
	rootCmd.AddCommand(untapCmd)
	tapCmd.Flags().StringVar(&tapCmdFlags.secret, "secret", "", "Name of a pull-credential Secret in cozy-system for a private repository")
	tapCmd.Flags().StringVar(&tapCmdFlags.tag, "tag", "", "OCI tag to tap (overrides a tag in the reference; defaults to latest)")
	tapCmd.Flags().StringVar(&tapCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	tapCmd.Flags().StringVar(&tapCmdFlags.index, "index", "", "Index location for resolving a short name (local dir or oci:// ref; defaults to COZYPKG_INDEX)")
	tapCmd.Flags().BoolVar(&tapCmdFlags.skipValidate, "skip-validate", false, "Skip validating the artifact before tapping")
	untapCmd.Flags().StringVar(&tapCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	untapCmd.Flags().BoolVar(&untapConfirmFlag, "yes", false, "Untap even if a Package from this source is still installed")
}
