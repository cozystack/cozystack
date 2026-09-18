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

package operator

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/collision"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

const (
	tapFieldOwner  = "cozystack-tap-materializer"
	tapWaitRequeue = 20 * time.Second
	tapFetchLimit  = maxArtifactBytes
)

// TapMaterializerReconciler watches community-tap OCIRepositories and
// materializes the PackageSource(s) their artifact carries, so a tap connected
// from the dashboard becomes installable without the API pulling the artifact.
type TapMaterializerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder surfaces materialization failures (e.g. a name collision with a
	// core component) as Events on the tap source. Optional; nil disables Events.
	Recorder record.EventRecorder
	// Fetch downloads a Flux artifact tarball. Defaults to HTTP; overridable in tests.
	Fetch func(ctx context.Context, url string) ([]byte, error)
}

func (r *TapMaterializerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var repo sourcev1.OCIRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if repo.Labels[tapconst.Label] != "true" {
		return ctrl.Result{}, nil
	}

	// Deletion: clean up materialized PackageSources, then release the finalizer.
	if !repo.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&repo, tapconst.Finalizer) {
			if err := r.deleteMaterialized(ctx, repo.Name); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&repo, tapconst.Finalizer)
			if err := r.Update(ctx, &repo); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&repo, tapconst.Finalizer) {
		controllerutil.AddFinalizer(&repo, tapconst.Finalizer)
		if err := r.Update(ctx, &repo); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	art := repo.Status.Artifact
	if art == nil || art.URL == "" || art.Digest == "" {
		// Source not pulled yet (or digest not yet reported); wait for
		// source-controller. Requiring the digest keeps materialization
		// fail-closed on the integrity check.
		return ctrl.Result{RequeueAfter: tapWaitRequeue}, nil
	}
	if repo.Annotations[tapconst.MaterializedRevisionAnnotation] == art.Revision {
		return ctrl.Result{}, nil
	}

	fetch := r.Fetch
	if fetch == nil {
		fetch = httpFetch
	}
	// The artifact URL is a cluster-DNS name (flux.<ns>.svc/...). cozystack-operator
	// runs with hostNetwork, so it resolves against the host's DNS, not CoreDNS,
	// and that name does not resolve. Rewrite the host to the Service ClusterIP,
	// which a hostNetwork pod can reach directly; on any failure fall back to the
	// original URL so clusters where DNS does work are unaffected.
	data, err := fetch(ctx, r.resolveArtifactURL(ctx, art.URL))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetch artifact for tap %s: %w", repo.Name, err)
	}

	tmp, err := os.MkdirTemp("", "tap-materialize-")
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := verifyAndExtract(data, art.Digest, tmp); err != nil {
		return ctrl.Result{}, fmt.Errorf("extract artifact for tap %s: %w", repo.Name, err)
	}
	sources, err := parsePackageSourcesFromTree(tmp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parse artifact for tap %s: %w", repo.Name, err)
	}

	// A tapped repository keeps its own declared name. Refuse the whole
	// materialization if any declared name clashes with a core component (or
	// another tap), surfacing the reason on the source instead of silently
	// overwriting an official PackageSource via ForceOwnership.
	for i := range sources {
		if err := collision.PackageSourceName(ctx, r.Client, sources[i].GetName(), repo.Name); err != nil {
			return r.failMaterialize(ctx, &repo, err)
		}
	}

	applied := make(map[string]bool, len(sources))
	for i := range sources {
		ps := sources[i].DeepCopy()
		origPath := "/"
		if ps.Spec.SourceRef != nil && ps.Spec.SourceRef.Path != "" {
			origPath = ps.Spec.SourceRef.Path
		}
		rewriteForMaterialize(ps, repo.Name, repo.Namespace, origPath)
		if ps.Labels == nil {
			ps.Labels = map[string]string{}
		}
		ps.Labels[tapconst.Label] = "true"
		if ps.Annotations == nil {
			ps.Annotations = map[string]string{}
		}
		ps.Annotations[tapconst.SourceAnnotation] = repo.Name
		if err := r.Patch(ctx, ps, client.Apply, client.FieldOwner(tapFieldOwner), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("materialize PackageSource %s: %w", ps.GetName(), err)
		}
		// Register the repository's apps on connect (not on a later `cozypkg
		// add`): create a tap-managed Package so the install-marked components
		// (the ApplicationDefinition registrations) deploy and the apps appear in
		// the catalog, mirroring how the platform ships a Package per built-in
		// app. The app components themselves carry no install block and are not
		// deployed by the Package; they are instantiated per user resource.
		if err := r.reconcileRegistrationPackage(ctx, ps, &repo); err != nil {
			return ctrl.Result{}, fmt.Errorf("register apps for %s: %w", ps.GetName(), err)
		}
		applied[ps.GetName()] = true
		logger.Info("materialized PackageSource from tap", "name", ps.GetName(), "tap", repo.Name)
	}
	if len(sources) == 0 {
		logger.Info("tap artifact carried no PackageSource", "tap", repo.Name, "revision", art.Revision)
	}
	// Materialization succeeded: clear any collision error from a prior revision.
	delete(repo.Annotations, tapconst.MaterializeErrorAnnotation)

	// Prune PackageSources this tap materialized from an earlier revision that
	// the current artifact no longer contains (including a rename when the
	// single/multi package count flips), so a removed package leaves the
	// catalog. Installed Packages are left in place.
	if err := r.pruneMaterialized(ctx, repo.Name, applied); err != nil {
		return ctrl.Result{}, err
	}

	// Stamp the revision so an unchanged artifact is not re-pulled every resync.
	if repo.Annotations == nil {
		repo.Annotations = map[string]string{}
	}
	repo.Annotations[tapconst.MaterializedRevisionAnnotation] = art.Revision
	if err := r.Update(ctx, &repo); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// failMaterialize records a materialization failure (e.g. a name collision with
// a core component) on the tap source so it surfaces on the Tap resource and the
// dashboard, and emits a warning Event. It deliberately does not stamp the
// materialized revision, so a corrected artifact (which carries a new revision)
// is retried; a collision does not otherwise self-resolve, so it returns without
// requeueing to avoid a hot loop.
func (r *TapMaterializerReconciler) failMaterialize(ctx context.Context, repo *sourcev1.OCIRepository, cause error) (ctrl.Result, error) {
	if repo.Annotations == nil {
		repo.Annotations = map[string]string{}
	}
	repo.Annotations[tapconst.MaterializeErrorAnnotation] = cause.Error()
	if r.Recorder != nil {
		r.Recorder.Event(repo, corev1.EventTypeWarning, "MaterializeFailed", cause.Error())
	}
	if err := r.Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("tap materialization blocked", "tap", repo.Name, "reason", cause.Error())
	return ctrl.Result{}, nil
}

// reconcileRegistrationPackage converges the tap-managed registration Package
// for a materialized PackageSource on EVERY reconcile, not only at connect. Its
// install-marked components (the ApplicationDefinition registrations) deploy so
// the apps become browsable in the catalog rather than only after a manual
// `cozypkg add`; the Package is labelled and annotated with its source so untap
// removes exactly the one this tap created.
//
// Auto-registration is confined to a "default" variant with no privileged
// install components: the connect path has none of the confirmation `cozypkg add
// --allow-privileged` enforces, so a privileged (or absent) default variant is
// never auto-installed. This is checked on every reconcile, not just at create
// time: a repository that ships a benign revision and later flips its default
// variant to privileged (or drops it) must not keep an installed, unconfirmed
// privileged workload, so a Package THIS tap previously created is de-registered
// (deleted, which garbage-collects its registration HelmReleases and their
// ApplicationDefinitions). A user's own Package (created by `cozypkg add`, no tap
// label) is never adopted or deleted; its owner is responsible for it.
func (r *TapMaterializerReconciler) reconcileRegistrationPackage(ctx context.Context, ps *cozyv1alpha1.PackageSource, repo *sourcev1.OCIRepository) error {
	name := ps.GetName()
	skip := autoRegisterSkipReason(ps)

	var existing cozyv1alpha1.Package
	getErr := r.Get(ctx, types.NamespacedName{Name: name}, &existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return getErr
	}
	exists := getErr == nil
	ours := exists && collision.Owns(&existing, repo.Name)

	if skip != "" {
		// Must not auto-register. De-register only a Package THIS tap created;
		// leave a user's or another tap's Package alone.
		if ours {
			if err := client.IgnoreNotFound(r.Delete(ctx, &existing)); err != nil {
				return err
			}
			r.warnNotAutoRegistered(repo, ps, "de-registered because "+skip)
			return r.setRegistrationState(ctx, ps, "de-registered: "+skip)
		}
		r.warnNotAutoRegistered(repo, ps, "not auto-registered because "+skip)
		return r.setRegistrationState(ctx, ps, "not auto-registered: "+skip)
	}

	// Should be registered.
	if !exists {
		pkg := &cozyv1alpha1.Package{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      map[string]string{tapconst.Label: "true"},
				Annotations: map[string]string{tapconst.SourceAnnotation: repo.Name},
			},
			// Spec.Variant left empty: the Package reconciler resolves it to "default".
		}
		if err := r.Create(ctx, pkg); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return r.setRegistrationState(ctx, ps, "")
	}
	if ours {
		// Already ours from an earlier reconcile: registration stands.
		return r.setRegistrationState(ctx, ps, "")
	}
	if existing.GetLabels()[tapconst.Label] == "true" {
		// Tap-labelled but a different source owns it: a stale leftover, since
		// collision.PackageSourceName already blocks two live taps sharing a
		// PackageSource name. Surface it durably; do not adopt or hot-loop.
		reason := fmt.Sprintf("a Package named %q is owned by another tap", name)
		r.warnNotAutoRegistered(repo, ps, reason)
		return r.setRegistrationState(ctx, ps, reason)
	}
	// Unmarked: a user's manual `cozypkg add` (or a Package from before this
	// version). Its Package already registers the apps, so the goal is met;
	// leave it as the user's to manage rather than erroring (which would hot-loop
	// the whole tap) or adopting it (which would delete it on untap).
	return r.setRegistrationState(ctx, ps, "")
}

// autoRegisterSkipReason returns why a PackageSource's apps must NOT be
// auto-registered on connect, or "" when they may be. Auto-registration is
// limited to a "default" variant with no privileged install components.
func autoRegisterSkipReason(ps *cozyv1alpha1.PackageSource) string {
	if !hasDefaultVariant(ps) {
		return `it declares no "default" variant; register it manually with 'cozypkg add'`
	}
	if privileged := collision.PrivilegedInstallComponents(ps, "default"); len(privileged) > 0 {
		return fmt.Sprintf("its default variant has privileged install component(s) %v; register it manually with 'cozypkg add --allow-privileged'", privileged)
	}
	return ""
}

// setRegistrationState records (or clears, on reason == "") the durable reason a
// PackageSource's apps are not auto-registered, on the PackageSource itself, so
// the dashboard and operator can read it after the Warning Event has expired. It
// is a no-op when the annotation already holds the wanted value.
func (r *TapMaterializerReconciler) setRegistrationState(ctx context.Context, ps *cozyv1alpha1.PackageSource, reason string) error {
	current := ps.GetAnnotations()[tapconst.RegistrationStateAnnotation]
	if current == reason {
		return nil
	}
	base := ps.DeepCopy()
	ann := ps.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	if reason == "" {
		delete(ann, tapconst.RegistrationStateAnnotation)
	} else {
		ann[tapconst.RegistrationStateAnnotation] = reason
	}
	ps.SetAnnotations(ann)
	return r.Patch(ctx, ps, client.MergeFrom(base))
}

// warnNotAutoRegistered records on the source, as a Warning Event, that a
// materialized PackageSource's apps were not auto-registered (or were
// de-registered), with the reason. The durable copy lives on the PackageSource
// via setRegistrationState; this is the immediate, human-facing signal.
func (r *TapMaterializerReconciler) warnNotAutoRegistered(repo *sourcev1.OCIRepository, ps *cozyv1alpha1.PackageSource, reason string) {
	if r.Recorder != nil {
		r.Recorder.Event(repo, corev1.EventTypeWarning, "AppsNotAutoRegistered",
			fmt.Sprintf("PackageSource %s: %s", ps.GetName(), reason))
	}
}

// hasDefaultVariant reports whether the PackageSource declares a variant named
// "default".
func hasDefaultVariant(ps *cozyv1alpha1.PackageSource) bool {
	for i := range ps.Spec.Variants {
		if ps.Spec.Variants[i].Name == "default" {
			return true
		}
	}
	return false
}

// deleteMaterialized removes every PackageSource materialized from the given
// tap source. Installed Packages are intentionally left in place.
func (r *TapMaterializerReconciler) deleteMaterialized(ctx context.Context, sourceName string) error {
	return r.pruneMaterialized(ctx, sourceName, nil)
}

// pruneMaterialized deletes the PackageSources materialized from sourceName
// except those whose names are in keep (nil keep deletes all of them).
func (r *TapMaterializerReconciler) pruneMaterialized(ctx context.Context, sourceName string, keep map[string]bool) error {
	var list cozyv1alpha1.PackageSourceList
	if err := r.List(ctx, &list, client.MatchingLabels{tapconst.Label: "true"}); err != nil {
		return err
	}
	for i := range list.Items {
		ps := &list.Items[i]
		if ps.Annotations[tapconst.SourceAnnotation] != sourceName || keep[ps.Name] {
			continue
		}
		// Delete the registration Package BEFORE the PackageSource. If the
		// Package delete fails transiently, the PackageSource is still present so
		// the next reconcile lists it and retries; deleting the PackageSource
		// first would leave a stranded Package no later pass can find.
		if err := r.deleteRegistrationPackage(ctx, ps.Name, sourceName); err != nil {
			return err
		}
		if err := r.Delete(ctx, ps); err != nil && client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// deleteRegistrationPackage removes the tap-managed registration Package for a
// PackageSource, but only when it is owned by the given source (label AND source
// annotation), so a Package a later tap created under a reused name is never
// deleted by an earlier tap's teardown.
func (r *TapMaterializerReconciler) deleteRegistrationPackage(ctx context.Context, name, sourceName string) error {
	var pkg cozyv1alpha1.Package
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &pkg); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !collision.Owns(&pkg, sourceName) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, &pkg))
}

// resolveArtifactURL rewrites a cluster-DNS artifact host (e.g.
// flux.cozy-fluxcd.svc) to the backing Service's ClusterIP, so the hostNetwork
// operator pod can reach it without CoreDNS. It fails open: any parse/lookup
// problem (non-cluster host, Service missing, headless/empty ClusterIP) returns
// the original URL unchanged, so this only ever adds a reachable path.
//
// Failing open here is safe ONLY because integrity is enforced downstream:
// verifyAndExtract rejects an absent or mismatched digest (see
// tapmaterializer_artifact.go). Resolving to the wrong host therefore yields a
// digest-mismatch error, never a silent content substitution. If the digest
// handling is ever relaxed, this fail-open must be revisited.
func (r *TapMaterializerReconciler) resolveArtifactURL(ctx context.Context, rawURL string) string {
	logger := log.FromContext(ctx)
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	svc, ns, ok := parseClusterServiceHost(u.Hostname())
	if !ok {
		return rawURL
	}
	var service corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: svc, Namespace: ns}, &service); err != nil {
		logger.V(1).Info("artifact host not resolved to ClusterIP; using DNS name", "host", u.Hostname(), "err", err.Error())
		return rawURL
	}
	ip := service.Spec.ClusterIP
	if ip == "" || ip == corev1.ClusterIPNone {
		return rawURL
	}
	rewritten, err := rewriteURLHost(rawURL, ip)
	if err != nil {
		return rawURL
	}
	return rewritten
}

// parseClusterServiceHost recognises an in-cluster Service DNS name of the form
// <service>.<namespace>.svc or <service>.<namespace>.svc.cluster.local and
// returns the service and namespace.
func parseClusterServiceHost(host string) (service, namespace string, ok bool) {
	host = strings.TrimSuffix(host, ".")
	labels := strings.Split(host, ".")
	// <svc>.<ns>.svc[.cluster.local]
	if len(labels) < 3 || labels[2] != "svc" {
		return "", "", false
	}
	if len(labels) > 3 {
		rest := strings.Join(labels[3:], ".")
		if rest != "cluster.local" {
			return "", "", false
		}
	}
	if labels[0] == "" || labels[1] == "" {
		return "", "", false
	}
	return labels[0], labels[1], true
}

// rewriteURLHost replaces the host of rawURL with newHost, preserving scheme,
// port, path and query.
func rewriteURLHost(rawURL, newHost string) (string, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(newHost, port)
	} else {
		u.Host = newHost
	}
	return u.String(), nil
}

// httpFetch downloads a Flux artifact tarball, bounded in size and time.
func httpFetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s fetching %s", resp.Status, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, tapFetchLimit+1))
}

// SetupWithManager wires the reconciler to community-tap OCIRepositories only.
func (r *TapMaterializerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	isTap := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[tapconst.Label] == "true"
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-tap-materializer").
		For(&sourcev1.OCIRepository{}, builder.WithPredicates(isTap)).
		Complete(r)
}
