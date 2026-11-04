package lifecycle

import (
	"context"
	"fmt"
	ar "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Unregister removes admission before Helm takes down the Service/backend.
// The kept CRD and its classes are not part of this cleanup.
func Unregister(ctx context.Context, c client.Client, mutating, validating string) error {
	for _, obj := range []client.Object{&ar.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: mutating}}, &ar.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: validating}}} {
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("unregistering admission %s: %w", obj.GetName(), err)
		}
	}
	return nil
}
