// Package backups holds the part of the backup framework that the core
// controller and every strategy driver must agree on. A driver lives in
// another Go module and imports this package; nothing here names a strategy
// or an application.
package backups

import (
	"context"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// ErrNoMatchingStrategy is returned by ResolveBackupClass when the class
// exists but binds no strategy to the application's kind. It is a
// configuration error that no retry fixes, so callers match it with
// errors.Is and fail the job instead of requeueing it.
var ErrNoMatchingStrategy = errors.New("no matching strategy in BackupClass for application Kind")

// ResolvedBackupConfig is the strategy a BackupClass binds to one application
// kind, with the parameters of that binding.
type ResolvedBackupConfig struct {
	StrategyRef corev1.TypedLocalObjectReference
	Parameters  map[string]string
}

// ResolveBackupClass returns the strategy that the class named className
// binds to the kind of applicationRef.
//
// A class is the BackupClass of that name together with its fragments: every
// BackupClass labelled backups.cozystack.io/class=<className>. The named
// object's own bindings are matched first, then the fragments' in name
// order, and the first binding for the kind wins. A class may consist of
// fragments alone. When neither the object nor a fragment exists the error
// satisfies apierrors.IsNotFound.
func ResolveBackupClass(
	ctx context.Context,
	c client.Reader,
	className string,
	applicationRef corev1.TypedLocalObjectReference,
) (*ResolvedBackupConfig, error) {
	applicationRef = backupsv1alpha1.NormalizeApplicationRef(applicationRef)
	appAPIGroup := *applicationRef.APIGroup

	var parts []backupsv1alpha1.BackupClass

	named := &backupsv1alpha1.BackupClass{}
	err := c.Get(ctx, client.ObjectKey{Name: className}, named)
	switch {
	case err == nil:
		parts = append(parts, *named)
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("failed to get BackupClass %s: %w", className, err)
	}

	fragments := &backupsv1alpha1.BackupClassList{}
	if err := c.List(ctx, fragments, client.MatchingLabels{backupsv1alpha1.BackupClassFragmentLabel: className}); err != nil {
		return nil, fmt.Errorf("failed to list fragments of BackupClass %s: %w", className, err)
	}
	sort.Slice(fragments.Items, func(i, j int) bool { return fragments.Items[i].Name < fragments.Items[j].Name })
	for _, f := range fragments.Items {
		// A class labelled as its own fragment is already first in parts.
		if f.Name != className {
			parts = append(parts, f)
		}
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("failed to get BackupClass %s: %w", className,
			apierrors.NewNotFound(schema.GroupResource{Group: backupsv1alpha1.GroupVersion.Group, Resource: "backupclasses"}, className))
	}

	for _, part := range parts {
		for _, strategy := range part.Spec.Strategies {
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
	}

	return nil, fmt.Errorf("%w: BackupClass=%s application=%s/%s",
		ErrNoMatchingStrategy, className, appAPIGroup, applicationRef.Kind)
}
