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
		// add`) via a tap-managed Package whose install-marked components (the
		// ApplicationDefinition registrations) deploy so the apps appear in the
		// catalog, mirroring how the platform ships a Package per built-in app.
		// The app components carry no install block and are instantiated per user
		// resource. Privileged install components are refused for a tap-labelled
		// (unconfirmed) Package on several best-effort layers: the Package
		// reconciler skips their HelmRelease and prunes a lingering one at the
		// install site, and the PackageSource reconciler withholds their
		// ExternalArtifact (collision.PrivilegedConfirmed). A benign component
		// flipped to privileged in a new revision can still be materialised by
		// source-watcher before these observe the flip; fully closing that race
		// needs the catalog registration decoupled from install (issue #4359).
		if err := r.ensureRegistration(ctx, ps, &repo); err != nil {
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

// registrationOwnerRef builds the ownerReference from a registration Package to
// its PackageSource, so the Package is garbage-collected if the PackageSource is
// deleted directly.
func registrationOwnerRef(ps *cozyv1alpha1.PackageSource) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: cozyv1alpha1.GroupVersion.String(),
		Kind:       "PackageSource",
		Name:       ps.GetName(),
		UID:        ps.GetUID(),
	}
}

// autoRegisterSkipReason returns why a PackageSource's apps cannot be
// auto-registered on connect, or "" when they can. Only a source with no
// "default" variant is skipped (nothing to register by default). Privileged
// components are NOT a reason to skip: the source is registered and its
// privileged components are refused at install time (collision.PrivilegedConfirmed)
// until the operator confirms with 'cozypkg add --allow-privileged'.
func autoRegisterSkipReason(ps *cozyv1alpha1.PackageSource) string {
	if !hasDefaultVariant(ps) {
		return `it declares no "default" variant; register it manually with 'cozypkg add'`
	}
	return ""
}

// warnNotAutoRegistered records on the source, as a Warning Event, that a
// materialized PackageSource's apps were not auto-registered, with the reason.
// This Event is the immediate, human-facing signal.
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

// deleteMaterialized removes every PackageSource materialized from the given tap
// source, and each one's tap-managed registration Package. A user's own Package
// (no tap label) is left in place.
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
// PackageSource, but only when it is a managed registration of the given source
// (owned AND still at the auto default variant), so a Package a later tap created
// under a reused name, or one a user pinned to a variant, is never deleted by an
// earlier tap's teardown.
func (r *TapMaterializerReconciler) deleteRegistrationPackage(ctx context.Context, name, sourceName string) error {
	// Delete only a Package this source still manages, leaving a user's own or
	// pinned Package (markers shed) in place. Teardown races the handover that
	// `cozypkg add --allow-privileged` performs (an Update that sheds the tap
	// markers): were it to land between the Get and the Delete, a plain
	// owned-delete would remove the just-confirmed user Package. Re-read and
	// re-classify on a conflict, and delete only the exact object classified via a
	// UID + ResourceVersion precondition, the same guard the untap and dashboard
	// delete paths use.
	for attempt := 0; ; attempt++ {
		var pkg cozyv1alpha1.Package
		if err := r.Get(ctx, types.NamespacedName{Name: name}, &pkg); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !collision.ManagedRegistration(&pkg, sourceName, pkg.Spec.Variant) {
			return nil
		}
		err := r.Delete(ctx, &pkg, client.Preconditions{UID: &pkg.UID, ResourceVersion: &pkg.ResourceVersion})
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		if apierrors.IsConflict(err) && attempt < 4 {
			continue
		}
		return err
	}
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

// ensureRegistration creates the tap-managed registration Package for a
// materialized PackageSource so its apps appear in the catalog, unless the source
// has no "default" variant (nothing to register), in which case a Warning Event
// says why. Privileged components are not gated here: the Package reconciler
// refuses them for an unconfirmed tap Package at the install site. The Package is
// created only when absent; an existing registration, or a user's own or pinned
// Package, is left as is.
func (r *TapMaterializerReconciler) ensureRegistration(ctx context.Context, ps *cozyv1alpha1.PackageSource, repo *sourcev1.OCIRepository) error {
	if skip := autoRegisterSkipReason(ps); skip != "" {
		r.warnNotAutoRegistered(repo, ps, "not auto-registered because "+skip)
		return nil
	}
	pkg := &cozyv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ps.GetName(),
			Labels:          map[string]string{tapconst.Label: "true"},
			Annotations:     map[string]string{tapconst.SourceAnnotation: repo.Name},
			OwnerReferences: []metav1.OwnerReference{registrationOwnerRef(ps)},
		},
	}
	if err := r.Create(ctx, pkg); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
