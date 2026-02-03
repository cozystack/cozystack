// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestBackupJob_ValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		job     *BackupJob
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid BackupJob with backupClassName",
			job: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "velero",
				},
			},
			wantErr: false,
		},
		{
			name: "BackupJob with empty backupClassName should be rejected",
			job: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "",
				},
			},
			wantErr: true,
			errMsg:  "backupClassName is required and cannot be empty",
		},
		{
			name: "BackupJob with whitespace-only backupClassName should be rejected",
			job: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "   ",
				},
			},
			wantErr: true,
			errMsg:  "backupClassName is required and cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := tt.job.ValidateCreate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("ValidateCreate() error message = %v, want %v", err.Error(), tt.errMsg)
				}
			}
			if warnings != nil && len(warnings) > 0 {
				t.Logf("ValidateCreate() warnings = %v", warnings)
			}
		})
	}
}

func TestBackupJob_ValidateUpdate(t *testing.T) {
	baseJob := &BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-job",
			Namespace: "default",
		},
		Spec: BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			BackupClassName: "velero",
		},
	}

	tests := []struct {
		name    string
		old     runtime.Object
		new     *BackupJob
		wantErr bool
		errMsg  string
	}{
		{
			name: "update with same backupClassName should succeed",
			old:  baseJob,
			new: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "velero", // Same as old
				},
			},
			wantErr: false,
		},
		{
			name: "update changing backupClassName should be rejected",
			old:  baseJob,
			new: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "different-class", // Changed!
				},
			},
			wantErr: true,
			errMsg:  "backupClassName is immutable and cannot be changed from \"velero\" to \"different-class\"",
		},
		{
			name: "update changing other fields but keeping backupClassName should succeed",
			old:  baseJob,
			new: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
					Labels: map[string]string{
						"new-label": "value",
					},
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm2", // Changed application
					},
					BackupClassName: "velero", // Same as old
				},
			},
			wantErr: false,
		},
		{
			name: "update when old backupClassName is empty should be rejected",
			old: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "", // Empty in old
				},
			},
			new: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "velero", // Setting it for the first time
				},
			},
			wantErr: true,
			errMsg:  "backupClassName is immutable",
		},
		{
			name: "update changing from non-empty to different non-empty should be rejected",
			old: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "class-a",
				},
			},
			new: &BackupJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
				Spec: BackupJobSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: "vm1",
					},
					BackupClassName: "class-b", // Changed from class-a
				},
			},
			wantErr: true,
			errMsg:  "backupClassName is immutable and cannot be changed from \"class-a\" to \"class-b\"",
		},
		{
			name: "update with invalid old object type should be rejected",
			old: &corev1.Pod{ // Wrong type - will be cast to runtime.Object in test
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
				},
			},
			new:     baseJob,
			wantErr: true,
			errMsg:  "expected a BackupJob but got a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := tt.new.ValidateUpdate(tt.old)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
				if err != nil {
					t.Logf("Error message: %v", err.Error())
				}
				return
			}
			if tt.wantErr && err != nil {
				if tt.errMsg != "" {
					if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
						t.Errorf("ValidateUpdate() error message = %v, want contains %v", err.Error(), tt.errMsg)
					}
				}
			}
			if warnings != nil && len(warnings) > 0 {
				t.Logf("ValidateUpdate() warnings = %v", warnings)
			}
		})
	}
}

func TestBackupJob_ValidateDelete(t *testing.T) {
	job := &BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-job",
			Namespace: "default",
		},
		Spec: BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			BackupClassName: "velero",
		},
	}

	warnings, err := job.ValidateDelete()
	if err != nil {
		t.Errorf("ValidateDelete() should never return an error, got %v", err)
	}
	if warnings != nil && len(warnings) > 0 {
		t.Logf("ValidateDelete() warnings = %v", warnings)
	}
}

func TestBackupJob_Default(t *testing.T) {
	job := &BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-job",
			Namespace: "default",
		},
		Spec: BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			BackupClassName: "velero",
		},
	}

	// Default() should not panic and should not modify the object
	originalClassName := job.Spec.BackupClassName
	job.Default()
	if job.Spec.BackupClassName != originalClassName {
		t.Errorf("Default() should not modify backupClassName, got %v, want %v", job.Spec.BackupClassName, originalClassName)
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
