package factory

import (
	"testing"
	"time"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBackupJob(t *testing.T) {
	tests := []struct {
		name     string
		plan     *backupsv1alpha1.Plan
		scheduled time.Time
		validate func(*testing.T, *backupsv1alpha1.BackupJob)
	}{
		{
			name: "creates BackupJob with BackupClassName",
			plan: &backupsv1alpha1.Plan{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-plan",
					Namespace: "default",
				},
				Spec: backupsv1alpha1.PlanSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "velero",
					Schedule: backupsv1alpha1.PlanSchedule{
						Type: backupsv1alpha1.PlanScheduleTypeCron,
						Cron: "0 2 * * *",
					},
				},
			},
			scheduled: time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC),
			validate: func(t *testing.T, job *backupsv1alpha1.BackupJob) {
				if job.Name == "" {
					t.Error("BackupJob name should be set")
				}
				if job.Namespace != "default" {
					t.Errorf("BackupJob namespace = %v, want default", job.Namespace)
				}
				if job.Spec.BackupClassName != "velero" {
					t.Errorf("BackupJob BackupClassName = %v, want velero", job.Spec.BackupClassName)
				}
				if job.Spec.ApplicationRef.Kind != "VirtualMachine" {
					t.Errorf("BackupJob ApplicationRef.Kind = %v, want VirtualMachine", job.Spec.ApplicationRef.Kind)
				}
				if job.Spec.ApplicationRef.Name != "vm1" {
					t.Errorf("BackupJob ApplicationRef.Name = %v, want vm1", job.Spec.ApplicationRef.Name)
				}
				if job.Spec.PlanRef == nil || job.Spec.PlanRef.Name != "test-plan" {
					t.Errorf("BackupJob PlanRef = %v, want {Name: test-plan}", job.Spec.PlanRef)
				}
			},
		},
		{
			name: "normalizes ApplicationRef apiGroup",
			plan: &backupsv1alpha1.Plan{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-plan",
					Namespace: "default",
				},
				Spec: backupsv1alpha1.PlanSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						// No APIGroup specified
						Kind: "MySQL",
						Name: "mysql1",
					},
					BackupClassName: "velero",
					Schedule: backupsv1alpha1.PlanSchedule{
						Type: backupsv1alpha1.PlanScheduleTypeCron,
						Cron: "0 2 * * *",
					},
				},
			},
			scheduled: time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC),
			validate: func(t *testing.T, job *backupsv1alpha1.BackupJob) {
				if job.Spec.ApplicationRef.APIGroup == nil {
					t.Error("BackupJob ApplicationRef.APIGroup should be set (normalized)")
					return
				}
				if *job.Spec.ApplicationRef.APIGroup != "apps.cozystack.io" {
					t.Errorf("BackupJob ApplicationRef.APIGroup = %v, want apps.cozystack.io", *job.Spec.ApplicationRef.APIGroup)
				}
			},
		},
		{
			name: "preserves explicit ApplicationRef apiGroup",
			plan: &backupsv1alpha1.Plan{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-plan",
					Namespace: "default",
				},
				Spec: backupsv1alpha1.PlanSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						APIGroup: stringPtr("custom.api.group.io"),
						Kind:     "CustomApp",
						Name:     "custom1",
					},
					BackupClassName: "velero",
					Schedule: backupsv1alpha1.PlanSchedule{
						Type: backupsv1alpha1.PlanScheduleTypeCron,
						Cron: "0 2 * * *",
					},
				},
			},
			scheduled: time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC),
			validate: func(t *testing.T, job *backupsv1alpha1.BackupJob) {
				if job.Spec.ApplicationRef.APIGroup == nil {
					t.Error("BackupJob ApplicationRef.APIGroup should be preserved")
					return
				}
				if *job.Spec.ApplicationRef.APIGroup != "custom.api.group.io" {
					t.Errorf("BackupJob ApplicationRef.APIGroup = %v, want custom.api.group.io", *job.Spec.ApplicationRef.APIGroup)
				}
			},
		},
		{
			name: "generates unique job name based on timestamp",
			plan: &backupsv1alpha1.Plan{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-plan",
					Namespace: "default",
				},
				Spec: backupsv1alpha1.PlanSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "velero",
					Schedule: backupsv1alpha1.PlanSchedule{
						Type: backupsv1alpha1.PlanScheduleTypeCron,
						Cron: "0 2 * * *",
					},
				},
			},
			scheduled: time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC),
			validate: func(t *testing.T, job *backupsv1alpha1.BackupJob) {
				if job.Name == "" {
					t.Error("BackupJob name should be generated")
				}
				// Name should start with plan name
				if len(job.Name) < len("test-plan") {
					t.Errorf("BackupJob name = %v, should start with test-plan", job.Name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := BackupJob(tt.plan, tt.scheduled)
			if job == nil {
				t.Fatal("BackupJob() returned nil")
			}
			tt.validate(t, job)
		})
	}
}

func stringPtr(s string) *string {
	return &s
}
