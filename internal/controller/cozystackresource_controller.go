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
	// Only handle debounced restart logic
	// HelmRelease reconciliation is handled by CozystackResourceDefinitionHelmReconciler
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
