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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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
	if err := r.reconcileCozyRDAndUpdateHelmReleases(ctx); err != nil {
		return ctrl.Result{}, err
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

// reconcileCozyRDAndUpdateHelmReleases reconciles all CozystackResourceDefinitions and updates HelmReleases from them
func (r *CozystackResourceDefinitionReconciler) reconcileCozyRDAndUpdateHelmReleases(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// List all CozystackResourceDefinitions
	crdList := &cozyv1alpha1.CozystackResourceDefinitionList{}
	if err := r.List(ctx, crdList); err != nil {
		logger.Error(err, "failed to list CozystackResourceDefinitions")
		return err
	}

	// Update HelmReleases for each CRD
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		if err := r.updateHelmReleasesForCRD(ctx, crd); err != nil {
			logger.Error(err, "failed to update HelmReleases for CRD", "crd", crd.Name)
			// Continue with other CRDs even if one fails
		}
	}

	return nil
}

// updateHelmReleasesForCRD updates all HelmReleases that match the application labels from CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) updateHelmReleasesForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)

	// Use application labels to find HelmReleases
	// Labels: apps.cozystack.io/application.kind and apps.cozystack.io/application.group
	applicationKind := crd.Spec.Application.Kind
	
	// Validate that applicationKind is non-empty
	if applicationKind == "" {
		logger.Error(fmt.Errorf("Application.Kind is empty"), "Skipping HelmRelease update: invalid CozystackResourceDefinition", "crd", crd.Name)
		return nil
	}
	
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

	logger.V(4).Info("Found HelmReleases to update", "crd", crd.Name, "kind", applicationKind, "count", len(hrList.Items))

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

// updateHelmReleaseChart updates the chart in HelmRelease based on CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) updateHelmReleaseChart(ctx context.Context, hr *helmv2.HelmRelease, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)
	hrCopy := hr.DeepCopy()
	updated := false

	// Validate Chart configuration exists
	if crd.Spec.Release.Chart.Name == "" {
		logger.V(4).Info("Skipping HelmRelease chart update: Chart.Name is empty", "crd", crd.Name)
		return nil
	}

	// Validate SourceRef fields
	if crd.Spec.Release.Chart.SourceRef.Kind == "" ||
		crd.Spec.Release.Chart.SourceRef.Name == "" ||
		crd.Spec.Release.Chart.SourceRef.Namespace == "" {
		logger.Error(fmt.Errorf("invalid SourceRef in CRD"), "Skipping HelmRelease chart update: SourceRef fields are incomplete",
			"crd", crd.Name,
			"kind", crd.Spec.Release.Chart.SourceRef.Kind,
			"name", crd.Spec.Release.Chart.SourceRef.Name,
			"namespace", crd.Spec.Release.Chart.SourceRef.Namespace)
		return nil
	}

	// Get version and reconcileStrategy from CRD or use defaults
	version := ">= 0.0.0-0"
	reconcileStrategy := "Revision"
	// TODO: Add Version and ReconcileStrategy fields to CozystackResourceDefinitionChart if needed

	// Build expected SourceRef
	expectedSourceRef := helmv2.CrossNamespaceObjectReference{
		Kind:      crd.Spec.Release.Chart.SourceRef.Kind,
		Name:      crd.Spec.Release.Chart.SourceRef.Name,
		Namespace: crd.Spec.Release.Chart.SourceRef.Namespace,
	}

	if hrCopy.Spec.Chart == nil {
		// Need to create Chart spec
		hrCopy.Spec.Chart = &helmv2.HelmChartTemplate{
			Spec: helmv2.HelmChartTemplateSpec{
				Chart:             crd.Spec.Release.Chart.Name,
				Version:           version,
				ReconcileStrategy: reconcileStrategy,
				SourceRef:         expectedSourceRef,
			},
		}
		updated = true
	} else {
		// Update existing Chart spec
		if hrCopy.Spec.Chart.Spec.Chart != crd.Spec.Release.Chart.Name ||
			hrCopy.Spec.Chart.Spec.SourceRef != expectedSourceRef {
			hrCopy.Spec.Chart.Spec.Chart = crd.Spec.Release.Chart.Name
			hrCopy.Spec.Chart.Spec.SourceRef = expectedSourceRef
			updated = true
		}
	}

	if updated {
		logger.V(4).Info("Updating HelmRelease chart", "name", hr.Name, "namespace", hr.Namespace)
		if err := r.Update(ctx, hrCopy); err != nil {
			return fmt.Errorf("failed to update HelmRelease: %w", err)
		}
	}

	return nil
}
