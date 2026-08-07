// SPDX-License-Identifier: Apache-2.0

package tenantsecret

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// Platform-owned keys, spelled out here rather than imported so the test pins
// the literal strings the controllers and the admission webhook agree on.
const (
	tenantCALabel      = "internal.cozystack.io/tenant-ca"
	caCopyLabel        = "internal.cozystack.io/ca-cert-copy"
	caSourceAnnotation = "internal.cozystack.io/ca-cert-source"
)

func testCtx() context.Context {
	return request.WithNamespace(context.Background(), testNamespace)
}

// backingSecret reads the Secret the registry actually wrote.
func backingSecret(t *testing.T, r *REST, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, sec); err != nil {
		t.Fatalf("get backing Secret %q: %v", name, err)
	}
	return sec
}

func TestCreate_DropsCallerSuppliedInternalLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]string
	}{
		{
			name:   "no labels keeps only the tenant-resource marker",
			labels: nil,
			want:   map[string]string{tsLabelKey: tsLabelValue},
		},
		{
			name:   "ordinary labels are preserved",
			labels: map[string]string{"apps.cozystack.io/application.kind": "Bucket", "team": "blue"},
			want: map[string]string{
				tsLabelKey:                           tsLabelValue,
				"apps.cozystack.io/application.kind": "Bucket",
				"team":                               "blue",
			},
		},
		{
			name:   "spoofed tenant-ca selector is dropped",
			labels: map[string]string{tenantCALabel: "true", "team": "blue"},
			want:   map[string]string{tsLabelKey: tsLabelValue, "team": "blue"},
		},
		{
			name:   "spoofed ca-cert-copy ownership marker is dropped",
			labels: map[string]string{caCopyLabel: "true"},
			want:   map[string]string{tsLabelKey: tsLabelValue},
		},
		{
			name:   "tenant-resource verdict cannot be forged",
			labels: map[string]string{tsLabelKey: "false"},
			want:   map[string]string{tsLabelKey: tsLabelValue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestREST(t)
			in := &corev1alpha1.TenantSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "creds",
					Namespace: testNamespace,
					Labels:    tt.labels,
				},
				Type: string(corev1.SecretTypeOpaque),
			}

			if _, err := r.Create(testCtx(), in, nil, &metav1.CreateOptions{}); err != nil {
				t.Fatalf("Create returned error: %v", err)
			}

			if got := backingSecret(t, r, "creds").Labels; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("backing Secret labels: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreate_DropsCallerSuppliedInternalAnnotations(t *testing.T) {
	r := newTestREST(t)
	in := &corev1alpha1.TenantSecret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "creds",
			Namespace: testNamespace,
			Annotations: map[string]string{
				caSourceAnnotation: "tenant-root/forged",
				"team.io/owner":    "blue",
			},
		},
		Type: string(corev1.SecretTypeOpaque),
	}

	if _, err := r.Create(testCtx(), in, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	want := map[string]string{"team.io/owner": "blue"}
	if got := backingSecret(t, r, "creds").Annotations; !reflect.DeepEqual(got, want) {
		t.Fatalf("backing Secret annotations: got %v, want %v", got, want)
	}
}

func TestUpdate_KeepsPlatformLabelsCallerCannotSee(t *testing.T) {
	existing := makeTenantSecret("creds", map[string]string{
		tenantCALabel: "true",
		"team":        "blue",
	})
	existing.Annotations = map[string]string{caSourceAnnotation: "cozy-system/root-ca"}

	tests := []struct {
		name            string
		labels          map[string]string
		annotations     map[string]string
		wantLabels      map[string]string
		wantAnnotations map[string]string
	}{
		{
			name:   "omitting a platform label does not strip it",
			labels: map[string]string{"team": "blue"},
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
				"team":        "blue",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:   "overwriting the tenant-ca selector is ignored",
			labels: map[string]string{tenantCALabel: "false"},
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
				"team":        "blue",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:   "overwriting the tenant-resource verdict is ignored",
			labels: map[string]string{tsLabelKey: "false"},
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
				"team":        "blue",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:   "planting a new platform label is ignored, ordinary labels land",
			labels: map[string]string{caCopyLabel: "true", "tier": "gold"},
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
				"team":        "blue",
				"tier":        "gold",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:            "overwriting a platform annotation is ignored",
			annotations:     map[string]string{caSourceAnnotation: "tenant-root/forged", "team.io/owner": "blue"},
			wantLabels:      map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true", "team": "blue"},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca", "team.io/owner": "blue"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestREST(t, existing.DeepCopy())
			in := &corev1alpha1.TenantSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "creds",
					Namespace:   testNamespace,
					Labels:      tt.labels,
					Annotations: tt.annotations,
				},
				Type: string(corev1.SecretTypeOpaque),
			}

			_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{})
			if err != nil {
				t.Fatalf("Update returned error: %v", err)
			}

			sec := backingSecret(t, r, "creds")
			if !reflect.DeepEqual(sec.Labels, tt.wantLabels) {
				t.Fatalf("backing Secret labels: got %v, want %v", sec.Labels, tt.wantLabels)
			}
			if !reflect.DeepEqual(sec.Annotations, tt.wantAnnotations) {
				t.Fatalf("backing Secret annotations: got %v, want %v", sec.Annotations, tt.wantAnnotations)
			}
		})
	}
}

// PATCH is served out of Update — rest.Patcher is Getter+Updater — so the guard
// that covers it is the one in tenantToSecret, not the Patch method further
// down. This pins Update against a caller sending only the keys it wants set,
// which is the shape that reaches the merge inside tenantToSecret.
func TestUpdate_PatchShapedObjectCannotPlantPlatformLabels(t *testing.T) {
	r := newTestREST(t, makeTenantSecret("creds", map[string]string{tenantCALabel: "true"}))
	patched := &corev1alpha1.TenantSecret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "creds",
			Namespace: testNamespace,
			Labels:    map[string]string{caCopyLabel: "true"},
		},
	}

	_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(patched), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	want := map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true"}
	if got := backingSecret(t, r, "creds").Labels; !reflect.DeepEqual(got, want) {
		t.Fatalf("backing Secret labels: got %v, want %v", got, want)
	}
}

// A PATCH request does not land here — rest.Patcher is Getter+Updater, so the
// endpoint installer serves PATCH through Update, which the case above covers.
// This method is a second write path all the same, and it reaches the backing
// Secret without any conversion, so it restores the platform keys afterwards.
func TestPatch_RestoresPlatformLabels(t *testing.T) {
	existing := makeTenantSecret("creds", map[string]string{tenantCALabel: "true"})
	existing.Annotations = map[string]string{caSourceAnnotation: "cozy-system/root-ca"}

	tests := []struct {
		name            string
		patch           string
		wantLabels      map[string]string
		wantAnnotations map[string]string
	}{
		{
			name:  "planting a platform label is reverted",
			patch: `{"metadata":{"labels":{"` + caCopyLabel + `":"true","team":"blue"}}}`,
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
				"team":        "blue",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:  "stripping platform labels is reverted",
			patch: `{"metadata":{"labels":{"` + tsLabelKey + `":null,"` + tenantCALabel + `":null}}}`,
			wantLabels: map[string]string{
				tsLabelKey:    tsLabelValue,
				tenantCALabel: "true",
			},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
		{
			name:            "rewriting a platform annotation is reverted",
			patch:           `{"metadata":{"annotations":{"` + caSourceAnnotation + `":"tenant-root/forged"}}}`,
			wantLabels:      map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true"},
			wantAnnotations: map[string]string{caSourceAnnotation: "cozy-system/root-ca"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestREST(t, existing.DeepCopy())

			if _, err := r.Patch(testCtx(), "creds", types.MergePatchType, []byte(tt.patch), &metav1.PatchOptions{}); err != nil {
				t.Fatalf("Patch returned error: %v", err)
			}

			sec := backingSecret(t, r, "creds")
			if !reflect.DeepEqual(sec.Labels, tt.wantLabels) {
				t.Fatalf("backing Secret labels: got %v, want %v", sec.Labels, tt.wantLabels)
			}
			if !reflect.DeepEqual(sec.Annotations, tt.wantAnnotations) {
				t.Fatalf("backing Secret annotations: got %v, want %v", sec.Annotations, tt.wantAnnotations)
			}
		})
	}
}

// A patch whose platform keys could not be put back must not come back as a
// success: the caller would take it as accepted and the spoofed key would sit
// on the backing Secret unreported.
func TestPatch_FailedRestoreIsReported(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(makeTenantSecret("creds", nil)).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return errors.New("boom")
			},
		}).
		Build()
	r := &REST{c: fc, w: fc, gvr: schema.GroupVersionResource{
		Group: corev1alpha1.GroupName, Version: "v1alpha1", Resource: "tenantsecrets",
	}}

	patch := []byte(`{"metadata":{"labels":{"` + tsLabelKey + `":null}}}`)
	if _, err := r.Patch(testCtx(), "creds", types.MergePatchType, patch, &metav1.PatchOptions{}); err == nil {
		t.Fatal("Patch reported success although the platform labels could not be restored")
	}
}

// Reads are not filtered beyond the tenant-resource marker: a tenant is allowed
// to see the platform's verdicts, it just cannot write them.
func TestGet_KeepsPlatformLabelsVisible(t *testing.T) {
	r := newTestREST(t, makeTenantSecret("creds", map[string]string{tenantCALabel: "true"}))

	obj, err := r.Get(testCtx(), "creds", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	ts, ok := obj.(*corev1alpha1.TenantSecret)
	if !ok {
		t.Fatalf("expected *TenantSecret, got %T", obj)
	}
	if ts.Labels[tenantCALabel] != "true" {
		t.Fatalf("tenant-ca label hidden from the tenant: got %v", ts.Labels)
	}
}
