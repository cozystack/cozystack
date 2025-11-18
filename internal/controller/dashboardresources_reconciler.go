package controller

import (
	"context"
	"fmt"
	"strings"
	"text/template"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// DashboardResourcesReconciler reconciles HelmReleases and creates Role/RoleBinding
// for dashboard access based on CozystackResourceDefinition
type DashboardResourcesReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

const (
	DashboardResourcesRoleName         = "-dashboard-resources"
	DashboardResourcesOwnerLabel       = "dashboardresources.cozystack.io/owned-by-crd"
	DashboardResourcesHelmReleaseLabel = "dashboardresources.cozystack.io/helm-release"
)

// Reconcile processes HelmRelease resources and creates corresponding Role/RoleBinding
func (r *DashboardResourcesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// Skip tenant HelmReleases
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

	// Check if we need to create dashboard resources
	if !r.shouldCreateDashboardResources(crd) {
		// Cleanup any existing resources we created
		if err := r.cleanupDashboardResources(ctx, hr); err != nil {
			logger.Error(err, "failed to cleanup dashboard resources")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Create/update Role and RoleBinding
	if err := r.reconcileDashboardResources(ctx, hr, crd); err != nil {
		logger.Error(err, "failed to reconcile dashboard resources")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findCRDForHelmRelease finds the CozystackResourceDefinition for a given HelmRelease
func (r *DashboardResourcesReconciler) findCRDForHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) (*cozyv1alpha1.CozystackResourceDefinition, error) {
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

// shouldCreateDashboardResources checks if we should create dashboard resources
func (r *DashboardResourcesReconciler) shouldCreateDashboardResources(crd *cozyv1alpha1.CozystackResourceDefinition) bool {
	// Create if we have any resources defined (secrets, services, or ingresses)
	return len(crd.Spec.Secrets.Include) > 0 ||
		len(crd.Spec.Services.Include) > 0 ||
		len(crd.Spec.Ingresses.Include) > 0
}

// reconcileDashboardResources creates or updates Role and RoleBinding
func (r *DashboardResourcesReconciler) reconcileDashboardResources(
	ctx context.Context,
	hr *helmv2.HelmRelease,
	crd *cozyv1alpha1.CozystackResourceDefinition,
) error {
	logger := log.FromContext(ctx)

	// Template data for rendering resource names
	templateData := map[string]interface{}{
		"name":      strings.TrimPrefix(hr.Name, crd.Spec.Release.Prefix),
		"kind":      strings.ToLower(crd.Spec.Application.Kind),
		"namespace": hr.Namespace,
	}

	// Build policy rules
	rules := []rbacv1.PolicyRule{}

	// Add secrets rules
	secretNames, err := r.renderResourceNames(crd.Spec.Secrets.Include, templateData)
	if err != nil {
		logger.Error(err, "failed to render secret names")
	} else if len(secretNames) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: secretNames,
			Verbs:         []string{"get", "list", "watch"},
		})
	}

	// Add services rules
	serviceNames, err := r.renderResourceNames(crd.Spec.Services.Include, templateData)
	if err != nil {
		logger.Error(err, "failed to render service names")
	} else if len(serviceNames) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{""},
			Resources:     []string{"services"},
			ResourceNames: serviceNames,
			Verbs:         []string{"get", "list", "watch"},
		})
	}

	// Add ingresses rules
	ingressNames, err := r.renderResourceNames(crd.Spec.Ingresses.Include, templateData)
	if err != nil {
		logger.Error(err, "failed to render ingress names")
	} else if len(ingressNames) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{"networking.k8s.io"},
			Resources:     []string{"ingresses"},
			ResourceNames: ingressNames,
			Verbs:         []string{"get", "list", "watch"},
		})
	}

	// Add WorkloadMonitors rule (always include for the release)
	rules = append(rules, rbacv1.PolicyRule{
		APIGroups:     []string{"cozystack.io"},
		Resources:     []string{"workloadmonitors"},
		ResourceNames: []string{hr.Name},
		Verbs:         []string{"get", "list", "watch"},
	})

	// Create or update Role
	roleName := hr.Name + DashboardResourcesRoleName
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: hr.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		// Set labels
		if role.Labels == nil {
			role.Labels = make(map[string]string)
		}
		role.Labels[DashboardResourcesOwnerLabel] = "true"
		role.Labels[DashboardResourcesHelmReleaseLabel] = hr.Name

		// Set owner reference to HelmRelease for automatic cleanup
		if err := controllerutil.SetControllerReference(hr, role, r.Scheme); err != nil {
			return err
		}

		// Update rules
		role.Rules = rules

		return nil
	})

	if err != nil {
		logger.Error(err, "failed to create/update Role", "name", roleName)
		return err
	}

	// Create or update RoleBinding
	roleBindingName := hr.Name + DashboardResourcesRoleName
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: hr.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, roleBinding, func() error {
		// Set labels
		if roleBinding.Labels == nil {
			roleBinding.Labels = make(map[string]string)
		}
		roleBinding.Labels[DashboardResourcesOwnerLabel] = "true"
		roleBinding.Labels[DashboardResourcesHelmReleaseLabel] = hr.Name

		// Set owner reference to HelmRelease for automatic cleanup
		if err := controllerutil.SetControllerReference(hr, roleBinding, r.Scheme); err != nil {
			return err
		}

		// Update subjects - generate based on tenant namespace
		roleBinding.Subjects = r.generateSubjects(hr.Namespace)

		// Update role reference
		roleBinding.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		}

		return nil
	})

	if err != nil {
		logger.Error(err, "failed to create/update RoleBinding", "name", roleBindingName)
		return err
	}

	logger.V(1).Info("Dashboard resources reconciled", "role", roleName, "roleBinding", roleBindingName)
	return nil
}

// renderResourceNames renders resource names from selectors
func (r *DashboardResourcesReconciler) renderResourceNames(
	selectors []*cozyv1alpha1.CozystackResourceDefinitionResourceSelector,
	templateData map[string]interface{},
) ([]string, error) {
	var names []string
	seen := make(map[string]bool)

	for _, selector := range selectors {
		if selector == nil {
			continue
		}
		for _, nameTemplate := range selector.ResourceNames {
			// Render the template
			rendered, err := renderTemplate(nameTemplate, templateData)
			if err != nil {
				return nil, fmt.Errorf("failed to render template %q: %w", nameTemplate, err)
			}
			// Add only unique names
			if !seen[rendered] {
				names = append(names, rendered)
				seen[rendered] = true
			}
		}
	}

	return names, nil
}

// generateSubjects generates RBAC subjects for a tenant namespace
// This mimics the behavior of cozy-lib.rbac.subjectsForTenantAndAccessLevel
func (r *DashboardResourcesReconciler) generateSubjects(namespace string) []rbacv1.Subject {
	// Get all parent tenants and this tenant
	tenants := r.getAllParentTenantsAndThis(namespace)

	// Access levels at or above "use"
	accessLevels := []string{"use", "admin", "super-admin"}

	var subjects []rbacv1.Subject

	for _, tenant := range tenants {
		// Add ServiceAccount subject
		subjects = append(subjects, rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      tenant,
			Namespace: tenant,
		})

		// Add Group subjects for each access level
		for _, level := range accessLevels {
			subjects = append(subjects, rbacv1.Subject{
				Kind:     "Group",
				Name:     fmt.Sprintf("%s-%s", tenant, level),
				APIGroup: "rbac.authorization.k8s.io",
			})
		}
	}

	return subjects
}

// getAllParentTenantsAndThis returns all parent tenants and the current tenant
func (r *DashboardResourcesReconciler) getAllParentTenantsAndThis(namespace string) []string {
	if !strings.HasPrefix(namespace, "tenant-") {
		return []string{}
	}

	parts := strings.Split(namespace, "-")
	var tenants []string

	// Build all parent tenant names
	for i := 2; i <= len(parts); i++ {
		tenant := strings.Join(parts[:i], "-")
		tenants = append(tenants, tenant)
	}

	// Always include tenant-root if not already present
	if namespace != "tenant-root" {
		found := false
		for _, t := range tenants {
			if t == "tenant-root" {
				found = true
				break
			}
		}
		if !found {
			tenants = append(tenants, "tenant-root")
		}
	}

	return tenants
}

// cleanupDashboardResources removes Role and RoleBinding created for a HelmRelease
func (r *DashboardResourcesReconciler) cleanupDashboardResources(ctx context.Context, hr *helmv2.HelmRelease) error {
	logger := log.FromContext(ctx)

	roleName := hr.Name + DashboardResourcesRoleName

	// Delete Role
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: hr.Namespace,
		},
	}
	if err := r.Delete(ctx, role); err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "failed to delete Role", "name", roleName)
	}

	// Delete RoleBinding
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: hr.Namespace,
		},
	}
	if err := r.Delete(ctx, roleBinding); err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "failed to delete RoleBinding", "name", roleName)
	}

	return nil
}

// renderTemplate renders a Go template string with the given data
// Reusing the same function from workloadmonitor_reconciler.go
func renderTemplate(tmplStr string, data interface{}) (string, error) {
	// Check if it's already a simple value (no template markers)
	if !strings.Contains(tmplStr, "{{") {
		return tmplStr, nil
	}

	// Add basic template functions
	funcMap := template.FuncMap{
		"slice": func(s string, start int) string {
			if start >= len(s) {
				return ""
			}
			return s[start:]
		},
	}

	tmpl, err := template.New("").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

// SetupWithManager sets up the controller with the Manager
func (r *DashboardResourcesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("dashboardresources-controller").
		For(&helmv2.HelmRelease{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&cozyv1alpha1.CozystackResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.mapCRDToHelmReleases),
		).
		Complete(r)
}

// mapCRDToHelmReleases maps CRD changes to HelmRelease reconcile requests
func (r *DashboardResourcesReconciler) mapCRDToHelmReleases(ctx context.Context, obj client.Object) []reconcile.Request {
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
