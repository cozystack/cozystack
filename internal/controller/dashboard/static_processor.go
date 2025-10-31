package dashboard

import (
	"context"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureStaticResources ensures all static dashboard resources are created
func (m *Manager) ensureStaticResources(ctx context.Context) error {
	// Use refactored resources from static_refactored.go
	// This replaces the old static variables with dynamic creation using helper functions
	staticResources := CreateAllStaticResources()

	// Create or update each static resource
	for _, resource := range staticResources {
		if err := m.ensureStaticResource(ctx, resource); err != nil {
			return err
		}
	}

	return nil
}

// ensureStaticResource creates or updates a single static resource
func (m *Manager) ensureStaticResource(ctx context.Context, obj client.Object) error {
	// Create a copy to avoid modifying the original
	resource := obj.DeepCopyObject().(client.Object)

	// Add dashboard labels to static resources
	m.addDashboardLabels(resource, nil, ResourceTypeStatic)

	_, err := controllerutil.CreateOrUpdate(ctx, m.Client, resource, func() error {
		// For static resources, we don't need to set owner references
		// as they are meant to be persistent across CRD changes
		// Copy Spec from the original object to the live object
		switch o := obj.(type) {
		case *dashv1alpha1.CustomColumnsOverride:
			resource.(*dashv1alpha1.CustomColumnsOverride).Spec = o.Spec
		case *dashv1alpha1.Breadcrumb:
			resource.(*dashv1alpha1.Breadcrumb).Spec = o.Spec
		case *dashv1alpha1.CustomFormsOverride:
			resource.(*dashv1alpha1.CustomFormsOverride).Spec = o.Spec
		case *dashv1alpha1.Factory:
			resource.(*dashv1alpha1.Factory).Spec = o.Spec
		case *dashv1alpha1.Navigation:
			resource.(*dashv1alpha1.Navigation).Spec = o.Spec
		case *dashv1alpha1.TableUriMapping:
			resource.(*dashv1alpha1.TableUriMapping).Spec = o.Spec
		}
		// Ensure labels are always set
		m.addDashboardLabels(resource, nil, ResourceTypeStatic)
		return nil
	})

	return err
}
