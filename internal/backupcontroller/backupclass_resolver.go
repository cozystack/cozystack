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
	DefaultApplicationAPIGroup = "apps.cozystack.io"
)

// NormalizeApplicationRef sets the default apiGroup to "apps.cozystack.io" if it's not specified.
// This function is exported so it can be used by other packages (e.g., factory).
func NormalizeApplicationRef(ref corev1.TypedLocalObjectReference) corev1.TypedLocalObjectReference {
	if ref.APIGroup == nil || *ref.APIGroup == "" {
		defaultGroup := DefaultApplicationAPIGroup
		ref.APIGroup = &defaultGroup
	}
	return ref
}

// normalizeApplicationRef is an internal alias for consistency within this package.
func normalizeApplicationRef(ref corev1.TypedLocalObjectReference) corev1.TypedLocalObjectReference {
	return NormalizeApplicationRef(ref)
}

// ResolvedBackupConfig contains the resolved strategy and storage configuration
// from a BackupClass.
type ResolvedBackupConfig struct {
	StrategyRef corev1.TypedLocalObjectReference
	StorageRef  *corev1.TypedLocalObjectReference // Optional, may come from parameters
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
	applicationRef = normalizeApplicationRef(applicationRef)

	// Get BackupClass
	backupClass := &backupsv1alpha1.BackupClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: backupClassName}, backupClass); err != nil {
		return nil, fmt.Errorf("failed to get BackupClass %s: %w", backupClassName, err)
	}

	// Determine application API group (already normalized, but extract for matching)
	appAPIGroup := DefaultApplicationAPIGroup
	if applicationRef.APIGroup != nil {
		appAPIGroup = *applicationRef.APIGroup
	}

	// Find matching strategy
	for _, strategy := range backupClass.Spec.Strategies {
		// Normalize strategy's application selector (default apiGroup if not specified)
		strategyAPIGroup := DefaultApplicationAPIGroup
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
