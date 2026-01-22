// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the BackupJob webhook with the manager.
func SetupBackupJobWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&BackupJob{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-backups-cozystack-io-v1alpha1-backupjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=backups.cozystack.io,resources=backupjobs,verbs=create;update,versions=v1alpha1,name=mbackupjob.kb.io,admissionReviewVersions=v1

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (j *BackupJob) Default() {
	// No defaults needed for BackupJob currently
}

// +kubebuilder:webhook:path=/validate-backups-cozystack-io-v1alpha1-backupjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=backups.cozystack.io,resources=backupjobs,verbs=create;update,versions=v1alpha1,name=vbackupjob.kb.io,admissionReviewVersions=v1

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (j *BackupJob) ValidateCreate() (admission.Warnings, error) {
	logger := log.FromContext(context.Background())
	logger.Info("validating BackupJob creation", "name", j.Name, "namespace", j.Namespace)

	// Validate that backupClassName is set
	if j.Spec.BackupClassName == "" {
		return nil, fmt.Errorf("backupClassName is required and cannot be empty")
	}

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (j *BackupJob) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	logger := log.FromContext(context.Background())
	logger.Info("validating BackupJob update", "name", j.Name, "namespace", j.Namespace)

	oldJob, ok := old.(*BackupJob)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a BackupJob but got a %T", old))
	}

	// Enforce immutability of backupClassName
	if oldJob.Spec.BackupClassName != "" && oldJob.Spec.BackupClassName != j.Spec.BackupClassName {
		return nil, fmt.Errorf("backupClassName is immutable and cannot be changed from %q to %q", oldJob.Spec.BackupClassName, j.Spec.BackupClassName)
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (j *BackupJob) ValidateDelete() (admission.Warnings, error) {
	// No validation needed for deletion
	return nil, nil
}
