// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// ProjectionError is a typed error so the reconciler can tell a misconfiguration
// that needs a human (a Secret owned by someone else) from an API hiccup that
// will clear on its own.
type ProjectionError struct {
	Reason  string
	Message string
}

func (e *ProjectionError) Error() string { return e.Message }

// projectCredentials writes a Source's credentials into the Secret shape
// Forklift's Provider consumes, and returns the Secret's name.
//
// Tenants cannot create Secrets in Cozystack (core.cozystack.io/tenantsecrets is
// read-only at every access level), so a credentialsSecretRef field would be
// unsatisfiable for the audience this API is for: the credentials arrive on the
// Source's spec and the controller materializes them. The exposure class is the
// same one every managed database already accepts for `users[].password`.
//
// The projected Secret is labelled as ours and the projector refuses to write a
// pre-existing Secret that is not, so a name collision can never clobber a
// tenant's own object — it surfaces as a terminal condition instead.
func projectCredentials(ctx context.Context, c client.Client, src *migrationv1alpha1.VMImportSource) (string, error) {
	return projectSecret(ctx, c, src, credentialsSecretName(src.Name), src.Spec.Credentials)
}

// projectSecret materializes one credential set into a Secret of the given
// name. Shared by the provider connection and by each host override, which
// carries its own ESXi account for the same reason: the engine authenticates
// that connection on the host itself.
func projectSecret(
	ctx context.Context,
	c client.Client,
	src *migrationv1alpha1.VMImportSource,
	name string,
	creds migrationv1alpha1.ProviderCredentials,
) (string, error) {
	desired := map[string][]byte{
		"user":     []byte(creds.Username),
		"password": []byte(creds.Password),
	}
	// The engine's vSphere validation wants `user`, `password`, and then either
	// a `cacert` or `insecureSkipVerify` parseable as a boolean. Without one of
	// the latter two it marks the provider's secret invalid and never attempts
	// a connection — a state that looks like a credentials problem and is not.
	// Verified against the vendored engine's own validation on a live cluster.
	if creds.CACert != "" {
		desired["cacert"] = []byte(creds.CACert)
	}
	if creds.InsecureSkipVerify {
		desired["insecureSkipVerify"] = []byte("true")
	}

	existing := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: src.Namespace,
				Labels: map[string]string{
					migrationv1alpha1.ManagedByLabel: migrationv1alpha1.ManagedByValue,
				},
				// Forklift matches a provider's Secret by this label when it
				// re-reads credentials after a Provider edit.
				Annotations: map[string]string{
					"createdForProviderType": string(src.Spec.Type),
				},
				OwnerReferences: []metav1.OwnerReference{
					ownerRef(migrationv1alpha1.GroupVersion.WithKind("VMImportSource"), src.Name, src.UID),
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: desired,
		}
		if err := c.Create(ctx, secret); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Lost a race with another reconcile; the next pass adopts it.
				return name, &ProjectionError{Reason: migrationv1alpha1.ReasonCredentialsMissing, Message: "credentials Secret is being created"}
			}
			return "", err
		}
		return name, nil

	case err != nil:
		return "", err
	}

	if existing.Labels[migrationv1alpha1.ManagedByLabel] != migrationv1alpha1.ManagedByValue {
		return "", &ProjectionError{
			Reason: migrationv1alpha1.ReasonCredentialsMissing,
			Message: fmt.Sprintf(
				"Secret %q already exists in this namespace and is not managed by the migration controller; "+
					"rename the VMImportSource or remove the Secret", name),
		}
	}

	if secretDataEqual(existing.Data, desired) {
		return name, nil
	}
	existing.Data = desired
	if err := c.Update(ctx, existing); err != nil {
		return "", err
	}
	return name, nil
}

func secretDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || string(av) != string(bv) {
			return false
		}
	}
	return true
}
