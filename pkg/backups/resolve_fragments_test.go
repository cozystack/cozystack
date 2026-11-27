package backups

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func class(name, fragmentOf string, bindings ...backupsv1alpha1.BackupClassStrategy) *backupsv1alpha1.BackupClass {
	c := &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       backupsv1alpha1.BackupClassSpec{Strategies: bindings},
	}
	if fragmentOf != "" {
		c.Labels = map[string]string{backupsv1alpha1.BackupClassFragmentLabel: fragmentOf}
	}
	return c
}

func binding(appKind, strategyName string) backupsv1alpha1.BackupClassStrategy {
	return backupsv1alpha1.BackupClassStrategy{
		Application: backupsv1alpha1.ApplicationSelector{Kind: appKind},
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new("strategy.example.test"),
			Kind:     "Dump",
			Name:     strategyName,
		},
	}
}

func TestResolveBackupClassFragments(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := backupsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		objects      []client.Object
		appKind      string
		wantStrategy string
		wantNotFound bool
		wantNoMatch  bool
	}{
		{
			name:         "neither the class nor a fragment exists",
			objects:      []client.Object{class("other", "", binding("Widget", "other"))},
			appKind:      "Widget",
			wantNotFound: true,
		},
		{
			name:         "a fragment of another class is not a part of this one",
			objects:      []client.Object{class("part", "other", binding("Widget", "from-other"))},
			appKind:      "Widget",
			wantNotFound: true,
		},
		{
			name:         "fragments alone make a class",
			objects:      []client.Object{class("part", "default", binding("Widget", "from-fragment"))},
			appKind:      "Widget",
			wantStrategy: "from-fragment",
		},
		{
			name: "the class binds nothing itself and a fragment binds the kind",
			objects: []client.Object{
				class("default", ""),
				class("part", "default", binding("Widget", "from-fragment")),
			},
			appKind:      "Widget",
			wantStrategy: "from-fragment",
		},
		{
			name: "the class's own binding wins over a fragment's",
			objects: []client.Object{
				class("default", "", binding("Widget", "own")),
				class("a-part", "default", binding("Widget", "from-fragment")),
			},
			appKind:      "Widget",
			wantStrategy: "own",
		},
		{
			name: "between fragments the first by name wins",
			objects: []client.Object{
				class("b-part", "default", binding("Widget", "from-b")),
				class("a-part", "default", binding("Widget", "from-a")),
			},
			appKind:      "Widget",
			wantStrategy: "from-a",
		},
		{
			name: "no part binds the kind",
			objects: []client.Object{
				class("default", "", binding("Gadget", "own")),
				class("part", "default", binding("Gizmo", "from-fragment")),
			},
			appKind:     "Widget",
			wantNoMatch: true,
		},
		{
			name: "a class labelled as its own fragment is matched once, first",
			objects: []client.Object{
				class("default", "default", binding("Widget", "own")),
				class("a-part", "default", binding("Widget", "from-fragment")),
			},
			appKind:      "Widget",
			wantStrategy: "own",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			got, err := ResolveBackupClass(context.Background(), c, "default",
				corev1.TypedLocalObjectReference{Kind: tt.appKind, Name: "app"})
			switch {
			case tt.wantNotFound:
				if !apierrors.IsNotFound(err) {
					t.Fatalf("want a NotFound error, got %v", err)
				}
			case tt.wantNoMatch:
				if !errors.Is(err, ErrNoMatchingStrategy) {
					t.Fatalf("want ErrNoMatchingStrategy, got %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got.StrategyRef.Name != tt.wantStrategy {
					t.Fatalf("resolved strategy %q, want %q", got.StrategyRef.Name, tt.wantStrategy)
				}
			}
		})
	}
}
