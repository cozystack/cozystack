// SPDX-License-Identifier: Apache-2.0

package tenantsecret

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

// The apiserver delivers validating admission to a storage as the
// ValidateObjectFunc it passes into Create, Update and Delete: mutating
// admission runs in the handler, but validating webhooks and
// ValidatingAdmissionPolicies run only when the storage calls that callback.
// A storage that drops it answers every write as if no policy existed.

var errDenied = errors.New("denied by test admission")

func denyCreate(called *bool) rest.ValidateObjectFunc {
	return func(_ context.Context, _ runtime.Object) error {
		*called = true
		return errDenied
	}
}

func denyUpdate(called *bool) rest.ValidateObjectUpdateFunc {
	return func(_ context.Context, _, _ runtime.Object) error {
		*called = true
		return errDenied
	}
}

func tenantSecretIn(name string) *corev1alpha1.TenantSecret {
	return &corev1alpha1.TenantSecret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Type:       string(corev1.SecretTypeOpaque),
	}
}

func secretExists(t *testing.T, r *REST, name string) bool {
	t.Helper()
	err := r.c.Get(testCtx(), types.NamespacedName{Namespace: testNamespace, Name: name}, &corev1.Secret{}, &client.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get backing Secret %q: %v", name, err)
	}
	return err == nil
}

func TestCreateRunsValidatingAdmission(t *testing.T) {
	r := newTestREST(t)
	called := false

	_, err := r.Create(testCtx(), tenantSecretIn("creds"), denyCreate(&called), &metav1.CreateOptions{})
	if !called {
		t.Fatal("Create never invoked validating admission")
	}
	if !errors.Is(err, errDenied) {
		t.Fatalf("Create swallowed the admission denial: got %v", err)
	}
	if secretExists(t, r, "creds") {
		t.Error("Create wrote the backing Secret despite the denial")
	}
}

func TestUpdateRunsValidatingAdmission(t *testing.T) {
	r := newTestREST(t, makeTenantSecret("creds", nil))
	called := false

	in := tenantSecretIn("creds")
	in.Data = map[string][]byte{"k": []byte("v")}
	_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(in), nil, denyUpdate(&called), false, &metav1.UpdateOptions{})
	if !called {
		t.Fatal("Update never invoked validating admission")
	}
	if !errors.Is(err, errDenied) {
		t.Fatalf("Update swallowed the admission denial: got %v", err)
	}
	if got := backingSecret(t, r, "creds").Data["k"]; got != nil {
		t.Errorf("Update wrote the backing Secret despite the denial: got %q", got)
	}
}

func TestUpdateForceCreateRunsValidatingAdmission(t *testing.T) {
	// The create-on-update path is a create, so it is createValidation that
	// carries admission for it.
	r := newTestREST(t)
	called := false

	_, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(tenantSecretIn("creds")), denyCreate(&called), nil, true, &metav1.UpdateOptions{})
	if !called {
		t.Fatal("force-create Update never invoked validating admission")
	}
	if !errors.Is(err, errDenied) {
		t.Fatalf("force-create Update swallowed the admission denial: got %v", err)
	}
	if secretExists(t, r, "creds") {
		t.Error("force-create Update wrote the backing Secret despite the denial")
	}
}

func TestDeleteRunsValidatingAdmission(t *testing.T) {
	// This is the path the cluster-wide no-delete guardrail runs on.
	r := newTestREST(t, makeTenantSecret("creds", nil))
	called := false

	_, deleted, err := r.Delete(testCtx(), "creds", denyCreate(&called), &metav1.DeleteOptions{})
	if !called {
		t.Fatal("Delete never invoked validating admission")
	}
	if !errors.Is(err, errDenied) {
		t.Fatalf("Delete swallowed the admission denial: got %v", err)
	}
	if deleted {
		t.Error("Delete reported the object as deleted despite the denial")
	}
	if !secretExists(t, r, "creds") {
		t.Error("Delete removed the backing Secret despite the denial")
	}
}

func TestWritesAcceptNilValidation(t *testing.T) {
	// The apiserver passes a nil callback when no admission plugin handles the
	// operation, so every write path must stay usable without one.
	r := newTestREST(t)
	if _, err := r.Create(testCtx(), tenantSecretIn("creds"), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create with nil validation: %v", err)
	}
	if _, _, err := r.Update(testCtx(), "creds", rest.DefaultUpdatedObjectInfo(tenantSecretIn("creds")), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update with nil validation: %v", err)
	}
	if _, _, err := r.Delete(testCtx(), "creds", nil, &metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete with nil validation: %v", err)
	}
}
