package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// WorkloadMonitorFromCRDReconciler reconciles HelmReleases and creates WorkloadMonitors
// based on CozystackResourceDefinition templates
type WorkloadMonitorFromCRDReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=workloadmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch

const (
	WorkloadMonitorOwnerLabel  = "workloadmonitor.cozystack.io/owned-by-crd"
	WorkloadMonitorSourceLabel = "workloadmonitor.cozystack.io/helm-release"
)

// Reconcile processes HelmRelease resources and creates corresponding WorkloadMonitors
func (r *WorkloadMonitorFromCRDReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the HelmRelease
	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		if errors.IsNotFound(err) {
			// HelmRelease deleted - cleanup will be handled by owner references
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch HelmRelease")
		return ctrl.Result{}, err
	}

	// Skip system HelmReleases
	if strings.HasPrefix(hr.Name, "tenant-") {
		return ctrl.Result{}, nil
	}

	// Find the matching CozystackResourceDefinition
	crd, err := r.findCRDForHelmRelease(ctx, hr)
	if err != nil {
		if errors.IsNotFound(err) {
			// No CRD found for this HelmRelease - skip
			logger.V(1).Info("No CozystackResourceDefinition found for HelmRelease", "name", hr.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to find CozystackResourceDefinition")
		return ctrl.Result{}, err
	}

	// If CRD doesn't have WorkloadMonitors, cleanup any existing ones we created
	if len(crd.Spec.WorkloadMonitors) == 0 {
		if err := r.cleanupWorkloadMonitors(ctx, hr); err != nil {
			logger.Error(err, "failed to cleanup WorkloadMonitors")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Get the HelmRelease values for template rendering
	values, err := r.getHelmReleaseValues(ctx, hr)
	if err != nil {
		logger.Error(err, "unable to get HelmRelease values")
		return ctrl.Result{}, err
	}

	// Create/update WorkloadMonitors based on templates
	if err := r.reconcileWorkloadMonitors(ctx, hr, crd, values); err != nil {
		logger.Error(err, "failed to reconcile WorkloadMonitors")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findCRDForHelmRelease finds the CozystackResourceDefinition for a given HelmRelease
func (r *WorkloadMonitorFromCRDReconciler) findCRDForHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) (*cozyv1alpha1.CozystackResourceDefinition, error) {
	// List all CozystackResourceDefinitions
	var crdList cozyv1alpha1.CozystackResourceDefinitionList
	if err := r.List(ctx, &crdList); err != nil {
		return nil, err
	}

	// Match by chart name and prefix
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		if crd.Spec.Release.Chart.Name == hr.Spec.Chart.Spec.Chart {
			// Check if HelmRelease name matches the prefix
			if strings.HasPrefix(hr.Name, crd.Spec.Release.Prefix) {
				return crd, nil
			}
		}
	}

	return nil, errors.NewNotFound(schema.GroupResource{Group: "cozystack.io", Resource: "cozystackresourcedefinitions"}, "")
}

// getHelmReleaseValues extracts the values from HelmRelease spec
func (r *WorkloadMonitorFromCRDReconciler) getHelmReleaseValues(ctx context.Context, hr *helmv2.HelmRelease) (map[string]interface{}, error) {
	if hr.Spec.Values == nil {
		return make(map[string]interface{}), nil
	}

	// Convert apiextensionsv1.JSON to map
	values := make(map[string]interface{})
	if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
		return nil, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	return values, nil
}

// reconcileWorkloadMonitors creates or updates WorkloadMonitors based on CRD templates
func (r *WorkloadMonitorFromCRDReconciler) reconcileWorkloadMonitors(
	ctx context.Context,
	hr *helmv2.HelmRelease,
	crd *cozyv1alpha1.CozystackResourceDefinition,
	values map[string]interface{},
) error {
	logger := log.FromContext(ctx)

	// Get chart version from HelmRelease
	chartVersion := ""
	if hr.Status.History != nil && len(hr.Status.History) > 0 {
		chartVersion = hr.Status.History[0].ChartVersion
	}

	// Template context
	templateData := map[string]interface{}{
		"Release": map[string]interface{}{
			"Name":      hr.Name,
			"Namespace": hr.Namespace,
		},
		"Chart": map[string]interface{}{
			"Version": chartVersion,
		},
		"Values": values,
	}

	// Track which monitors we should have
	expectedMonitors := make(map[string]bool)

	// Process each WorkloadMonitor template
	for _, tmpl := range crd.Spec.WorkloadMonitors {
		// Check condition
		if tmpl.Condition != "" {
			shouldCreate, err := evaluateCondition(tmpl.Condition, templateData)
			if err != nil {
				logger.Error(err, "failed to evaluate condition", "template", tmpl.Name, "condition", tmpl.Condition)
				continue
			}
			if !shouldCreate {
				logger.V(1).Info("Skipping WorkloadMonitor due to condition", "template", tmpl.Name)
				continue
			}
		}

		// Render monitor name
		monitorName, err := renderTemplate(tmpl.Name, templateData)
		if err != nil {
			logger.Error(err, "failed to render monitor name", "template", tmpl.Name)
			continue
		}

		expectedMonitors[monitorName] = true

		// Render selector values
		selector := make(map[string]string)
		for key, valueTmpl := range tmpl.Selector {
			renderedValue, err := renderTemplate(valueTmpl, templateData)
			if err != nil {
				logger.Error(err, "failed to render selector value", "key", key, "template", valueTmpl)
				continue
			}
			selector[key] = renderedValue
		}

		// Render replicas
		var replicas *int32
		if tmpl.Replicas != "" {
			replicasStr, err := renderTemplate(tmpl.Replicas, templateData)
			if err != nil {
				logger.Error(err, "failed to render replicas", "template", tmpl.Replicas)
			} else {
				if replicasInt, err := strconv.ParseInt(replicasStr, 10, 32); err == nil {
					replicas = pointer.Int32(int32(replicasInt))
				}
			}
		}

		// Render minReplicas
		var minReplicas *int32
		if tmpl.MinReplicas != "" {
			minReplicasStr, err := renderTemplate(tmpl.MinReplicas, templateData)
			if err != nil {
				logger.Error(err, "failed to render minReplicas", "template", tmpl.MinReplicas)
			} else {
				if minReplicasInt, err := strconv.ParseInt(minReplicasStr, 10, 32); err == nil {
					minReplicas = pointer.Int32(int32(minReplicasInt))
				}
			}
		}

		// Create or update WorkloadMonitor
		monitor := &cozyv1alpha1.WorkloadMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      monitorName,
				Namespace: hr.Namespace,
			},
		}

		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, monitor, func() error {
			// Set labels
			if monitor.Labels == nil {
				monitor.Labels = make(map[string]string)
			}
			monitor.Labels[WorkloadMonitorOwnerLabel] = "true"
			monitor.Labels[WorkloadMonitorSourceLabel] = hr.Name

			// Set owner reference to HelmRelease for automatic cleanup
			if err := controllerutil.SetControllerReference(hr, monitor, r.Scheme); err != nil {
				return err
			}

			// Update spec
			monitor.Spec.Selector = selector
			monitor.Spec.Kind = tmpl.Kind
			monitor.Spec.Type = tmpl.Type
			monitor.Spec.Version = chartVersion
			monitor.Spec.Replicas = replicas
			monitor.Spec.MinReplicas = minReplicas

			return nil
		})

		if err != nil {
			logger.Error(err, "failed to create/update WorkloadMonitor", "name", monitorName)
			continue
		}

		logger.V(1).Info("WorkloadMonitor reconciled", "name", monitorName)
	}

	// Cleanup WorkloadMonitors that are no longer in templates
	if err := r.cleanupUnexpectedMonitors(ctx, hr, expectedMonitors); err != nil {
		logger.Error(err, "failed to cleanup unexpected WorkloadMonitors")
		return err
	}

	return nil
}

// cleanupWorkloadMonitors removes all WorkloadMonitors created for a HelmRelease
func (r *WorkloadMonitorFromCRDReconciler) cleanupWorkloadMonitors(ctx context.Context, hr *helmv2.HelmRelease) error {
	return r.cleanupUnexpectedMonitors(ctx, hr, make(map[string]bool))
}

// cleanupUnexpectedMonitors removes WorkloadMonitors that are no longer expected
func (r *WorkloadMonitorFromCRDReconciler) cleanupUnexpectedMonitors(
	ctx context.Context,
	hr *helmv2.HelmRelease,
	expectedMonitors map[string]bool,
) error {
	logger := log.FromContext(ctx)

	// List all WorkloadMonitors in the namespace that we created
	var monitorList cozyv1alpha1.WorkloadMonitorList
	labelSelector := labels.SelectorFromSet(labels.Set{
		WorkloadMonitorOwnerLabel:  "true",
		WorkloadMonitorSourceLabel: hr.Name,
	})
	if err := r.List(ctx, &monitorList,
		client.InNamespace(hr.Namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	); err != nil {
		return err
	}

	// Delete monitors that are not expected
	for i := range monitorList.Items {
		monitor := &monitorList.Items[i]
		if !expectedMonitors[monitor.Name] {
			logger.Info("Deleting unexpected WorkloadMonitor", "name", monitor.Name)
			if err := r.Delete(ctx, monitor); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "failed to delete WorkloadMonitor", "name", monitor.Name)
			}
		}
	}

	return nil
}

// renderTemplate renders a Go template string with the given data
func renderTemplate(tmplStr string, data interface{}) (string, error) {
	// Check if it's already a simple value (no template markers)
	if !strings.Contains(tmplStr, "{{") {
		return tmplStr, nil
	}

	// Add Sprig functions for compatibility with Helm templates
	tmpl, err := template.New("").Funcs(getTemplateFuncs()).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

// evaluateCondition evaluates a template condition (should return "true" or non-empty for true)
func evaluateCondition(condition string, data interface{}) (bool, error) {
	result, err := renderTemplate(condition, data)
	if err != nil {
		return false, err
	}

	// Check for truthy values
	result = strings.TrimSpace(strings.ToLower(result))
	return result == "true" || result == "1" || result == "yes", nil
}

// getTemplateFuncs returns template functions compatible with Helm
func getTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		// Math functions
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"mul": func(a, b int) int { return a * b },
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"add1": func(a int) int { return a + 1 },
		"sub1": func(a int) int { return a - 1 },

		// String functions
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"trim":    strings.TrimSpace,
		"trimAll": func(cutset, s string) string { return strings.Trim(s, cutset) },
		"replace": func(old, new string, n int, s string) string { return strings.Replace(s, old, new, n) },

		// Logic functions
		"default": func(defaultVal, val interface{}) interface{} {
			if val == nil || val == "" {
				return defaultVal
			}
			return val
		},
		"empty": func(val interface{}) bool {
			return val == nil || val == ""
		},
		"not": func(val bool) bool {
			return !val
		},
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *WorkloadMonitorFromCRDReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("workloadmonitor-from-crd-controller").
		For(&helmv2.HelmRelease{}).
		Owns(&cozyv1alpha1.WorkloadMonitor{}).
		Watches(
			&cozyv1alpha1.CozystackResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.mapCRDToHelmReleases),
		).
		Complete(r)
}

// mapCRDToHelmReleases maps CRD changes to HelmRelease reconcile requests
func (r *WorkloadMonitorFromCRDReconciler) mapCRDToHelmReleases(ctx context.Context, obj client.Object) []reconcile.Request {
	crd, ok := obj.(*cozyv1alpha1.CozystackResourceDefinition)
	if !ok {
		return nil
	}

	// List all HelmReleases
	var hrList helmv2.HelmReleaseList
	if err := r.List(ctx, &hrList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range hrList.Items {
		hr := &hrList.Items[i]
		// Skip tenant HelmReleases
		if strings.HasPrefix(hr.Name, "tenant-") {
			continue
		}
		// Match by chart name and prefix
		if crd.Spec.Release.Chart.Name == hr.Spec.Chart.Spec.Chart {
			if strings.HasPrefix(hr.Name, crd.Spec.Release.Prefix) {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      hr.Name,
						Namespace: hr.Namespace,
					},
				})
			}
		}
	}

	return requests
}
