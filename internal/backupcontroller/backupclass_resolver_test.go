package backupcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func TestNormalizeApplicationRef(t *testing.T) {
	tests := []struct {
		name     string
		input    corev1.TypedLocalObjectReference
		expected corev1.TypedLocalObjectReference
	}{
		{
			name: "apiGroup not specified - should default to apps.cozystack.io",
			input: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			expected: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(DefaultApplicationAPIGroup),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
		},
		{
			name: "apiGroup is nil - should default to apps.cozystack.io",
			input: corev1.TypedLocalObjectReference{
				APIGroup: nil,
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
			expected: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(DefaultApplicationAPIGroup),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
		},
		{
			name: "apiGroup is empty string - should default to apps.cozystack.io",
			input: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(""),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
			expected: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(DefaultApplicationAPIGroup),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
		},
		{
			name: "apiGroup is explicitly set - should keep it",
			input: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("custom.api.group.io"),
				Kind:     "CustomApp",
				Name:     "custom-app",
			},
			expected: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("custom.api.group.io"),
				Kind:     "CustomApp",
				Name:     "custom-app",
			},
		},
		{
			name: "apiGroup is apps.cozystack.io - should keep it",
			input: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(DefaultApplicationAPIGroup),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
			expected: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr(DefaultApplicationAPIGroup),
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeApplicationRef(tt.input)
			if !apiequality.Semantic.DeepEqual(result, tt.expected) {
				t.Errorf("NormalizeApplicationRef() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestResolveBackupClass(t *testing.T) {
	scheme := runtime.NewScheme()
	err := backupsv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatalf("Failed to add backupsv1alpha1 to scheme: %v", err)
	}
	err = strategyv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatalf("Failed to add strategyv1alpha1 to scheme: %v", err)
	}

	tests := []struct {
		name                string
		backupClass         *backupsv1alpha1.BackupClass
		applicationRef      corev1.TypedLocalObjectReference
		backupClassName     string
		wantErr             bool
		expectedStrategyRef *corev1.TypedLocalObjectReference
		expectedParams      map[string]string
	}{
		{
			name: "successful resolution - matches VirtualMachine strategy",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-vm",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								Kind: "VirtualMachine",
							},
							Parameters: map[string]string{
								"backupStorageLocationName": "default",
							},
						},
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-mariadb",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								Kind: "MariaDB",
							},
							Parameters: map[string]string{
								"backupStorageLocationName": "mariadb-storage",
							},
						},
					},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			backupClassName: "velero",
			wantErr:         false,
			expectedStrategyRef: &corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("strategy.backups.cozystack.io"),
				Kind:     "Velero",
				Name:     "velero-strategy-vm",
			},
			expectedParams: map[string]string{
				"backupStorageLocationName": "default",
			},
		},
		{
			name: "successful resolution - matches MariaDB strategy with explicit apiGroup",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-mariadb",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								APIGroup: stringPtr("apps.cozystack.io"),
								Kind:     "MariaDB",
							},
							Parameters: map[string]string{
								"backupStorageLocationName": "mariadb-storage",
							},
						},
					},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "MariaDB",
				Name:     "mariadb1",
			},
			backupClassName: "velero",
			wantErr:         false,
			expectedStrategyRef: &corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("strategy.backups.cozystack.io"),
				Kind:     "Velero",
				Name:     "velero-strategy-mariadb",
			},
			expectedParams: map[string]string{
				"backupStorageLocationName": "mariadb-storage",
			},
		},
		{
			name: "successful resolution - applicationRef without apiGroup defaults correctly",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-vm",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								Kind: "VirtualMachine",
							},
							Parameters: map[string]string{
								"backupStorageLocationName": "default",
							},
						},
					},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				// No APIGroup specified
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			backupClassName: "velero",
			wantErr:         false,
			expectedStrategyRef: &corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("strategy.backups.cozystack.io"),
				Kind:     "Velero",
				Name:     "velero-strategy-vm",
			},
			expectedParams: map[string]string{
				"backupStorageLocationName": "default",
			},
		},
		{
			name: "error - BackupClass not found",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				Kind: "VirtualMachine",
				Name: "vm1",
			},
			backupClassName: "nonexistent",
			wantErr:         true,
		},
		{
			name: "error - no matching strategy found",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-vm",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								Kind: "VirtualMachine",
							},
						},
					},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				Kind: "PostgreSQL", // Not in BackupClass
				Name: "pg1",
			},
			backupClassName: "velero",
			wantErr:         true,
		},
		{
			name: "error - apiGroup mismatch",
			backupClass: &backupsv1alpha1.BackupClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "velero",
				},
				Spec: backupsv1alpha1.BackupClassSpec{
					Strategies: []backupsv1alpha1.BackupClassStrategy{
						{
							StrategyRef: corev1.TypedLocalObjectReference{
								APIGroup: stringPtr("strategy.backups.cozystack.io"),
								Kind:     "Velero",
								Name:     "velero-strategy-vm",
							},
							Application: backupsv1alpha1.ApplicationSelector{
								APIGroup: stringPtr("custom.api.group.io"),
								Kind:     "VirtualMachine",
							},
						},
					},
				},
			},
			applicationRef: corev1.TypedLocalObjectReference{
				APIGroup: stringPtr("apps.cozystack.io"), // Different apiGroup
				Kind:     "VirtualMachine",
				Name:     "vm1",
			},
			backupClassName: "velero",
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.backupClass).
				Build()

			resolved, err := ResolveBackupClass(ctx, fakeClient, tt.backupClassName, tt.applicationRef)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ResolveBackupClass() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("ResolveBackupClass() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if resolved == nil {
				t.Errorf("ResolveBackupClass() returned nil, expected ResolvedBackupConfig")
				return
			}

			// Verify strategy ref using apimachinery equality
			if tt.expectedStrategyRef != nil {
				if !apiequality.Semantic.DeepEqual(resolved.StrategyRef, *tt.expectedStrategyRef) {
					t.Errorf("ResolveBackupClass() StrategyRef = %v, want %v", resolved.StrategyRef, *tt.expectedStrategyRef)
				}
			}

			// Verify parameters using apimachinery equality
			if tt.expectedParams != nil {
				if !apiequality.Semantic.DeepEqual(resolved.Parameters, tt.expectedParams) {
					t.Errorf("ResolveBackupClass() Parameters = %v, want %v", resolved.Parameters, tt.expectedParams)
				}
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}
