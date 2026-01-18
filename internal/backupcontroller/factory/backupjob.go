package factory

import (
	"fmt"
	"time"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// defaultApplicationAPIGroup is the default API group for applications
	// when not specified in ApplicationRef.
	defaultApplicationAPIGroup = "apps.cozystack.io"
)

// normalizeApplicationRef sets the default apiGroup to "apps.cozystack.io" if it's not specified.
func normalizeApplicationRef(ref corev1.TypedLocalObjectReference) corev1.TypedLocalObjectReference {
	if ref.APIGroup == nil || *ref.APIGroup == "" {
		defaultGroup := defaultApplicationAPIGroup
		ref.APIGroup = &defaultGroup
	}
	return ref
}

func BackupJob(p *backupsv1alpha1.Plan, scheduledFor time.Time) *backupsv1alpha1.BackupJob {
	// Normalize ApplicationRef (default apiGroup if not specified)
	appRef := normalizeApplicationRef(*p.Spec.ApplicationRef.DeepCopy())

	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", p.Name, scheduledFor.Unix()/60),
			Namespace: p.Namespace,
		},
		Spec: backupsv1alpha1.BackupJobSpec{
			PlanRef: &corev1.LocalObjectReference{
				Name: p.Name,
			},
			ApplicationRef: appRef,
			BackupClassName: p.Spec.BackupClassName,
		},
	}
	return job
}
