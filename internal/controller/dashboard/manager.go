package dashboard

import (
	"context"
	"fmt"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	managerpkg "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// Label keys for dashboard resource management
	LabelManagedBy    = "dashboard.cozystack.io/managed-by"
	LabelResourceType = "dashboard.cozystack.io/resource-type"
	LabelCRDName      = "dashboard.cozystack.io/crd-name"
	LabelCRDGroup     = "dashboard.cozystack.io/crd-group"
	LabelCRDVersion   = "dashboard.cozystack.io/crd-version"
	LabelCRDKind      = "dashboard.cozystack.io/crd-kind"
	LabelCRDPlural    = "dashboard.cozystack.io/crd-plural"

	// Label values
	ManagedByValue      = "cozystack-dashboard-controller"
	ResourceTypeStatic  = "static"
	ResourceTypeDynamic = "dynamic"
)

// AddToScheme exposes dashboard types registration for controller setup.
func AddToScheme(s *runtime.Scheme) error {
	return dashv1alpha1.AddToScheme(s)
}

// Manager owns logic for creating/updating dashboard resources derived from CRDs.
// It’s easy to extend: add new ensure* methods and wire them into EnsureForCRD.
type Manager struct {
	client.Client
	Scheme *runtime.Scheme
}

// NewManager constructs a dashboard Manager.
func NewManager(c client.Client, scheme *runtime.Scheme) *Manager {
	m := &Manager{Client: c, Scheme: scheme}
	return m
}

func (m *Manager) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("dashboard-reconciler").
		For(&cozyv1alpha1.CozystackResourceDefinition{}).
		Complete(m); err != nil {
		return err
	}

	return mgr.Add(managerpkg.RunnableFunc(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("dashboard static resources cache sync failed")
		}
		return m.ensureStaticResources(ctx)
	}))
}

func (m *Manager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	crd := &cozyv1alpha1.CozystackResourceDefinition{}

	err := m.Get(ctx, types.NamespacedName{Name: req.Name}, crd)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := m.CleanupOrphanedResources(ctx); err != nil {
				l.Error(err, "Failed to cleanup orphaned dashboard resources")
			}
			return ctrl.Result{}, nil // no point in requeuing here
		}
		return ctrl.Result{}, err
	}

	return m.EnsureForCRD(ctx, crd)
}

// EnsureForCRD is the single entry-point used by the controller.
// Add more ensure* calls here as you implement support for other resources:
//
//   - ensureBreadcrumb            (implemented)
//   - ensureCustomColumnsOverride (implemented)
//   - ensureCustomFormsOverride   (implemented)
//   - ensureCustomFormsPrefill    (implemented)
//   - ensureFactory
//   - ensureMarketplacePanel      (implemented)
//   - ensureSidebar               (implemented)
//   - ensureTableUriMapping 	     (implemented)
func (m *Manager) EnsureForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	// Early return if crd.Spec.Dashboard is nil to prevent oscillation
	if crd.Spec.Dashboard == nil {
		return reconcile.Result{}, nil
	}

	// MarketplacePanel
	if res, err := m.ensureMarketplacePanel(ctx, crd); err != nil || res.Requeue || res.RequeueAfter > 0 {
		return res, err
	}

	// CustomFormsPrefill
	if res, err := m.ensureCustomFormsPrefill(ctx, crd); err != nil || res.Requeue || res.RequeueAfter > 0 {
		return res, err
	}

	// CustomColumnsOverride
	if _, err := m.ensureCustomColumnsOverride(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	if err := m.ensureTableUriMapping(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	if err := m.ensureBreadcrumb(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	if err := m.ensureCustomFormsOverride(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	if err := m.ensureSidebar(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}

	if err := m.ensureFactory(ctx, crd); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// InitializeStaticResources creates all static dashboard resources once during controller startup
func (m *Manager) InitializeStaticResources(ctx context.Context) error {
	return m.ensureStaticResources(ctx)
}

// addDashboardLabels adds standard dashboard management labels to a resource
func (m *Manager) addDashboardLabels(obj client.Object, crd *cozyv1alpha1.CozystackResourceDefinition, resourceType string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	labels[LabelManagedBy] = ManagedByValue
	labels[LabelResourceType] = resourceType

	if crd != nil {
		g, v, kind := pickGVK(crd)
		plural := pickPlural(kind, crd)

		labels[LabelCRDName] = crd.Name
		labels[LabelCRDGroup] = g
		labels[LabelCRDVersion] = v
		labels[LabelCRDKind] = kind
		labels[LabelCRDPlural] = plural
	}

	obj.SetLabels(labels)
}

// getDashboardResourceSelector returns a label selector for dashboard-managed resources
func (m *Manager) getDashboardResourceSelector() client.MatchingLabels {
	return client.MatchingLabels{
		LabelManagedBy: ManagedByValue,
	}
}

// getDynamicResourceSelector returns a label selector for dynamic dashboard resources
func (m *Manager) getDynamicResourceSelector() client.MatchingLabels {
	return client.MatchingLabels{
		LabelManagedBy:    ManagedByValue,
		LabelResourceType: ResourceTypeDynamic,
	}
}

// getStaticResourceSelector returns a label selector for static dashboard resources
func (m *Manager) getStaticResourceSelector() client.MatchingLabels {
	return client.MatchingLabels{
		LabelManagedBy:    ManagedByValue,
		LabelResourceType: ResourceTypeStatic,
	}
}

// CleanupOrphanedResources removes dashboard resources that are no longer needed
// This should be called after cache warming to ensure all current resources are known
func (m *Manager) CleanupOrphanedResources(ctx context.Context) error {
	var crdList cozyv1alpha1.CozystackResourceDefinitionList
	if err := m.List(ctx, &crdList, &client.ListOptions{}); err != nil {
		return err
	}
	allCRDs := crdList.Items

	// Build a set of expected resource names for each type
	expectedResources := m.buildExpectedResourceSet(allCRDs)

	// Clean up each resource type
	resourceTypes := []client.Object{
		&dashv1alpha1.CustomColumnsOverride{},
		&dashv1alpha1.CustomFormsOverride{},
		&dashv1alpha1.CustomFormsPrefill{},
		&dashv1alpha1.MarketplacePanel{},
		&dashv1alpha1.Sidebar{},
		&dashv1alpha1.TableUriMapping{},
		&dashv1alpha1.Breadcrumb{},
		&dashv1alpha1.Factory{},
	}

	for _, resourceType := range resourceTypes {
		if err := m.cleanupResourceType(ctx, resourceType, expectedResources); err != nil {
			return err
		}
	}

	return nil
}

// buildExpectedResourceSet creates a map of expected resource names by type
func (m *Manager) buildExpectedResourceSet(crds []cozyv1alpha1.CozystackResourceDefinition) map[string]map[string]bool {
	expected := make(map[string]map[string]bool)

	// Initialize maps for each resource type
	resourceTypes := []string{
		"CustomColumnsOverride",
		"CustomFormsOverride",
		"CustomFormsPrefill",
		"MarketplacePanel",
		"Sidebar",
		"TableUriMapping",
		"Breadcrumb",
		"Factory",
	}

	for _, rt := range resourceTypes {
		expected[rt] = make(map[string]bool)
	}

	// Add static resources (these should always exist)
	staticResources := CreateAllStaticResources()
	for _, resource := range staticResources {
		resourceType := resource.GetObjectKind().GroupVersionKind().Kind
		if expected[resourceType] != nil {
			expected[resourceType][resource.GetName()] = true
		}
	}

	// Add dynamic resources based on current CRDs
	for _, crd := range crds {
		if crd.Spec.Dashboard == nil {
			continue
		}

		// Note: We include ALL resources with dashboard config, regardless of module flag
		// because ensureFactory and ensureBreadcrumb create resources for all CRDs with dashboard config

		g, v, kind := pickGVK(&crd)
		plural := pickPlural(kind, &crd)

		// CustomColumnsOverride - created for ALL CRDs with dashboard config
		name := fmt.Sprintf("stock-namespace-%s.%s.%s", g, v, plural)
		expected["CustomColumnsOverride"][name] = true

		// CustomFormsOverride - created for ALL CRDs with dashboard config
		name = fmt.Sprintf("%s.%s.%s", g, v, plural)
		expected["CustomFormsOverride"][name] = true

		// CustomFormsPrefill - created for ALL CRDs with dashboard config
		expected["CustomFormsPrefill"][name] = true

		// MarketplacePanel - only created for non-module CRDs
		if !crd.Spec.Dashboard.Module {
			expected["MarketplacePanel"][crd.Name] = true
		}

		// Sidebar resources - created for ALL CRDs with dashboard config
		lowerKind := strings.ToLower(kind)
		detailsID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)
		expected["Sidebar"][detailsID] = true

		// Add other stock sidebars that are created for each CRD
		stockSidebars := []string{
			"stock-instance-api-form",
			"stock-instance-api-table",
			"stock-instance-builtin-form",
			"stock-instance-builtin-table",
			"stock-project-factory-marketplace",
			"stock-project-factory-workloadmonitor-details",
			"stock-project-api-form",
			"stock-project-api-table",
			"stock-project-builtin-form",
			"stock-project-builtin-table",
			"stock-project-crd-form",
			"stock-project-crd-table",
		}
		for _, sidebarID := range stockSidebars {
			expected["Sidebar"][sidebarID] = true
		}

		// TableUriMapping - created for ALL CRDs with dashboard config
		name = fmt.Sprintf("stock-namespace-%s.%s.%s", g, v, plural)
		expected["TableUriMapping"][name] = true

		// Breadcrumb - created for ALL CRDs with dashboard config
		detailID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)
		expected["Breadcrumb"][detailID] = true

		// Factory - created for ALL CRDs with dashboard config
		factoryName := fmt.Sprintf("%s-details", lowerKind)
		expected["Factory"][factoryName] = true
	}

	return expected
}

// cleanupResourceType removes orphaned resources of a specific type
func (m *Manager) cleanupResourceType(ctx context.Context, resourceType client.Object, expectedResources map[string]map[string]bool) error {
	var (
		list         client.ObjectList
		resourceKind string
	)
	switch resourceType.(type) {
	case *dashv1alpha1.CustomColumnsOverride:
		list = &dashv1alpha1.CustomColumnsOverrideList{}
		resourceKind = "CustomColumnsOverride"
	case *dashv1alpha1.CustomFormsOverride:
		list = &dashv1alpha1.CustomFormsOverrideList{}
		resourceKind = "CustomFormsOverride"
	case *dashv1alpha1.CustomFormsPrefill:
		list = &dashv1alpha1.CustomFormsPrefillList{}
		resourceKind = "CustomFormsPrefill"
	case *dashv1alpha1.MarketplacePanel:
		list = &dashv1alpha1.MarketplacePanelList{}
		resourceKind = "MarketplacePanel"
	case *dashv1alpha1.Sidebar:
		list = &dashv1alpha1.SidebarList{}
		resourceKind = "Sidebar"
	case *dashv1alpha1.TableUriMapping:
		list = &dashv1alpha1.TableUriMappingList{}
		resourceKind = "TableUriMapping"
	case *dashv1alpha1.Breadcrumb:
		list = &dashv1alpha1.BreadcrumbList{}
		resourceKind = "Breadcrumb"
	case *dashv1alpha1.Factory:
		list = &dashv1alpha1.FactoryList{}
		resourceKind = "Factory"
	default:
		return nil // Unknown type
	}

	expected := expectedResources[resourceKind]
	if expected == nil {
		return nil // No expected resources for this type
	}

	// List with dashboard labels
	if err := m.List(ctx, list, m.getDashboardResourceSelector()); err != nil {
		return err
	}

	// Delete resources that are not in the expected set
	switch l := list.(type) {
	case *dashv1alpha1.CustomColumnsOverrideList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.CustomFormsOverrideList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.CustomFormsPrefillList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.MarketplacePanelList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.SidebarList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.TableUriMappingList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					// Resource already deleted, continue
				}
			}
		}
	case *dashv1alpha1.BreadcrumbList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				logger := log.FromContext(ctx)
				logger.Info("Deleting orphaned Breadcrumb resource", "name", item.Name)
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
				}
			}
		}
	case *dashv1alpha1.FactoryList:
		for _, item := range l.Items {
			if !expected[item.Name] {
				logger := log.FromContext(ctx)
				logger.Info("Deleting orphaned Factory resource", "name", item.Name)
				if err := m.Delete(ctx, &item); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
				}
			}
		}
	}

	return nil
}
