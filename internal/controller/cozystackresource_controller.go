package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
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

	// Get all CozystackResourceDefinitions
	crdList := &cozyv1alpha1.CozystackResourceDefinitionList{}
	if err := r.List(ctx, crdList); err != nil {
		logger.Error(err, "failed to list CozystackResourceDefinitions")
		return ctrl.Result{}, err
	}

	// Update HelmReleases for each CRD
	for i := range crdList.Items {
		crd := &crdList.Items[i]
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

// updateHelmReleasesForCRD updates all HelmReleases that match the labels from CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) updateHelmReleasesForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)

	// Skip if no labels defined
	if len(crd.Spec.Release.Labels) == 0 {
		return nil
	}

	// List all HelmReleases with matching labels
	hrList := &helmv2.HelmReleaseList{}
	labelSelector := client.MatchingLabels(crd.Spec.Release.Labels)
	if err := r.List(ctx, hrList, labelSelector); err != nil {
		return err
	}

	logger.Info("Found HelmReleases to update", "crd", crd.Name, "count", len(hrList.Items))

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

// updateHelmReleaseChart updates the chart/chartRef in HelmRelease based on CozystackResourceDefinition
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

	if !updated {
		return nil
	}

	// Update the HelmRelease
	patch := client.MergeFrom(hr.DeepCopy())
	if err := r.Patch(ctx, hrCopy, patch); err != nil {
		return err
	}

	logger.Info("Updated HelmRelease chart/chartRef", "name", hr.Name, "namespace", hr.Namespace, "crd", crd.Name)
	return nil
}
