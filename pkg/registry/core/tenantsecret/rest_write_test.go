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
// that covers it is the one in tenantToSecret. This pins Update against a caller
// sending only the keys it wants set, which is the shape a patched object takes
// by the time it reaches the merge inside tenantToSecret.
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

// A write reaches the backing Secret exactly once, so a write that fails leaves
// the stored object as it was and the caller's keys never become visible to a
// controller or a List. A path that committed the caller's object first and
// repaired it afterwards would open that window, and leave it open for good
// when the repair failed.
func TestUpdate_FailedWriteLeavesTheBackingSecretIntact(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(makeTenantSecret("creds", map[string]string{tenantCALabel: "true"})).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return errors.New("boom")
			},
		}).
		Build()
	r := &REST{c: fc, w: fc, gvr: schema.GroupVersionResource{
		Group: corev1alpha1.GroupName, Version: "v1alpha1", Resource: "tenantsecrets",
	}}

	in := &corev1alpha1.TenantSecret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "creds",
			Namespace: testNamespace,
			Labels:    map[string]string{tsLabelKey: "false", tenantCALabel: "false", "team": "blue"},
		},
	}
	if _, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{}); err == nil {
		t.Fatal("Update reported success although the write to the backing Secret failed")
	}

	want := map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true"}
	if got := backingSecret(t, r, "creds").Labels; !reflect.DeepEqual(got, want) {
		t.Fatalf("backing Secret labels after a failed write: got %v, want %v", got, want)
	}
}

// The drop-rather-than-reject decision rests on this round trip: a tenant sees
// the platform's keys on read, so `kubectl edit` sends them straight back, and
// that write has to be accepted with the stored values, and the payload, intact.
func TestUpdate_UnchangedRoundTripKeepsPlatformKeys(t *testing.T) {
	existing := makeTenantSecret("creds", map[string]string{
		tenantCALabel: "true",
		"team":        "blue",
	})
	existing.Annotations = map[string]string{
		caSourceAnnotation: "cozy-system/root-ca",
		"team.io/owner":    "blue",
	}
	existing.Data = map[string][]byte{"password": []byte("s3cr3t")}
	r := newTestREST(t, existing)

	obj, err := r.Get(testCtx(), "creds", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	got, ok := obj.(*corev1alpha1.TenantSecret)
	if !ok {
		t.Fatalf("expected *TenantSecret, got %T", obj)
	}

	_, _, err = r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(got), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update of an unmodified object returned error: %v", err)
	}

	sec := backingSecret(t, r, "creds")
	wantLabels := map[string]string{
		tsLabelKey:    tsLabelValue,
		tenantCALabel: "true",
		"team":        "blue",
	}
	wantAnnotations := map[string]string{
		caSourceAnnotation: "cozy-system/root-ca",
		"team.io/owner":    "blue",
	}
	if !reflect.DeepEqual(sec.Labels, wantLabels) {
		t.Fatalf("backing Secret labels after a round trip: got %v, want %v", sec.Labels, wantLabels)
	}
	if !reflect.DeepEqual(sec.Annotations, wantAnnotations) {
		t.Fatalf("backing Secret annotations after a round trip: got %v, want %v", sec.Annotations, wantAnnotations)
	}
	// The read hands back both Data and its base64 rendering in StringData; a
	// round trip that took the StringData branch would re-encode the payload.
	if got := string(sec.Data["password"]); got != "s3cr3t" {
		t.Fatalf("backing Secret payload after a round trip: got %q, want %q", got, "s3cr3t")
	}
}

// Every case above names a key some controller writes today, and an
// implementation that filtered that list instead of the prefix would pass all
// of them. Only a key nobody has claimed yet tells the two apart, so each write
// path gets one.
func TestWrites_DropUnclaimedInternalKeys(t *testing.T) {
	const (
		futureLabel      = "internal.cozystack.io/some-future-marker"
		futureAnnotation = "internal.cozystack.io/some-future-source"
	)

	forged := func(labels map[string]string) *corev1alpha1.TenantSecret {
		return &corev1alpha1.TenantSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "creds",
				Namespace:   testNamespace,
				Labels:      labels,
				Annotations: map[string]string{futureAnnotation: "forged", "team.io/owner": "blue"},
			},
			Type: string(corev1.SecretTypeOpaque),
		}
	}

	tests := []struct {
		name       string
		seed       bool
		write      func(t *testing.T, r *REST)
		wantLabels map[string]string
	}{
		{
			name: "create",
			write: func(t *testing.T, r *REST) {
				t.Helper()
				in := forged(map[string]string{futureLabel: "true", "team": "blue"})
				if _, err := r.Create(testCtx(), in, nil, &metav1.CreateOptions{}); err != nil {
					t.Fatalf("Create returned error: %v", err)
				}
			},
			wantLabels: map[string]string{tsLabelKey: tsLabelValue, "team": "blue"},
		},
		{
			name: "update",
			seed: true,
			write: func(t *testing.T, r *REST) {
				t.Helper()
				in := forged(map[string]string{futureLabel: "true", "team": "blue"})
				_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{})
				if err != nil {
					t.Fatalf("Update returned error: %v", err)
				}
			},
			wantLabels: map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true", "team": "blue"},
		},
		{
			name: "patch-shaped update carrying nothing but the forged keys",
			seed: true,
			write: func(t *testing.T, r *REST) {
				t.Helper()
				in := forged(map[string]string{futureLabel: "true"})
				in.Annotations = map[string]string{futureAnnotation: "forged"}
				_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{})
				if err != nil {
					t.Fatalf("Update returned error: %v", err)
				}
			},
			wantLabels: map[string]string{tsLabelKey: tsLabelValue, tenantCALabel: "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *REST
			if tt.seed {
				r = newTestREST(t, makeTenantSecret("creds", map[string]string{tenantCALabel: "true"}))
			} else {
				r = newTestREST(t)
			}

			tt.write(t, r)

			sec := backingSecret(t, r, "creds")
			if !reflect.DeepEqual(sec.Labels, tt.wantLabels) {
				t.Fatalf("backing Secret labels: got %v, want %v", sec.Labels, tt.wantLabels)
			}
			if _, ok := sec.Annotations[futureAnnotation]; ok {
				t.Fatalf("unclaimed platform annotation survived the write: %v", sec.Annotations)
			}
		})
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
