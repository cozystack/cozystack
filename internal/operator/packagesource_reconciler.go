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
	"strconv"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Constants tuning the workaround for fluxcd/pkg#934 (patch.Helper split-write
// race in source-watcher). See the block comment on maybeRequeueArtifactGenerator.
const (
	stuckGracePeriod   = 30 * time.Second
	initialBackoff     = 30 * time.Second
	maxBackoff         = 4 * time.Minute
	maxRequeueAttempts = 5

	annotationFluxRequestedAt = "reconcile.fluxcd.io/requestedAt"
	annotationRequeueAttempts = "cozystack.io/source-watcher-requeue-attempts"
	annotationLastRequeueAt   = "cozystack.io/source-watcher-last-requeue-at"

	reasonAwaitingRequeue  = "AwaitingSourceWatcherRequeue"
	reasonSourceWatcherBad = "SourceWatcherStalled"
)

// nowFunc is overridable in tests; production code always uses time.Now.
var nowFunc = time.Now

// PackageSourceReconciler reconciles PackageSource resources
type PackageSourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *PackageSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	packageSource := &cozyv1alpha1.PackageSource{}
	if err := r.Get(ctx, req.NamespacedName, packageSource); err != nil {
		if apierrors.IsNotFound(err) {
			// Resource not found, return (ownerReference will handle cleanup)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Generate ArtifactGenerator for package source
	if err := r.reconcileArtifactGenerators(ctx, packageSource); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerator")
		return ctrl.Result{}, err
	}

	// Update PackageSource status (variants and conditions from ArtifactGenerator).
	// The status update may schedule a follow-up reconcile via RequeueAfter when
	// it detects a source-watcher status-patch stall and needs to wait for the
	// next backoff window; that Result is honoured on the way out.
	result, err := r.updateStatus(ctx, packageSource)
	if err != nil {
		logger.Error(err, "failed to update status")
		// Don't return error, status update is not critical
	}

	return result, nil
}

// reconcileArtifactGenerators generates a single ArtifactGenerator for the package source
// Creates one ArtifactGenerator per package source with all OutputArtifacts from components
func (r *PackageSourceReconciler) reconcileArtifactGenerators(ctx context.Context, packageSource *cozyv1alpha1.PackageSource) error {
	logger := log.FromContext(ctx)

	// Check if SourceRef is set
	if packageSource.Spec.SourceRef == nil {
		logger.Info("skipping ArtifactGenerator creation, SourceRef not set", "packageSource", packageSource.Name)
		return nil
	}

	// Namespace is always cozy-system
	namespace := "cozy-system"
	// ArtifactGenerator name is the package source name
	agName := packageSource.Name

	// Collect all OutputArtifacts
	outputArtifacts := []sourcewatcherv1beta1.OutputArtifact{}

	// Process all variants and their components
	for _, variant := range packageSource.Spec.Variants {
		// Build library map for this variant
		// Map key is the library name (from lib.Name or extracted from path)
		// This allows components in this variant to reference libraries by name
		// Libraries are scoped per variant to avoid conflicts between variants
		libraryMap := make(map[string]cozyv1alpha1.Library)
		for _, lib := range variant.Libraries {
			libName := lib.Name
			if libName == "" {
				// If library name is not set, extract from path
				libName = r.getPackageNameFromPath(lib.Path)
			}
			if libName != "" {
				// Store library with the resolved name
				libraryMap[libName] = lib
			}
		}

		for _, component := range variant.Components {
			// Skip components without path
			if component.Path == "" {
				logger.V(1).Info("skipping component without path", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name)
				continue
			}

			logger.V(1).Info("processing component", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "path", component.Path)

			// Extract component name from path (last component)
			componentPathName := r.getPackageNameFromPath(component.Path)
			if componentPathName == "" {
				logger.Info("skipping component with invalid path", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "path", component.Path)
				continue
			}

			// Get basePath with default values
			basePath := r.getBasePath(packageSource)

			// Build copy operations
			copyOps := []sourcewatcherv1beta1.CopyOperation{
				{
					From: r.buildSourcePath(packageSource.Spec.SourceRef.Name, basePath, component.Path),
					To:   fmt.Sprintf("@artifact/%s/", componentPathName),
				},
			}

			// Add libraries if specified
			for _, libName := range component.Libraries {
				if lib, ok := libraryMap[libName]; ok {
					copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
						From: r.buildSourcePath(packageSource.Spec.SourceRef.Name, basePath, lib.Path),
						To:   fmt.Sprintf("@artifact/%s/charts/%s/", componentPathName, libName),
					})
				}
			}

			// Add valuesFiles if specified
			for i, valuesFile := range component.ValuesFiles {
				strategy := "Merge"
				if i == 0 {
					strategy = "Overwrite"
				}
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From:     r.buildSourceFilePath(packageSource.Spec.SourceRef.Name, basePath, fmt.Sprintf("%s/%s", component.Path, valuesFile)),
					To:       fmt.Sprintf("@artifact/%s/values.yaml", componentPathName),
					Strategy: strategy,
				})
			}

			// Artifact name: <packagesource>-<variant>-<componentname>
			// Replace dots with dashes to comply with Kubernetes naming requirements
			artifactName := fmt.Sprintf("%s-%s-%s",
				strings.ReplaceAll(packageSource.Name, ".", "-"),
				strings.ReplaceAll(variant.Name, ".", "-"),
				strings.ReplaceAll(component.Name, ".", "-"))

			outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
				Name: artifactName,
				Copy: copyOps,
			})

			logger.Info("added OutputArtifact for component", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "artifactName", artifactName)
		}
	}

	// If there are no OutputArtifacts, return (ownerReference will handle cleanup if needed)
	if len(outputArtifacts) == 0 {
		logger.Info("no OutputArtifacts to generate, skipping ArtifactGenerator creation", "packageSource", packageSource.Name)
		return nil
	}

	// Build labels
	labels := make(map[string]string)
	labels["cozystack.io/packagesource"] = packageSource.Name

	// Create single ArtifactGenerator for the package source
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
			Sources: []sourcewatcherv1beta1.SourceReference{
				{
					Alias:     packageSource.Spec.SourceRef.Name,
					Kind:      packageSource.Spec.SourceRef.Kind,
					Name:      packageSource.Spec.SourceRef.Name,
					Namespace: packageSource.Spec.SourceRef.Namespace,
				},
			},
			OutputArtifacts: outputArtifacts,
		},
	}

	// Set ownerReference
	gvk, err := apiutil.GVKForObject(packageSource, r.Scheme)
	if err != nil {
		return fmt.Errorf("failed to get GVK for PackageSource: %w", err)
	}
	ag.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Name:       packageSource.Name,
			UID:        packageSource.UID,
			Controller: func() *bool { b := true; return &b }(),
		},
	}

	logger.Info("creating ArtifactGenerator for package source", "packageSource", packageSource.Name, "agName", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	if err := r.createOrUpdate(ctx, ag); err != nil {
		return fmt.Errorf("failed to reconcile ArtifactGenerator %s: %w", agName, err)
	}

	logger.Info("reconciled ArtifactGenerator for package source", "name", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	return nil
}

// Helper functions
func (r *PackageSourceReconciler) getPackageNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// getBasePath returns the basePath with default values based on source kind
func (r *PackageSourceReconciler) getBasePath(packageSource *cozyv1alpha1.PackageSource) string {
	// If path is explicitly set in SourceRef, use it (but normalize "/" to empty)
	if packageSource.Spec.SourceRef.Path != "" {
		path := strings.Trim(packageSource.Spec.SourceRef.Path, "/")
		// If path is "/" or empty after trim, return empty string
		if path == "" {
			return ""
		}
		return path
	}
	// Default values based on kind
	if packageSource.Spec.SourceRef.Kind == "OCIRepository" {
		return "" // Root for OCI
	}
	// Default for GitRepository
	return "packages"
}

// buildSourcePath builds the full source path using basePath with glob pattern
func (r *PackageSourceReconciler) buildSourcePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		trimmed := strings.Trim(basePath, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if path != "" {
		trimmed := strings.Trim(path, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s/**", sourceName)
	}
	return fmt.Sprintf("@%s/%s/**", sourceName, fullPath)
}

// buildSourceFilePath builds the full source path for a specific file (without glob pattern)
func (r *PackageSourceReconciler) buildSourceFilePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		trimmed := strings.Trim(basePath, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if path != "" {
		trimmed := strings.Trim(path, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s", sourceName)
	}
	return fmt.Sprintf("@%s/%s", sourceName, fullPath)
}

// createOrUpdate creates or updates a resource using server-side apply
func (r *PackageSourceReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
	// Ensure TypeMeta is set for server-side apply
	// Use type assertion to set GVK if the object supports it
	if runtimeObj, ok := obj.(runtime.Object); ok {
		gvk, err := apiutil.GVKForObject(obj, r.Scheme)
		if err != nil {
			return fmt.Errorf("failed to get GVK for object: %w", err)
		}
		runtimeObj.GetObjectKind().SetGroupVersionKind(gvk)
	}

	// Use server-side apply with field manager
	// This is atomic and avoids race conditions from Get/Create/Update pattern
	// Labels, annotations, and spec will be merged automatically by the server
	// Each field is treated separately, so existing ones are preserved
	return r.Patch(ctx, obj, client.Apply, client.FieldOwner("cozystack-packagesource-controller"))
}

// updateStatus updates PackageSource status (variants and conditions from
// ArtifactGenerator). It may return a Result with RequeueAfter set when the
// ArtifactGenerator's upstream Ready condition is stuck and the reconciler is
// driving source-watcher through a bounded requeue schedule; see
// maybeRequeueArtifactGenerator for the strategy.
func (r *PackageSourceReconciler) updateStatus(ctx context.Context, packageSource *cozyv1alpha1.PackageSource) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Update variants in status from spec
	variantNames := make([]string, 0, len(packageSource.Spec.Variants))
	for _, variant := range packageSource.Spec.Variants {
		variantNames = append(variantNames, variant.Name)
	}
	packageSource.Status.Variants = strings.Join(variantNames, ",")

	// Check if SourceRef is set
	if packageSource.Spec.SourceRef == nil {
		// Set status to unknown if SourceRef is not set
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionUnknown,
			Reason:  "SourceRefNotSet",
			Message: "SourceRef is not configured",
		})
		return ctrl.Result{}, r.Status().Update(ctx, packageSource)
	}

	// Get ArtifactGenerator
	ag := &sourcewatcherv1beta1.ArtifactGenerator{}
	agKey := types.NamespacedName{
		Name:      packageSource.Name,
		Namespace: "cozy-system",
	}

	if err := r.Get(ctx, agKey, ag); err != nil {
		if apierrors.IsNotFound(err) {
			// ArtifactGenerator not found, set status to unknown
			meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionUnknown,
				Reason:  "ArtifactGeneratorNotFound",
				Message: "ArtifactGenerator not found",
			})
			return ctrl.Result{}, r.Status().Update(ctx, packageSource)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ArtifactGenerator: %w", err)
	}

	// Find Ready condition in ArtifactGenerator
	readyCondition := meta.FindStatusCondition(ag.Status.Conditions, "Ready")

	// Detect the source-watcher status-patch stall (fluxcd/pkg#934) and, if
	// stuck, drive source-watcher through a bounded retry schedule instead of
	// blindly copying the stuck Unknown across. See maybeRequeueArtifactGenerator
	// for the mechanism.
	if artifactGeneratorStuck(ag, readyCondition, nowFunc()) {
		return r.maybeRequeueArtifactGenerator(ctx, packageSource, ag)
	}

	// AG is not stuck — clear any requeue-tracking annotations the previous
	// stuck path left behind, then either surface the missing-Ready case or
	// copy the real condition through.
	if err := r.clearRequeueTracking(ctx, ag); err != nil {
		logger.Error(err, "failed to clear requeue tracking annotations", "artifactGenerator", ag.Name)
		// Non-fatal: annotations are best-effort bookkeeping.
	}

	if readyCondition == nil {
		// No Ready condition in ArtifactGenerator, set status to unknown
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionUnknown,
			Reason:  "ArtifactGeneratorNotReady",
			Message: "ArtifactGenerator Ready condition not found",
		})
		return ctrl.Result{}, r.Status().Update(ctx, packageSource)
	}

	// Copy Ready condition from ArtifactGenerator to PackageSource
	meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             readyCondition.Status,
		Reason:             readyCondition.Reason,
		Message:            readyCondition.Message,
		ObservedGeneration: packageSource.Generation,
		LastTransitionTime: readyCondition.LastTransitionTime,
	})

	logger.V(1).Info("updated PackageSource status from ArtifactGenerator",
		"packageSource", packageSource.Name,
		"status", readyCondition.Status,
		"reason", readyCondition.Reason)

	return ctrl.Result{}, r.Status().Update(ctx, packageSource)
}

// maybeRequeueArtifactGenerator advances the bounded-retry schedule for an AG
// whose Ready condition is stuck in the fluxcd/pkg#934 window (Inventory and
// ObservedSourcesDigest persisted, Ready condition write lost to the split
// patch). It nudges source-watcher via `reconcile.fluxcd.io/requestedAt` with
// exponential backoff and — after maxRequeueAttempts fruitless attempts —
// stops lying and surfaces the failure as PackageSource.Ready=False with
// reason SourceWatcherStalled so an operator can intervene.
//
// The retry state (attempt count + last-requeue timestamp) lives on the AG
// itself as annotations so it survives operator restarts and rides the same
// ownerReference lifecycle as the AG.
//
// TODO(remove once fluxcd/pkg#934 lands and is rolled out): once source-watcher
// consumes a patch.Helper that either serialises or transactionally combines
// the .status / .status.conditions writes, this whole retry driver can be
// deleted and updateStatus can copy the AG's Ready condition through
// unconditionally.
func (r *PackageSourceReconciler) maybeRequeueArtifactGenerator(ctx context.Context, packageSource *cozyv1alpha1.PackageSource, ag *sourcewatcherv1beta1.ArtifactGenerator) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	now := nowFunc()

	attempts, lastRequeueAt := readRequeueTracking(ag)
	decision := decideRequeue(attempts, lastRequeueAt, now)

	switch decision.action {
	case requeueActionGiveUp:
		message := fmt.Sprintf(
			"ArtifactGenerator %s/%s has been stuck with a lost Ready condition through %d requeue attempts; "+
				"source-watcher is not recovering. See https://github.com/fluxcd/pkg/issues/934. "+
				"An operator must restart source-watcher or manually inspect the ArtifactGenerator.",
			ag.Namespace, ag.Name, maxRequeueAttempts,
		)
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             reasonSourceWatcherBad,
			Message:            message,
			ObservedGeneration: packageSource.Generation,
		})
		logger.Info("source-watcher stalled after bounded requeues; surfacing PackageSource Ready=False",
			"packageSource", packageSource.Name, "artifactGenerator", ag.Name, "attempts", attempts)
		return ctrl.Result{}, r.Status().Update(ctx, packageSource)

	case requeueActionWait:
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionUnknown,
			Reason: reasonAwaitingRequeue,
			Message: fmt.Sprintf(
				"ArtifactGenerator Ready condition lost to fluxcd/pkg#934 patch.Helper race; "+
					"requeue %d/%d nudged source-watcher, waiting for a real Ready write.",
				attempts, maxRequeueAttempts,
			),
			ObservedGeneration: packageSource.Generation,
		})
		if err := r.Status().Update(ctx, packageSource); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: decision.wait}, nil

	case requeueActionBump:
		nextAttempt := attempts + 1
		if err := r.bumpArtifactGeneratorRequeue(ctx, ag, now, nextAttempt); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to bump ArtifactGenerator requeue annotation: %w", err)
		}
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionUnknown,
			Reason: reasonAwaitingRequeue,
			Message: fmt.Sprintf(
				"ArtifactGenerator Ready condition lost to fluxcd/pkg#934 patch.Helper race; "+
					"bumped reconcile.fluxcd.io/requestedAt (attempt %d/%d) to nudge source-watcher.",
				nextAttempt, maxRequeueAttempts,
			),
			ObservedGeneration: packageSource.Generation,
		})
		if err := r.Status().Update(ctx, packageSource); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("nudged source-watcher via reconcile.fluxcd.io/requestedAt",
			"packageSource", packageSource.Name, "artifactGenerator", ag.Name, "attempt", nextAttempt)
		return ctrl.Result{RequeueAfter: backoffFor(nextAttempt)}, nil
	}

	// Unreachable — decideRequeue always returns one of the three actions above.
	return ctrl.Result{}, nil
}

// artifactGeneratorStuck detects the fluxcd/pkg#934 stall signature: artifacts
// are demonstrably produced (Inventory populated and ObservedSourcesDigest set)
// on the current spec generation, yet the Ready condition is either missing or
// has been sitting in Unknown longer than stuckGracePeriod. The grace period
// keeps this predicate off the fast path during a normal in-flight rebuild;
// only genuinely quiescent AGs land here.
//
// Ready=True and Ready=False are BOTH pass-through (return false) — the retry
// driver only runs on the specific stuck-Unknown case. A real regeneration
// failure surfaces as Ready=False and is copied through unchanged.
func artifactGeneratorStuck(ag *sourcewatcherv1beta1.ArtifactGenerator, ready *metav1.Condition, now time.Time) bool {
	if len(ag.Status.Inventory) == 0 {
		return false
	}
	if ag.Status.ObservedSourcesDigest == "" {
		return false
	}
	if ready == nil {
		// Ready condition entirely absent: this is the half-persisted case the
		// PR was written for. Wait out the grace period from AG creation before
		// intervening so we don't fight a fresh-install AG that just hasn't
		// been touched yet.
		return ag.CreationTimestamp.Time.Add(stuckGracePeriod).Before(now)
	}
	if ready.Status != metav1.ConditionUnknown {
		return false
	}
	if ready.ObservedGeneration != ag.Generation {
		return false
	}
	// Only intervene if Unknown has held for the grace period; otherwise source
	// -watcher is legitimately mid-rebuild and will settle on its own.
	return ready.LastTransitionTime.Time.Add(stuckGracePeriod).Before(now)
}

// requeueAction enumerates what maybeRequeueArtifactGenerator should do given
// the current retry state.
type requeueAction int

const (
	requeueActionBump   requeueAction = iota // enough time elapsed — issue a fresh reconcile.fluxcd.io/requestedAt
	requeueActionWait                        // in backoff window — schedule a follow-up reconcile at wait
	requeueActionGiveUp                      // exceeded maxRequeueAttempts — surface as Ready=False
)

type requeueDecision struct {
	action requeueAction
	wait   time.Duration
}

// decideRequeue is the pure decision function driving maybeRequeueArtifactGenerator.
// Split out so it can be unit-tested without a cluster.
func decideRequeue(attempts int, lastRequeueAt time.Time, now time.Time) requeueDecision {
	if attempts >= maxRequeueAttempts {
		return requeueDecision{action: requeueActionGiveUp}
	}
	if attempts == 0 {
		return requeueDecision{action: requeueActionBump}
	}
	elapsed := now.Sub(lastRequeueAt)
	needed := backoffFor(attempts)
	if elapsed >= needed {
		return requeueDecision{action: requeueActionBump}
	}
	return requeueDecision{action: requeueActionWait, wait: needed - elapsed}
}

// backoffFor returns the backoff duration to wait AFTER the Nth bump before
// the (N+1)th. Attempts are 1-indexed. Exponential up to maxBackoff.
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := initialBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// bumpArtifactGeneratorRequeue nudges source-watcher via
// reconcile.fluxcd.io/requestedAt and updates our own bookkeeping annotations
// in a single merge patch so the AG is only mutated once per attempt.
func (r *PackageSourceReconciler) bumpArtifactGeneratorRequeue(ctx context.Context, ag *sourcewatcherv1beta1.ArtifactGenerator, now time.Time, nextAttempt int) error {
	patchBase := ag.DeepCopy()
	if ag.Annotations == nil {
		ag.Annotations = map[string]string{}
	}
	ag.Annotations[annotationFluxRequestedAt] = now.UTC().Format(time.RFC3339Nano)
	ag.Annotations[annotationRequeueAttempts] = strconv.Itoa(nextAttempt)
	ag.Annotations[annotationLastRequeueAt] = now.UTC().Format(time.RFC3339Nano)
	return r.Patch(ctx, ag, client.MergeFrom(patchBase))
}

// clearRequeueTracking removes our bookkeeping annotations once the AG is
// healthy again. Leaves reconcile.fluxcd.io/requestedAt alone — that annotation
// is owned by source-watcher's own reconcile loop semantics and must persist.
func (r *PackageSourceReconciler) clearRequeueTracking(ctx context.Context, ag *sourcewatcherv1beta1.ArtifactGenerator) error {
	if ag.Annotations == nil {
		return nil
	}
	_, hasAttempts := ag.Annotations[annotationRequeueAttempts]
	_, hasLast := ag.Annotations[annotationLastRequeueAt]
	if !hasAttempts && !hasLast {
		return nil
	}
	patchBase := ag.DeepCopy()
	delete(ag.Annotations, annotationRequeueAttempts)
	delete(ag.Annotations, annotationLastRequeueAt)
	return r.Patch(ctx, ag, client.MergeFrom(patchBase))
}

// readRequeueTracking pulls the retry-attempt counter and last-bump timestamp
// off the AG. Missing/malformed annotations are treated as "no prior attempts"
// so a corrupted counter can't wedge the retry loop.
func readRequeueTracking(ag *sourcewatcherv1beta1.ArtifactGenerator) (attempts int, lastRequeueAt time.Time) {
	if ag.Annotations == nil {
		return 0, time.Time{}
	}
	if raw, ok := ag.Annotations[annotationRequeueAttempts]; ok {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			attempts = parsed
		}
	}
	if raw, ok := ag.Annotations[annotationLastRequeueAt]; ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			lastRequeueAt = parsed
		}
	}
	return attempts, lastRequeueAt
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-packagesource").
		For(&cozyv1alpha1.PackageSource{}).
		Owns(&sourcewatcherv1beta1.ArtifactGenerator{}).
		Complete(r)
}

