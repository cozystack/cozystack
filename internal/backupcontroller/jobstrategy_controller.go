package backupcontroller

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/template"
	appscozystackio "github.com/cozystack/cozystack/pkg/apis/apps"
)

// BackupJobStrategyReconciler reconciles BackupJob with a strategy referencing
// Job.strategy.backups.cozystack.io objects.
type BackupJobStrategyReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	dynClient dynamic.Interface
	mapper    meta.RESTMapper
}

func (r *BackupJobStrategyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(2).Info("reconciling")

	j := &backupsv1alpha1.BackupJob{}

	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, j); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(3).Info("BackupJob not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var applicationRefAPIGroup string
	var strategyRefAPIGroup string
	var storageRefAPIGroup string
	if j.Spec.ApplicationRef.APIGroup != nil {
		applicationRefAPIGroup = *j.Spec.ApplicationRef.APIGroup
	}
	if j.Spec.StrategyRef.APIGroup != nil {
		strategyRefAPIGroup = *j.Spec.StrategyRef.APIGroup
	}
	if j.Spec.StorageRef.APIGroup != nil {
		storageRefAPIGroup = *j.Spec.StorageRef.APIGroup
	}

	if strategyRefAPIGroup != strategyv1alpha1.GroupVersion.Group {
		return ctrl.Result{}, nil
	}
	if j.Spec.StrategyRef.Kind != strategyv1alpha1.JobStrategyKind {
		return ctrl.Result{}, nil
	}

	app, err := r.getUnstructured(ctx, applicationRefAPIGroup, j.Spec.ApplicationRef.Kind, j.Namespace, j.Spec.ApplicationRef.Name)
	if err != nil {
		// TODO: we should handle not-found errors separately, but it's not
		// clear, how to trigger a reconcile if the application is created
		// later, so we just rely on the default exponential backoff.
		return ctrl.Result{}, err
	}

	strategy := &strategyv1alpha1.Job{}
	err = r.Get(ctx, types.NamespacedName{Name: j.Spec.StrategyRef.Name}, strategy)
	if err != nil {
		// TODO: as with the app, not-found errors for strategies are pointless
		// to retry, but a reconcile should be triggered if a strategy is later
		// created.
		return ctrl.Result{}, err
	}

	// TODO: we should use the storage in a more generic way, but since the
	// storage part of the backups API is not implemented at all, we skip this
	// for now and revert to a default implementation: only Bucket is supported
	if storageRefAPIGroup != appscozystackio.GroupName {
		return ctrl.Result{}, nil
	}
	if j.Spec.StorageRef.Kind != "Bucket" {
		return ctrl.Result{}, nil
	}
	_, err = r.getUnstructured(ctx, storageRefAPIGroup, j.Spec.StorageRef.Kind, j.Namespace, j.Spec.StorageRef.Name)
	if err != nil {
		// TODO: same not-found caveat as before
		return ctrl.Result{}, err
	}

	values, ok := app.Object["spec"].(map[string]any)
	if !ok {
		values = map[string]any{}
	}
	release := map[string]any{
		"Name":      fmt.Sprintf("%s-%s", strings.ToLower(j.Spec.ApplicationRef.Kind), j.Spec.ApplicationRef.Name),
		"Namespace": j.Namespace,
	}
	templateContext := map[string]any{
		"Release": release,
		"Values":  values,
		"Storage": map[string]any{
			"APIGroup": storageRefAPIGroup,
			"Kind":     j.Spec.StorageRef.Kind,
			"Name":     fmt.Sprintf("%s-%s", strings.ToLower(j.Spec.StorageRef.Kind), j.Spec.StorageRef.Name),
		},
	}
	podTemplate, err := template.Template(&strategy.Spec.Template, templateContext)
	if err != nil {
		return ctrl.Result{}, err
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      j.Name,
			Namespace: j.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: *podTemplate,
		},
	}

	if err := r.Create(ctx, job); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers our controller with the Manager and sets up watches.
func (r *BackupJobStrategyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	cfg := rest.CopyConfig(mgr.GetConfig())
	var err error
	r.dynClient, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return err
	}

	r.mapper, err = apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&backupsv1alpha1.BackupJob{}).
		Complete(r)
}

func (r *BackupJobStrategyReconciler) getUnstructured(ctx context.Context, apiGroup, kind, namespace, name string) (*unstructured.Unstructured, error) {
	mapping, err := r.mapper.RESTMapping(schema.GroupKind{Group: apiGroup, Kind: kind})
	if err != nil {
		return nil, err
	}

	ns := namespace
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		ns = ""
	}

	obj, err := r.dynClient.Resource(mapping.Resource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}
