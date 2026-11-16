package lifecycle

import (
	"context"
	"errors"
	ar "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"testing"
)

func TestUnregisterOnlyThisRelease(t *testing.T) {
	s := runtime.NewScheme()
	if err := ar.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(&ar.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "owned-m"}}, &ar.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "owned-v"}}, &ar.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "other"}}).Build()
	for range 2 {
		if err := Unregister(context.Background(), c, "owned-m", "owned-v"); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "owned-v"}, &ar.ValidatingWebhookConfiguration{}); !apierrors.IsNotFound(err) {
		t.Fatalf("owned config still present: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "other"}, &ar.ValidatingWebhookConfiguration{}); err != nil {
		t.Fatalf("unrelated config changed: %v", err)
	}
}

func TestCleanupFailureIsNotIgnored(t *testing.T) {
	s := runtime.NewScheme()
	if err := ar.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	want := errors.New("delete denied")
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return want }}).Build()
	if err := Unregister(context.Background(), c, "owned-m", "owned-v"); !errors.Is(err, want) {
		t.Fatalf("cleanup error=%v want %v", err, want)
	}
}
