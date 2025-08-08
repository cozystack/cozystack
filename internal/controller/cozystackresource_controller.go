package controller

import (
	"context"
	"sync"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
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

	// Configurable debounce duration
	Debounce time.Duration

	// Internal state for debouncing
	mu          sync.Mutex
	lastEvent   time.Time // Time of last CRUD event on CozystackResourceDefinition
	lastHandled time.Time // Last time the Deployment was actually restarted
}

// Reconcile handles the logic to restart the target Deployment only once,
// even if multiple events occur close together
func (r *CozystackResourceDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Only respond to our target deployment
	if req.Namespace != "cozy-system" || req.Name != "cozystack-api" {
		return ctrl.Result{}, nil
	}

	r.mu.Lock()
	le := r.lastEvent
	lh := r.lastHandled
	debounce := r.Debounce
	r.mu.Unlock()

	if debounce <= 0 {
		debounce = 5 * time.Second
	}

	// No events received yet — nothing to do
	if le.IsZero() {
		return ctrl.Result{}, nil
	}

	// Wait until the debounce duration has passed since the last event
	if d := time.Since(le); d < debounce {
		return ctrl.Result{RequeueAfter: debounce - d}, nil
	}

	// Already handled this event — skip restart
	if !lh.Before(le) {
		return ctrl.Result{}, nil
	}

	// Perform the restart by patching the deployment annotation
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-api"}, deploy); err != nil {
		log.Error(err, "Failed to get Deployment cozy-system/cozystack-api")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	if err := r.Patch(ctx, deploy, patch); err != nil {
		log.Error(err, "Failed to patch Deployment annotation")
		return ctrl.Result{}, err
	}

	// Mark this event as handled
	r.mu.Lock()
	r.lastHandled = le
	r.mu.Unlock()

	log.Info("Deployment cozy-system/cozystack-api successfully restarted")
	return ctrl.Result{}, nil
}

// SetupWithManager configures how the controller listens to events
func (r *CozystackResourceDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Debounce == 0 {
		r.Debounce = 5 * time.Second
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-restart-controller").
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
