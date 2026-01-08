package factory

import (
	"fmt"
	"time"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BackupJob(p *backupsv1alpha1.Plan, scheduledFor time.Time) *backupsv1alpha1.BackupJob {
	job := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", p.Name, scheduledFor.Unix()/60),
			Namespace: p.Namespace,
		},
		Spec: backupsv1alpha1.BackupJobSpec{
			PlanRef: &corev1.LocalObjectReference{
				Name: p.Name,
			},
			ApplicationRef: *p.Spec.ApplicationRef.DeepCopy(),
			StorageRef:     *p.Spec.StorageRef.DeepCopy(),
			StrategyRef:    *p.Spec.StrategyRef.DeepCopy(),
		},
	}
	return job
}
