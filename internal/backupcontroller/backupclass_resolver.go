package backupcontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const (
	// DefaultApplicationAPIGroup is the default API group for applications
	// when not specified in ApplicationRef or ApplicationSelector.
	// Deprecated: Use backupsv1alpha1.DefaultApplicationAPIGroup instead.
	DefaultApplicationAPIGroup = backupsv1alpha1.DefaultApplicationAPIGroup
)

// NormalizeApplicationRef sets the default apiGroup to DefaultApplicationAPIGroup if it's not specified.
// Deprecated: Use backupsv1alpha1.NormalizeApplicationRef instead.
func NormalizeApplicationRef(ref corev1.TypedLocalObjectReference) corev1.TypedLocalObjectReference {
	return backupsv1alpha1.NormalizeApplicationRef(ref)
}

// ResolvedBackupConfig contains the resolved strategy and storage configuration
// from a BackupClass.
type ResolvedBackupConfig struct {
	StrategyRef backupsv1alpha1.TypedClusterObjectReference
	Parameters  map[string]string
}

// ResolveBackupClass resolves a BackupClass and finds the matching strategy for the given application.
// It normalizes the applicationRef's apiGroup (defaults to apps.cozystack.io if not specified)
// and matches it against the strategies in the BackupClass.
func ResolveBackupClass(
	ctx context.Context,
	c client.Client,
	backupClassName string,
	applicationRef corev1.TypedLocalObjectReference,
) (*ResolvedBackupConfig, error) {
	// Normalize applicationRef (default apiGroup if not specified)
	applicationRef = NormalizeApplicationRef(applicationRef)

	// Get BackupClass
	backupClass := &backupsv1alpha1.BackupClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: backupClassName}, backupClass); err != nil {
		return nil, fmt.Errorf("failed to get BackupClass %s: %w", backupClassName, err)
	}

	// Determine application API group (already normalized, but extract for matching)
	appAPIGroup := backupsv1alpha1.DefaultApplicationAPIGroup
	if applicationRef.APIGroup != nil {
		appAPIGroup = *applicationRef.APIGroup
	}

	// Find matching strategy
	for _, strategy := range backupClass.Spec.Strategies {
		// Normalize strategy's application selector (default apiGroup if not specified)
		strategyAPIGroup := backupsv1alpha1.DefaultApplicationAPIGroup
		if strategy.Application.APIGroup != nil && *strategy.Application.APIGroup != "" {
			strategyAPIGroup = *strategy.Application.APIGroup
		}

		if strategyAPIGroup == appAPIGroup && strategy.Application.Kind == applicationRef.Kind {
			return &ResolvedBackupConfig{
				StrategyRef: strategy.StrategyRef,
				Parameters:  strategy.Parameters,
			}, nil
		}
	}

	return nil, fmt.Errorf("no matching strategy found in BackupClass %s for application %s/%s",
		backupClassName, appAPIGroup, applicationRef.Kind)
}
