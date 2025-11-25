package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
type CozystackResourceDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Debounce time.Duration

	mu          sync.Mutex
	lastEvent   time.Time
	lastHandled time.Time

	CozystackAPIKind string
}

func (r *CozystackResourceDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling CozystackResourceDefinitions", "request", req.NamespacedName)

	// Get all CozystackResourceDefinitions
	crdList := &cozyv1alpha1.CozystackResourceDefinitionList{}
	if err := r.List(ctx, crdList); err != nil {
		logger.Error(err, "failed to list CozystackResourceDefinitions")
		return ctrl.Result{}, err
	}

	logger.Info("Found CozystackResourceDefinitions", "count", len(crdList.Items))

	// Update HelmReleases for each CRD
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		logger.V(4).Info("Processing CRD", "crd", crd.Name, "hasValues", crd.Spec.Release.Values != nil)
		if err := r.updateHelmReleasesForCRD(ctx, crd); err != nil {
			logger.Error(err, "failed to update HelmReleases for CRD", "crd", crd.Name)
			// Continue with other CRDs even if one fails
		}
	}

	// Continue with debounced restart logic
	return r.debouncedRestart(ctx)
}

func (r *CozystackResourceDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Debounce == 0 {
		r.Debounce = 5 * time.Second
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystackresource-controller").
		Watches(
			&cozyv1alpha1.CozystackResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				r.mu.Lock()
				r.lastEvent = time.Now()
				r.mu.Unlock()
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: "cozy-system",
						Name:      "cozystack-api",
					},
				}}
			}),
		).
		Watches(
			&helmv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				hr, ok := obj.(*helmv2.HelmRelease)
				if !ok {
					return nil
				}
				// Only watch HelmReleases with cozystack.io/ui=true label
				if hr.Labels == nil || hr.Labels["cozystack.io/ui"] != "true" {
					return nil
				}
				// Trigger reconciliation of all CRDs when a HelmRelease with the label is created/updated
				r.mu.Lock()
				r.lastEvent = time.Now()
				r.mu.Unlock()
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: "cozy-system",
						Name:      "cozystack-api",
					},
				}}
			}),
		).
		Complete(r)
}

type crdHashView struct {
	Name string                                       `json:"name"`
	Spec cozyv1alpha1.CozystackResourceDefinitionSpec `json:"spec"`
}

func (r *CozystackResourceDefinitionReconciler) computeConfigHash(ctx context.Context) (string, error) {
	list := &cozyv1alpha1.CozystackResourceDefinitionList{}
	if err := r.List(ctx, list); err != nil {
		return "", err
	}

	slices.SortFunc(list.Items, sortCozyRDs)

	views := make([]crdHashView, 0, len(list.Items))
	for i := range list.Items {
		views = append(views, crdHashView{
			Name: list.Items[i].Name,
			Spec: list.Items[i].Spec,
		})
	}
	b, err := json.Marshal(views)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (r *CozystackResourceDefinitionReconciler) debouncedRestart(ctx context.Context) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	r.mu.Lock()
	le := r.lastEvent
	lh := r.lastHandled
	debounce := r.Debounce
	r.mu.Unlock()

	if debounce <= 0 {
		debounce = 5 * time.Second
	}
	if le.IsZero() {
		return ctrl.Result{}, nil
	}
	if d := time.Since(le); d < debounce {
		return ctrl.Result{RequeueAfter: debounce - d}, nil
	}
	if !lh.Before(le) {
		return ctrl.Result{}, nil
	}

	newHash, err := r.computeConfigHash(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	tpl, obj, patch, err := r.getWorkload(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-api"})
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	oldHash := tpl.Annotations["cozystack.io/config-hash"]

	if oldHash == newHash && oldHash != "" {
		r.mu.Lock()
		r.lastHandled = le
		r.mu.Unlock()
		logger.Info("No changes in CRD config; skipping restart", "hash", newHash)
		return ctrl.Result{}, nil
	}

	tpl.Annotations["cozystack.io/config-hash"] = newHash

	if err := r.Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, err
	}

	r.mu.Lock()
	r.lastHandled = le
	r.mu.Unlock()

	logger.Info("Updated cozystack-api podTemplate config-hash; rollout triggered",
		"old", oldHash, "new", newHash)
	return ctrl.Result{}, nil
}

func (r *CozystackResourceDefinitionReconciler) getWorkload(
	ctx context.Context,
	key types.NamespacedName,
) (tpl *corev1.PodTemplateSpec, obj client.Object, patch client.Patch, err error) {
	if r.CozystackAPIKind == "Deployment" {
		dep := &appsv1.Deployment{}
		if err := r.Get(ctx, key, dep); err != nil {
			return nil, nil, nil, err
		}
		obj = dep
		tpl = &dep.Spec.Template
		patch = client.MergeFrom(dep.DeepCopy())
	} else {
		ds := &appsv1.DaemonSet{}
		if err := r.Get(ctx, key, ds); err != nil {
			return nil, nil, nil, err
		}
		obj = ds
		tpl = &ds.Spec.Template
		patch = client.MergeFrom(ds.DeepCopy())
	}
	if tpl.Annotations == nil {
		tpl.Annotations = make(map[string]string)
	}
	return tpl, obj, patch, nil
}

func sortCozyRDs(a, b cozyv1alpha1.CozystackResourceDefinition) int {
	if a.Name == b.Name {
		return 0
	}
	if a.Name < b.Name {
		return -1
	}
	return 1
}

// updateHelmReleasesForCRD updates all HelmReleases that match the application labels from CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) updateHelmReleasesForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)

	// Use application labels to find HelmReleases
	// Labels: apps.cozystack.io/application.kind and apps.cozystack.io/application.group
	applicationKind := crd.Spec.Application.Kind
	applicationGroup := "apps.cozystack.io" // All applications use this group

	// Build label selector for HelmReleases
	// Only reconcile HelmReleases with cozystack.io/ui=true label
	labelSelector := client.MatchingLabels{
		"apps.cozystack.io/application.kind":  applicationKind,
		"apps.cozystack.io/application.group": applicationGroup,
		"cozystack.io/ui":                      "true",
	}

	// List all HelmReleases with matching labels
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, labelSelector); err != nil {
		logger.Error(err, "failed to list HelmReleases", "kind", applicationKind, "group", applicationGroup)
		return err
	}

	logger.Info("Found HelmReleases to update", "crd", crd.Name, "kind", applicationKind, "count", len(hrList.Items), "hasValues", crd.Spec.Release.Values != nil)
	if crd.Spec.Release.Values != nil {
		logger.V(4).Info("CRD has values", "crd", crd.Name, "valuesSize", len(crd.Spec.Release.Values.Raw))
	}

	// Log each HelmRelease that will be updated
	for i := range hrList.Items {
		hr := &hrList.Items[i]
		logger.V(4).Info("Processing HelmRelease", "name", hr.Name, "namespace", hr.Namespace, "kind", applicationKind)
	}

	// Update each HelmRelease
	for i := range hrList.Items {
		hr := &hrList.Items[i]
		if err := r.updateHelmReleaseChart(ctx, hr, crd); err != nil {
			logger.Error(err, "failed to update HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
	}

	return nil
}

// updateHelmReleaseChart updates the chart/chartRef and values in HelmRelease based on CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) updateHelmReleaseChart(ctx context.Context, hr *helmv2.HelmRelease, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)
	updated := false
	hrCopy := hr.DeepCopy()

	// Update based on Chart or ChartRef configuration
	if crd.Spec.Release.Chart != nil {
		// Using Chart (HelmRepository)
		if hrCopy.Spec.Chart == nil {
			// Need to create Chart spec
			hrCopy.Spec.Chart = &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart: crd.Spec.Release.Chart.Name,
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      crd.Spec.Release.Chart.SourceRef.Kind,
						Name:      crd.Spec.Release.Chart.SourceRef.Name,
						Namespace: crd.Spec.Release.Chart.SourceRef.Namespace,
					},
				},
			}
			// Clear ChartRef if it exists
			hrCopy.Spec.ChartRef = nil
			updated = true
		} else {
			// Update existing Chart spec
			if hrCopy.Spec.Chart.Spec.Chart != crd.Spec.Release.Chart.Name ||
				hrCopy.Spec.Chart.Spec.SourceRef.Kind != crd.Spec.Release.Chart.SourceRef.Kind ||
				hrCopy.Spec.Chart.Spec.SourceRef.Name != crd.Spec.Release.Chart.SourceRef.Name ||
				hrCopy.Spec.Chart.Spec.SourceRef.Namespace != crd.Spec.Release.Chart.SourceRef.Namespace {
				hrCopy.Spec.Chart.Spec.Chart = crd.Spec.Release.Chart.Name
				hrCopy.Spec.Chart.Spec.SourceRef = helmv2.CrossNamespaceObjectReference{
					Kind:      crd.Spec.Release.Chart.SourceRef.Kind,
					Name:      crd.Spec.Release.Chart.SourceRef.Name,
					Namespace: crd.Spec.Release.Chart.SourceRef.Namespace,
				}
				// Clear ChartRef if it exists
				hrCopy.Spec.ChartRef = nil
				updated = true
			}
		}
	} else if crd.Spec.Release.ChartRef != nil {
		// Using ChartRef (ExternalArtifact)
		expectedChartRef := &helmv2.CrossNamespaceSourceReference{
			Kind:      "ExternalArtifact",
			Name:      crd.Spec.Release.ChartRef.SourceRef.Name,
			Namespace: crd.Spec.Release.ChartRef.SourceRef.Namespace,
		}

		if hrCopy.Spec.ChartRef == nil {
			// Need to create ChartRef
			hrCopy.Spec.ChartRef = expectedChartRef
			// Clear Chart if it exists
			hrCopy.Spec.Chart = nil
			updated = true
		} else {
			// Update existing ChartRef
			if hrCopy.Spec.ChartRef.Kind != expectedChartRef.Kind ||
				hrCopy.Spec.ChartRef.Name != expectedChartRef.Name ||
				hrCopy.Spec.ChartRef.Namespace != expectedChartRef.Namespace {
				hrCopy.Spec.ChartRef = expectedChartRef
				// Clear Chart if it exists
				hrCopy.Spec.Chart = nil
				updated = true
			}
		}
	}

	// Update Values from CRD if specified
	var mergedValues *apiextensionsv1.JSON
	var err error
	if crd.Spec.Release.Values != nil {
		logger.V(4).Info("Merging values from CRD", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
		mergedValues, err = r.mergeHelmReleaseValues(crd.Spec.Release.Values, hrCopy.Spec.Values)
		if err != nil {
			logger.Error(err, "failed to merge values", "name", hr.Name, "namespace", hr.Namespace)
			return fmt.Errorf("failed to merge values: %w", err)
		}
	} else {
		// Even if CRD has no values, we still need to ensure _namespace is set
		mergedValues = hrCopy.Spec.Values
	}

	// Always inject namespace labels (top-level _namespace field)
	// This matches the behavior in cozystack-api and NamespaceHelmReconciler
	mergedValues, err = r.injectNamespaceLabelsIntoValues(ctx, mergedValues, hrCopy.Namespace)
	if err != nil {
		logger.Error(err, "failed to inject namespace labels", "name", hr.Name, "namespace", hr.Namespace)
		// Continue even if namespace labels injection fails
	}

	// Always update values to ensure _cozystack and _namespace are applied
	// This ensures that CRD values (especially _cozystack and _namespace) are always applied
	// We always update to ensure CRD values are propagated, even if they appear equal
	// This is important because JSON comparison might not catch all differences (e.g., field order)
	if crd.Spec.Release.Values != nil || mergedValues != hrCopy.Spec.Values {
		hrCopy.Spec.Values = mergedValues
		updated = true
		if crd.Spec.Release.Values != nil {
			logger.Info("Updated values from CRD", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
		} else {
			logger.V(4).Info("Updated values with namespace labels", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
		}
	} else {
		logger.V(4).Info("No values update needed", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
	}

	if !updated {
		return nil
	}

	// Update the HelmRelease
	patch := client.MergeFrom(hr.DeepCopy())
	if err := r.Patch(ctx, hrCopy, patch); err != nil {
		return err
	}

	logger.Info("Updated HelmRelease", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
	return nil
}

// mergeHelmReleaseValues merges CRD default values with existing HelmRelease values
// All fields are merged except "_cozystack" and "_namespace" which are fully overwritten from CRD values
// Existing HelmRelease values (outside of _cozystack and _namespace) take precedence (user values override defaults)
func (r *CozystackResourceDefinitionReconciler) mergeHelmReleaseValues(crdValues, existingValues *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	// If CRD has no values, preserve existing
	if crdValues == nil || len(crdValues.Raw) == 0 {
		return existingValues, nil
	}

	// If existing has no values, use CRD values
	if existingValues == nil || len(existingValues.Raw) == 0 {
		return crdValues, nil
	}

	var crdMap, existingMap map[string]interface{}

	// Parse CRD values (defaults)
	if err := json.Unmarshal(crdValues.Raw, &crdMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CRD values: %w", err)
	}

	// Parse existing HelmRelease values
	if err := json.Unmarshal(existingValues.Raw, &existingMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal existing values: %w", err)
	}

	// Start with existing values as base (user values take priority)
	// Then merge CRD values on top, but _cozystack and _namespace from CRD completely overwrite
	merged := deepMergeMaps(existingMap, crdMap)

	// Explicitly handle "_cozystack" field: CRD values completely overwrite existing
	// This ensures _cozystack field from CRD is always used, even if user modified it
	if crdCozystack, exists := crdMap["_cozystack"]; exists {
		merged["_cozystack"] = crdCozystack
	}

	// Explicitly handle "_namespace" field: CRD values completely overwrite existing
	// This ensures _namespace field from CRD is always used, even if user modified it
	if crdNamespace, exists := crdMap["_namespace"]; exists {
		merged["_namespace"] = crdNamespace
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}

// deepMergeMaps performs a deep merge of two maps
func deepMergeMaps(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy base map
	for k, v := range base {
		result[k] = v
	}

	// Merge override map
	for k, v := range override {
		if baseVal, exists := result[k]; exists {
			// If both are maps, recursively merge
			if baseMap, ok := baseVal.(map[string]interface{}); ok {
				if overrideMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMergeMaps(baseMap, overrideMap)
					continue
				}
			}
		}
		// Override takes precedence
		result[k] = v
	}

	return result
}

// valuesEqual compares two JSON values for equality
func valuesEqual(a, b *apiextensionsv1.JSON) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Simple byte comparison (could be improved with canonical JSON)
	return string(a.Raw) == string(b.Raw)
}

// injectNamespaceLabelsIntoValues injects namespace.cozystack.io/* labels into _namespace (top-level)
// This matches the behavior in cozystack-api and NamespaceHelmReconciler
func (r *CozystackResourceDefinitionReconciler) injectNamespaceLabelsIntoValues(ctx context.Context, values *apiextensionsv1.JSON, namespaceName string) (*apiextensionsv1.JSON, error) {
	// Get namespace to extract namespace.cozystack.io/* labels
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace); err != nil {
		// If namespace not found, return values as-is
		return values, nil
	}

	// Extract namespace.cozystack.io/* labels
	namespaceLabels := extractNamespaceLabelsFromNamespace(namespace)
	if len(namespaceLabels) == 0 {
		// No namespace labels, return values as-is
		return values, nil
	}

	// Parse values
	var valuesMap map[string]interface{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &valuesMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal values: %w", err)
		}
	} else {
		valuesMap = make(map[string]interface{})
	}

	// Convert namespaceLabels from map[string]string to map[string]interface{}
	namespaceLabelsMap := make(map[string]interface{})
	for k, v := range namespaceLabels {
		namespaceLabelsMap[k] = v
	}

	// Namespace labels completely overwrite existing _namespace field (top-level)
	valuesMap["_namespace"] = namespaceLabelsMap

	// Marshal back to JSON
	mergedJSON, err := json.Marshal(valuesMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal values with namespace labels: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}
