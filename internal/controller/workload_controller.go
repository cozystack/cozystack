package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)


// WorkloadMonitorReconciler reconciles a WorkloadMonitor object
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// workload_controller.go
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	w := &cozyv1alpha1.Workload{}
	if err := r.Get(ctx, req.NamespacedName, w); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If my monitor is gone, delete me.
	monName, has := w.Labels["workloads.cozystack.io/monitor"]
	if !has {
		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, w))
	}
	monitor := &cozyv1alpha1.WorkloadMonitor{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: monName}, monitor); apierrors.IsNotFound(err) {
		// Monitor is gone → delete the Workload.  Ignore NotFound here, too.
		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, w))
	} else if err != nil {
		// Some other error fetching the monitor
		log.Error(err, "failed to get WorkloadMonitor", "monitor", monName)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch Workload objects
		For(&cozyv1alpha1.Workload{}).
		Complete(r)
}
