package dashboard

import (
	"context"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// ensureTableUriMapping creates or updates a TableUriMapping resource for the given CRD
func (m *Manager) ensureTableUriMapping(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	// Links are fully managed by the CustomColumnsOverride.
	return nil
}
