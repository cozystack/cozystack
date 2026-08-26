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
	"net/http"
	"os"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
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
	data, err := fetch(ctx, art.URL)
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

	base := repo.Annotations[tapconst.NameAnnotation]
	if base == "" {
		base = communityBaseFromURL(repo.Spec.URL)
	}
	single := len(sources) == 1
	applied := make(map[string]bool, len(sources))
	for i := range sources {
		ps := sources[i].DeepCopy()
		origPath := "/"
		if ps.Spec.SourceRef != nil && ps.Spec.SourceRef.Path != "" {
			origPath = ps.Spec.SourceRef.Path
		}
		rewriteForMaterialize(ps, materializedName(base, ps.GetName(), single), repo.Name, repo.Namespace, origPath)
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
		applied[ps.GetName()] = true
		logger.Info("materialized PackageSource from tap", "name", ps.GetName(), "tap", repo.Name)
	}
	if len(sources) == 0 {
		logger.Info("tap artifact carried no PackageSource", "tap", repo.Name, "revision", art.Revision)
	}

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
		if err := r.Delete(ctx, ps); err != nil && client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
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
