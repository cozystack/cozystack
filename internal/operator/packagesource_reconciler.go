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
	"strings"

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

	// Update PackageSource status (variants and conditions from ArtifactGenerator)
	if err := r.updateStatus(ctx, packageSource); err != nil {
		logger.Error(err, "failed to update status")
		// Don't return error, status update is not critical
	}

	return ctrl.Result{}, nil
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

// updateStatus updates PackageSource status (variants and conditions from ArtifactGenerator)
func (r *PackageSourceReconciler) updateStatus(ctx context.Context, packageSource *cozyv1alpha1.PackageSource) error {
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
		return r.Status().Update(ctx, packageSource)
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
			return r.Status().Update(ctx, packageSource)
		}
		return fmt.Errorf("failed to get ArtifactGenerator: %w", err)
	}

	// Find Ready condition in ArtifactGenerator
	readyCondition := meta.FindStatusCondition(ag.Status.Conditions, "Ready")

	// Workaround for a race in fluxcd/source-watcher (bug tracked upstream in
	// TODO(remove once https://github.com/fluxcd/source-watcher/issues/TBD is
	// fixed and cozystack has bumped past the fix). Verified against
	// v2.1.0 sources — the same code paths ship in v2.2.x (flux-aio latest
	// as of this writing), so a simple version bump does not remove the
	// need for this synthesis.
	//
	// Kvaps' review on #3182 confirmed the diagnosis by tracing the
	// upstream reconciler:
	//   1. source-watcher writes new Inventory + ObservedSourcesDigest and
	//      then tries to patch conditions with Ready=True. fluxcd uses a
	//      `patch.Helper` that patches `.status` and `.status.conditions`
	//      as SEPARATE apiserver requests — the etcd load window during
	//      first-install can land a timeout between the two so that the
	//      "artifacts done" fields land while the Ready condition write
	//      never does.
	//   2. On the next reconcile the "!hasDrifted" branch returns before
	//      touching conditions again. The deferred `summarizeStatus` still
	//      runs and adjusts the Reconciling condition, but it never
	//      promotes a stuck Unknown Ready to True — that promotion is only
	//      done on the successful `hasDrifted` path.
	// Result: the ArtifactGenerator is functionally Ready — downstream Flux
	// consumes its ExternalArtifacts, HelmReleases install and become Ready
	// — but its Ready condition stays Unknown, this reconciler faithfully
	// copies Unknown onto the PackageSource, and any Package/HR that waits
	// on `PackageSource.status.conditions[Ready]=True` (in particular
	// `cozystack-platform`) times out at 15m and fails the install.
	//
	// The workaround synthesises Ready=True from observable state when
	// artifacts are present. Note that this is an INTENTIONAL contract
	// change on PackageSource.status.conditions[Ready]:
	//
	//   BEFORE: Ready=True means "source-watcher has processed the current
	//           revision of the sources referenced by this PackageSource".
	//   AFTER:  Ready=True means "valid consumable artifacts for THIS
	//           PackageSource exist in the cluster" — a slightly weaker
	//           but strictly more useful signal for the downstream Package
	//           / HelmRelease reconcile ordering that actually cares about
	//           artifact availability rather than reconcile progress.
	//
	// The reason for the weaker contract: source-watcher writes the same
	// `condition.ObservedGeneration = ag.Generation` on EVERY condition
	// mutation, including the Progressing/Unknown mark at the very start of
	// a rebuild (see `fluxcd/pkg/runtime/conditions.Set`). And
	// ArtifactGeneratorStatus exposes no object-level ObservedGeneration
	// (only ReconcileRequestStatus is inlined). So a condition-level
	// Generation match cannot distinguish "current revision fully
	// processed" from "rebuild in progress, previous Inventory/digest still
	// persisted" — meaning the predicate can (and does) fire during a
	// legitimate content-only regeneration where the OCI revision changed
	// but `.metadata.generation` did not.
	//
	// Consequence in practice — accepted, and arguably better:
	//   * install/upgrade ordering no longer flaps: PackageSource Ready
	//     does not dip through Unknown while a new artifact revision is
	//     being processed and previous artifacts are still on disk.
	//   * a genuine regeneration FAILURE still surfaces — the upstream
	//     writes Ready=False on the same reconcile, and this predicate
	//     returns false for Ready=False so the caller passes it through.
	//   * a spec-edit (which does bump ag.Generation) is still detected by
	//     the ObservedGeneration matching in
	//     `artifactGeneratorObservablyReady`, so we do not synthesise
	//     Ready=True on artifacts that are known-stale by generation.
	//
	// If the upstream Ready condition eventually resolves, the
	// Owns(&ArtifactGenerator{}) watch below re-fires this reconciler and
	// the real Ready condition is copied over, replacing the synthetic
	// one. The synthesis reason `ArtifactsGeneratedAwaitingUpstreamStatus`
	// is greppable so operators can audit which PackageSources are
	// currently on the workaround path.
	if artifactGeneratorObservablyReady(ag, readyCondition) {
		syntheticReason := "ArtifactsGeneratedAwaitingUpstreamStatus"
		syntheticMessage := "ArtifactGenerator Inventory populated and ObservedSourcesDigest set; " +
			"upstream Ready condition has not been finalised. Synthesising Ready=True " +
			"from observable artifact state so downstream Package/HR reconciles do not " +
			"block on the fluxcd/source-watcher status-patch early-exit bug."
		// Preserve LastTransitionTime if we've already stamped this same synthetic
		// condition — controller-runtime meta.SetStatusCondition rewrites only
		// on Status/Reason change, so identical calls are cheap.
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             syntheticReason,
			Message:            syntheticMessage,
			ObservedGeneration: packageSource.Generation,
		})
		logger.V(1).Info("synthesised PackageSource Ready=True from ArtifactGenerator observable state",
			"packageSource", packageSource.Name,
			"reason", syntheticReason,
			"inventoryLen", len(ag.Status.Inventory))
		return r.Status().Update(ctx, packageSource)
	}

	if readyCondition == nil {
		// No Ready condition in ArtifactGenerator, set status to unknown
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionUnknown,
			Reason:  "ArtifactGeneratorNotReady",
			Message: "ArtifactGenerator Ready condition not found",
		})
		return r.Status().Update(ctx, packageSource)
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

	return r.Status().Update(ctx, packageSource)
}

// artifactGeneratorObservablyReady returns true iff the ArtifactGenerator has
// observable consumable artifacts (Inventory populated, ObservedSourcesDigest
// set, Ready condition present and observed at the current Generation) AND
// its Ready condition has stalled in Unknown. That is exactly the window the
// fluxcd/source-watcher status-patch race produces; treating it as Ready lets
// downstream Package / HelmRelease reconciles proceed without waiting on
// an upstream fix.
//
// The caller MUST pass in the pre-resolved Ready condition (or nil) — the
// caller already did the FindStatusCondition lookup in order to copy it
// through on the non-workaround path, and re-doing the lookup here (kvaps'
// nit) is wasted work when both call sites want the same result.
//
// The Generation check is load-bearing for SPEC edits: if the user edits
// PackageSource.spec.variants the derived ArtifactGenerator gets a new
// Generation. Between "source-watcher started rebuilding gen=N+1" and
// "source-watcher persisted the new Inventory/digest", Inventory / digest
// still reflect gen=N. Without the Generation match, we would synthesise
// Ready=True on artifacts that are known-stale by generation. See the block
// comment at the call site for why this predicate can — and does —
// intentionally still fire during a content-only regeneration (same
// Generation, new OCI revision), which is what makes the resulting
// PackageSource Ready contract "valid consumable artifacts exist" rather
// than "current revision processed".
//
// Ready is required to be present because condition.ObservedGeneration is
// the only Generation signal source-watcher exposes on
// ArtifactGeneratorStatus (only ReconcileRequestStatus is inlined at the
// object level, and it carries no Generation). Without a condition to read
// the Generation from we cannot verify the artifacts are for the current
// spec.
//
// Ready=True is passed through by the caller unchanged (we return false so
// the caller copies the real True across, preserving upstream reason/message
// for audit). Ready=False is passed through unchanged (we return false so
// the caller surfaces the real failure — this workaround must NEVER mask a
// genuine regeneration failure).
func artifactGeneratorObservablyReady(ag *sourcewatcherv1beta1.ArtifactGenerator, ready *metav1.Condition) bool {
	if len(ag.Status.Inventory) == 0 {
		return false
	}
	if ag.Status.ObservedSourcesDigest == "" {
		return false
	}
	if ready == nil {
		return false
	}
	if ready.Status != metav1.ConditionUnknown {
		return false
	}
	// Ready condition must observe the current spec Generation — otherwise
	// Inventory and ObservedSourcesDigest may still reflect a previous
	// spec-edit generation. (This does not distinguish content-only
	// regenerations on the same Generation; see call-site comment.)
	return ready.ObservedGeneration == ag.Generation
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-packagesource").
		For(&cozyv1alpha1.PackageSource{}).
		Owns(&sourcewatcherv1beta1.ArtifactGenerator{}).
		Complete(r)
}

