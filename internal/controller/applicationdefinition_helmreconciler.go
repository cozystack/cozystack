package controller

import (
	"context"
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/shared/appdefowner"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=applicationdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

// ApplicationDefinitionHelmReconciler reconciles ApplicationDefinitions
// and updates related HelmReleases when an ApplicationDefinition changes.
// This controller does NOT watch HelmReleases to avoid mutual reconciliation storms
// with Flux's helm-controller.
type ApplicationDefinitionHelmReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *ApplicationDefinitionHelmReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the ApplicationDefinition that triggered this reconciliation
	appDef := &cozyv1alpha1.ApplicationDefinition{}
	if err := r.Get(ctx, req.NamespacedName, appDef); err != nil {
		logger.Error(err, "failed to get ApplicationDefinition", "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only the definition that owns its kind may rewrite that kind's releases.
	// HelmReleases are selected by kind label alone, so without this a second
	// definition declaring the same kind would repoint every release of it at
	// its own artifact.
	if kind := appDef.Spec.Application.Kind; kind != "" {
		defs := &cozyv1alpha1.ApplicationDefinitionList{}
		if err := r.List(ctx, defs); err != nil {
			return ctrl.Result{}, err
		}
		if owner := appdefowner.Owners(defs.Items)[kind]; owner != appDef.Name {
			logger.Info("skipping HelmRelease update: application kind is owned by another definition", "appDef", appDef.Name, "kind", kind, "owner", owner)
			if r.Recorder != nil {
				r.Recorder.Eventf(appDef, corev1.EventTypeWarning, "KindClaimed",
					"application kind %s is already owned by ApplicationDefinition %s; this definition does not manage its releases", kind, owner)
			}
			return ctrl.Result{}, nil
		}
	}

	// Update HelmReleases related to this specific ApplicationDefinition
	if err := r.updateHelmReleasesForAppDef(ctx, appDef); err != nil {
		logger.Error(err, "failed to update HelmReleases for ApplicationDefinition", "appDef", appDef.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ApplicationDefinitionHelmReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("applicationdefinition-helm-reconciler").
		Watches(&cozyv1alpha1.ApplicationDefinition{}, handler.EnqueueRequestsFromMapFunc(r.sameKindRequests)).
		Complete(r)
}

// sameKindRequests enqueues every definition declaring the same kind as obj,
// obj included, so that when the owner of a kind is deleted or changes kind the
// next owner reconciles its releases without waiting for a change of its own.
func (r *ApplicationDefinitionHelmReconciler) sameKindRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	reqs := []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName()}}}
	appDef, ok := obj.(*cozyv1alpha1.ApplicationDefinition)
	if !ok || appDef.Spec.Application.Kind == "" {
		return reqs
	}
	defs := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := r.List(ctx, defs); err != nil {
		log.FromContext(ctx).Error(err, "failed to list ApplicationDefinitions for kind fan-out", "kind", appDef.Spec.Application.Kind)
		return reqs
	}
	for i := range defs.Items {
		if defs.Items[i].Name != obj.GetName() && defs.Items[i].Spec.Application.Kind == appDef.Spec.Application.Kind {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: defs.Items[i].Name}})
		}
	}
	return reqs
}

// updateHelmReleasesForAppDef updates all HelmReleases that match the application labels from ApplicationDefinition
func (r *ApplicationDefinitionHelmReconciler) updateHelmReleasesForAppDef(ctx context.Context, appDef *cozyv1alpha1.ApplicationDefinition) error {
	logger := log.FromContext(ctx)

	// Use application labels to find HelmReleases
	// Labels: apps.cozystack.io/application.kind and apps.cozystack.io/application.group
	applicationKind := appDef.Spec.Application.Kind

	// Validate that applicationKind is non-empty
	if applicationKind == "" {
		logger.V(4).Info("Skipping HelmRelease update: Application.Kind is empty", "appDef", appDef.Name)
		return nil
	}

	applicationGroup := "apps.cozystack.io" // All applications use this group

	// Build label selector for HelmReleases
	// Only reconcile HelmReleases with apps.cozystack.io/application.* labels
	labelSelector := client.MatchingLabels{
		"apps.cozystack.io/application.kind":  applicationKind,
		"apps.cozystack.io/application.group": applicationGroup,
	}

	// List all HelmReleases with matching labels
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, labelSelector); err != nil {
		logger.Error(err, "failed to list HelmReleases", "kind", applicationKind, "group", applicationGroup)
		return err
	}

	logger.V(4).Info("Found HelmReleases to update", "appDef", appDef.Name, "kind", applicationKind, "count", len(hrList.Items))

	// Update each HelmRelease
	for i := range hrList.Items {
		hr := &hrList.Items[i]
		if err := r.updateHelmReleaseChart(ctx, hr, appDef); err != nil {
			logger.Error(err, "failed to update HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
	}

	return nil
}

// expectedValuesFrom returns the expected valuesFrom configuration for HelmReleases
func expectedValuesFrom() []helmv2.ValuesReference {
	return []helmv2.ValuesReference{
		{
			Kind: "Secret",
			Name: "cozystack-values",
		},
	}
}

// valuesFromEqual compares two ValuesReference slices
func valuesFromEqual(a, b []helmv2.ValuesReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind ||
			a[i].Name != b[i].Name ||
			a[i].ValuesKey != b[i].ValuesKey ||
			a[i].TargetPath != b[i].TargetPath ||
			a[i].Optional != b[i].Optional {
			return false
		}
	}
	return true
}

// updateHelmReleaseChart updates the chart and valuesFrom in HelmRelease based on ApplicationDefinition
func (r *ApplicationDefinitionHelmReconciler) updateHelmReleaseChart(ctx context.Context, hr *helmv2.HelmRelease, appDef *cozyv1alpha1.ApplicationDefinition) error {
	logger := log.FromContext(ctx)
	hrCopy := hr.DeepCopy()
	updated := false

	// Validate ChartRef configuration exists
	if appDef.Spec.Release.ChartRef == nil ||
		appDef.Spec.Release.ChartRef.Kind == "" ||
		appDef.Spec.Release.ChartRef.Name == "" ||
		appDef.Spec.Release.ChartRef.Namespace == "" {
		logger.Error(fmt.Errorf("invalid ChartRef in ApplicationDefinition"), "Skipping HelmRelease chartRef update: ChartRef is nil or incomplete",
			"appDef", appDef.Name)
		return nil
	}

	// Use ChartRef directly from ApplicationDefinition
	expectedChartRef := appDef.Spec.Release.ChartRef

	// Check if chartRef needs to be updated
	if hrCopy.Spec.ChartRef == nil {
		hrCopy.Spec.ChartRef = expectedChartRef
		// Clear the old chart field when switching to chartRef
		hrCopy.Spec.Chart = nil
		updated = true
	} else if hrCopy.Spec.ChartRef.Kind != expectedChartRef.Kind ||
		hrCopy.Spec.ChartRef.Name != expectedChartRef.Name ||
		hrCopy.Spec.ChartRef.Namespace != expectedChartRef.Namespace {
		hrCopy.Spec.ChartRef = expectedChartRef
		updated = true
	}

	// Check and update valuesFrom configuration
	expected := expectedValuesFrom()
	if !valuesFromEqual(hrCopy.Spec.ValuesFrom, expected) {
		logger.V(4).Info("Updating HelmRelease valuesFrom", "name", hr.Name, "namespace", hr.Namespace)
		hrCopy.Spec.ValuesFrom = expected
		updated = true
	}

	// Check and update the readiness wait. cozystack-api reads
	// release.cozystack.io/helm-install-disable-wait off the definition when it
	// builds a release, and only there, so a release created before the
	// annotation appeared keeps waiting on readiness until its Application is
	// written through the apps API again. Where the wait is what broke, that is
	// every existing instance of the kind at once, each looping upgrade and
	// timeout until an operator rewrites it by hand. The definition is the
	// source of truth in both directions, since the API rebuilds the whole spec
	// on every write and emits DisableWait=false once the annotation is gone.
	disableWait, err := config.ParseHelmInstallDisableWaitAnnotation(
		appDef.Annotations[config.HelmInstallDisableWaitAnnotation],
	)
	if err != nil {
		// cozystack-api refuses to start on this same value, so the cluster
		// already has a loud signal; leave the field as it is rather than guess.
		logger.Error(err, "Skipping HelmRelease wait update: invalid annotation",
			"appDef", appDef.Name, "annotation", config.HelmInstallDisableWaitAnnotation)
	} else {
		// An absent install/upgrade block already behaves as DisableWait=false,
		// so it is only materialised when the definition asks to disable the wait.
		if disableWait && hrCopy.Spec.Install == nil {
			hrCopy.Spec.Install = &helmv2.Install{}
		}
		if hrCopy.Spec.Install != nil && hrCopy.Spec.Install.DisableWait != disableWait {
			logger.V(4).Info("Updating HelmRelease install wait", "name", hr.Name, "namespace", hr.Namespace, "disableWait", disableWait)
			hrCopy.Spec.Install.DisableWait = disableWait
			updated = true
		}
		if disableWait && hrCopy.Spec.Upgrade == nil {
			hrCopy.Spec.Upgrade = &helmv2.Upgrade{}
		}
		if hrCopy.Spec.Upgrade != nil && hrCopy.Spec.Upgrade.DisableWait != disableWait {
			logger.V(4).Info("Updating HelmRelease upgrade wait", "name", hr.Name, "namespace", hr.Namespace, "disableWait", disableWait)
			hrCopy.Spec.Upgrade.DisableWait = disableWait
			updated = true
		}
	}

	// Check and update labels from ApplicationDefinition
	if len(appDef.Spec.Release.Labels) > 0 {
		if hrCopy.Labels == nil {
			hrCopy.Labels = make(map[string]string)
		}
		for key, value := range appDef.Spec.Release.Labels {
			if hrCopy.Labels[key] != value {
				logger.V(4).Info("Updating HelmRelease label", "name", hr.Name, "namespace", hr.Namespace, "label", key, "value", value)
				hrCopy.Labels[key] = value
				updated = true
			}
		}
	}

	if updated {
		logger.V(4).Info("Updating HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
		if err := r.Update(ctx, hrCopy); err != nil {
			return fmt.Errorf("failed to update HelmRelease: %w", err)
		}
	}

	return nil
}
