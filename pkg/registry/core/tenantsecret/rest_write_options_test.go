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

func newWriteOptionsREST(t *testing.T, funcs interceptor.Funcs, secrets ...*corev1.Secret) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register core scheme: %v", err)
	}
	objects := make([]client.Object, 0, len(secrets))
	for _, secret := range secrets {
		objects = append(objects, secret)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
	return &REST{c: c, w: c, gvr: schema.GroupVersionResource{
		Group: corev1alpha1.GroupName, Version: "v1alpha1", Resource: "tenantsecrets",
	}}
}

func tenantSecretInput(name string) *corev1alpha1.TenantSecret {
	return &corev1alpha1.TenantSecret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}}
}

func capturePatchOptions(opts ...client.PatchOption) *metav1.PatchOptions {
	converted := (&client.PatchOptions{}).ApplyOptions(opts)
	return converted.AsPatchOptions().DeepCopy()
}

func captureTenantCreateOptions(opts ...client.CreateOption) *metav1.CreateOptions {
	converted := (&client.CreateOptions{}).ApplyOptions(opts)
	return converted.AsCreateOptions().DeepCopy()
}

func captureTenantUpdateOptions(opts ...client.UpdateOption) *metav1.UpdateOptions {
	converted := (&client.UpdateOptions{}).ApplyOptions(opts)
	return converted.AsUpdateOptions().DeepCopy()
}

func captureTenantDeleteOptions(opts ...client.DeleteOption) *metav1.DeleteOptions {
	converted := &client.DeleteOptions{}
	for _, option := range opts {
		option.ApplyToDelete(converted)
	}
	return converted.AsDeleteOptions().DeepCopy()
}

func TestTenantSecretCreatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "creator", FieldValidation: "Strict"}
	var got *metav1.CreateOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
			got = captureTenantCreateOptions(opts...)
			return nil
		},
	})
	if _, err := r.Create(request.WithNamespace(context.Background(), testNamespace), tenantSecretInput("new"), nil, want); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Create options lost: got %#v, want %#v", got, want)
	}
}

func TestTenantSecretUpdatePropagatesWriteOptions(t *testing.T) {
	want := &metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "updater", FieldValidation: "Warn"}
	var got *metav1.UpdateOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.UpdateOption) error {
			got = captureTenantUpdateOptions(opts...)
			return nil
		},
	}, makeTenantSecret("existing", nil))
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, _, err := r.Update(ctx, "existing", rest.DefaultUpdatedObjectInfo(tenantSecretInput("existing")), nil, nil, false, want); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Update options lost: got %#v, want %#v", got, want)
	}
}

func TestTenantSecretForceCreatePropagatesWriteOptions(t *testing.T) {
	update := &metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "apply", FieldValidation: "Strict"}
	want := &metav1.CreateOptions{DryRun: update.DryRun, FieldManager: update.FieldManager, FieldValidation: update.FieldValidation}
	var got *metav1.CreateOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
			got = captureTenantCreateOptions(opts...)
			return nil
		},
	})
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, _, err := r.Update(ctx, "new", rest.DefaultUpdatedObjectInfo(tenantSecretInput("new")), nil, nil, true, update); err != nil {
		t.Fatalf("force-create Update returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("force-create options lost: got %#v, want %#v", got, want)
	}
}

func TestTenantSecretPatchPropagatesWriteOptions(t *testing.T) {
	want := &metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "patcher", FieldValidation: "Warn"}
	var got *metav1.PatchOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			got = capturePatchOptions(opts...)
			return c.Patch(ctx, obj, patch, opts...)
		},
	}, makeTenantSecret("existing", nil))
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, err := r.Patch(ctx, "existing", types.MergePatchType, []byte(`{"metadata":{"annotations":{"test":"value"}}}`), want); err != nil {
		t.Fatalf("Patch returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Patch options lost: got %#v, want %#v", got, want)
	}
}

func TestTenantSecretPatchRepairPreservesDryRun(t *testing.T) {
	want := &metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "patcher", FieldValidation: "Strict"}
	var got *metav1.UpdateOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return c.Patch(ctx, obj, patch, opts...)
		},
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.UpdateOption) error {
			got = captureTenantUpdateOptions(opts...)
			return nil
		},
	}, makeTenantSecret("existing", nil))
	data := []byte(`{"metadata":{"labels":{"internal.cozystack.io/tenantresource":null}}}`)
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, err := r.Patch(ctx, "existing", types.MergePatchType, data, want); err != nil {
		t.Fatalf("Patch returned error: %v", err)
	}
	expected := &metav1.UpdateOptions{DryRun: want.DryRun, FieldManager: want.FieldManager, FieldValidation: want.FieldValidation}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("patch repair options lost: got %#v, want %#v", got, expected)
	}
}

func TestTenantSecretPatchReturnsRepairFailure(t *testing.T) {
	sentinel := errors.New("repair failed")
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return c.Patch(ctx, obj, patch, opts...)
		},
		Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
			return sentinel
		},
	}, makeTenantSecret("existing", nil))
	data := []byte(`{"metadata":{"labels":{"internal.cozystack.io/tenantresource":null}}}`)
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, err := r.Patch(ctx, "existing", types.MergePatchType, data, &metav1.PatchOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("Patch returned %v, want repair error %v", err, sentinel)
	}
}

func TestTenantSecretDeletePropagatesWriteOptions(t *testing.T) {
	propagation := metav1.DeletePropagationForeground
	want := &metav1.DeleteOptions{PropagationPolicy: &propagation, DryRun: []string{metav1.DryRunAll}}
	var got *metav1.DeleteOptions
	r := newWriteOptionsREST(t, interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.DeleteOption) error {
			got = captureTenantDeleteOptions(opts...)
			return nil
		},
	}, makeTenantSecret("existing", nil))
	ctx := request.WithNamespace(context.Background(), testNamespace)
	if _, _, err := r.Delete(ctx, "existing", nil, want); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Delete options lost: got %#v, want %#v", got, want)
	}
}
