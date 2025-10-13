package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/cozystack/cozystack/internal/controller/dashboard"
	"github.com/cozystack/cozystack/internal/shared/crdmem"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type CozystackResourceDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Debounce time.Duration

	mu          sync.Mutex
	lastEvent   time.Time
	lastHandled time.Time

	mem *crdmem.Memory

	// Track static resources initialization
	staticResourcesInitialized bool
	staticResourcesMutex       sync.Mutex
}

func (r *CozystackResourceDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	crd := &cozyv1alpha1.CozystackResourceDefinition{}
	err := r.Get(ctx, types.NamespacedName{Name: req.Name}, crd)
	if err == nil {
		if r.mem != nil {
			r.mem.Upsert(crd)
		}

		mgr := dashboard.NewManager(
			r.Client,
			r.Scheme,
			dashboard.WithCRDListFunc(func(c context.Context) ([]cozyv1alpha1.CozystackResourceDefinition, error) {
				if r.mem != nil {
					return r.mem.ListFromCacheOrAPI(c, r.Client)
				}
				var list cozyv1alpha1.CozystackResourceDefinitionList
				if err := r.Client.List(c, &list); err != nil {
					return nil, err
				}
				return list.Items, nil
			}),
		)

		if res, derr := mgr.EnsureForCRD(ctx, crd); derr != nil || res.Requeue || res.RequeueAfter > 0 {
			return res, derr
		}

		// After processing CRD, perform cleanup of orphaned resources
		// This should be done after cache warming to ensure all current resources are known
		if cleanupErr := mgr.CleanupOrphanedResources(ctx); cleanupErr != nil {
			logger.Error(cleanupErr, "Failed to cleanup orphaned dashboard resources")
			// Don't fail the reconciliation, just log the error
		}

		r.mu.Lock()
		r.lastEvent = time.Now()
		r.mu.Unlock()
		return ctrl.Result{}, nil
	}

	// Handle error cases (err is guaranteed to be non-nil here)
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	// If resource is not found, clean up from memory
	if r.mem != nil {
		r.mem.Delete(req.Name)
	}
	if req.Namespace == "cozy-system" && req.Name == "cozystack-api" {
		return r.debouncedRestart(ctx, logger)
	}
	return ctrl.Result{}, nil
}

// initializeStaticResourcesOnce ensures static resources are created only once
func (r *CozystackResourceDefinitionReconciler) initializeStaticResourcesOnce(ctx context.Context) error {
	r.staticResourcesMutex.Lock()
	defer r.staticResourcesMutex.Unlock()

	if r.staticResourcesInitialized {
		return nil // Already initialized
	}

	// Create dashboard manager and initialize static resources
	mgr := dashboard.NewManager(
		r.Client,
		r.Scheme,
		dashboard.WithCRDListFunc(func(c context.Context) ([]cozyv1alpha1.CozystackResourceDefinition, error) {
			if r.mem != nil {
				return r.mem.ListFromCacheOrAPI(c, r.Client)
			}
			var list cozyv1alpha1.CozystackResourceDefinitionList
			if err := r.Client.List(c, &list); err != nil {
				return nil, err
			}
			return list.Items, nil
		}),
	)

	if err := mgr.InitializeStaticResources(ctx); err != nil {
		return err
	}

	r.staticResourcesInitialized = true
	log.FromContext(ctx).Info("Static dashboard resources initialized successfully")
	return nil
}

func (r *CozystackResourceDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Debounce == 0 {
		r.Debounce = 5 * time.Second
	}
	if r.mem == nil {
		r.mem = crdmem.Global()
	}
	if err := r.mem.EnsurePrimingWithManager(mgr); err != nil {
		return err
	}

	// Initialize static resources once during controller startup using manager.Runnable
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if err := r.initializeStaticResourcesOnce(ctx); err != nil {
			log.FromContext(ctx).Error(err, "Failed to initialize static resources")
			return err
		}
		return nil
	})); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystackresource-controller").
		For(&cozyv1alpha1.CozystackResourceDefinition{}, builder.WithPredicates()).
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
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5, // Allow more concurrent reconciles with proper rate limiting
		}).
		Complete(r)
}

type crdHashView struct {
	Name string                                       `json:"name"`
	Spec cozyv1alpha1.CozystackResourceDefinitionSpec `json:"spec"`
}

func (r *CozystackResourceDefinitionReconciler) computeConfigHash(ctx context.Context) (string, error) {
	var items []cozyv1alpha1.CozystackResourceDefinition
	if r.mem != nil {
		list, err := r.mem.ListFromCacheOrAPI(ctx, r.Client)
		if err != nil {
			return "", err
		}
		items = list
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	views := make([]crdHashView, 0, len(items))
	for i := range items {
		views = append(views, crdHashView{
			Name: items[i].Name,
			Spec: items[i].Spec,
		})
	}
	b, err := json.Marshal(views)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (r *CozystackResourceDefinitionReconciler) debouncedRestart(ctx context.Context, logger logr.Logger) (ctrl.Result, error) {
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

	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-api"}, deploy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	oldHash := deploy.Spec.Template.Annotations["cozystack.io/config-hash"]

	if oldHash == newHash && oldHash != "" {
		r.mu.Lock()
		r.lastHandled = le
		r.mu.Unlock()
		logger.Info("No changes in CRD config; skipping restart", "hash", newHash)
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(deploy.DeepCopy())
	deploy.Spec.Template.Annotations["cozystack.io/config-hash"] = newHash

	if err := r.Patch(ctx, deploy, patch); err != nil {
		return ctrl.Result{}, err
	}

	r.mu.Lock()
	r.lastHandled = le
	r.mu.Unlock()

	logger.Info("Updated cozystack-api podTemplate config-hash; rollout triggered",
		"old", oldHash, "new", newHash)
	return ctrl.Result{}, nil
}
