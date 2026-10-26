// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"encoding/json"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"k8s.io/klog/v2"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/store"
)

// Credentials returns the kind's credentials block, nil for a kind that is
// not converted.
func (r *REST) Credentials() *cozyv1alpha1.ApplicationDefinitionCredentials {
	return r.credentials
}

// AccountsOf resolves the tenant accounts a HelmRelease declares; the
// credentials registry asks it on a mint of an account whose Secret is missing.
func (r *REST) AccountsOf(_ context.Context, hr *helmv2.HelmRelease) ([]account.Account, error) {
	return r.accountsOf(hr.Spec.Values)
}

func (r *REST) accountsOf(values *apiextv1.JSON) ([]account.Account, error) {
	return store.ResolveAccounts(r.credentials, r.specSchema, values)
}

// invalidAccounts turns an account resolution failure into the Invalid the
// write is refused with.
func (r *REST) invalidAccounts(app *appsv1alpha1.Application, err error) error {
	return apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, field.ErrorList{
		field.Invalid(field.NewPath("spec"), "", err.Error()),
	})
}

func (r *REST) storeApp(namespace, name string) store.App {
	return store.App{
		Namespace: namespace,
		Release:   r.releaseConfig.Prefix + name,
		Group:     r.gvk.Group,
		Kind:      r.kindName,
		Name:      name,
		Block:     r.credentials,
	}
}

// actor names the caller for the record an account Secret keeps.
func actor(ctx context.Context) string {
	if user, ok := request.UserFrom(ctx); ok {
		return user.GetName()
	}
	return ""
}

// seedBeforeCreate writes the account Secrets of a new application, with no
// owner yet, and the valuesFrom entries that carry them into the chart.
// It returns the Secrets it wrote, for the owner to be set once the
// HelmRelease exists, or for removal if the HelmRelease is not created.
func (r *REST) seedBeforeCreate(ctx context.Context, app *appsv1alpha1.Application, hr *helmv2.HelmRelease, accounts []account.Account) ([]string, error) {
	if r.credentials == nil {
		return nil, nil
	}
	hr.Spec.ValuesFrom = store.ValuesFrom(r.credentials, hr.Name, accounts)
	written, err := store.Ensure(ctx, r.c, r.storeApp(app.Namespace, app.Name), accounts, nil, metav1.Now(), actor(ctx))
	if err != nil {
		r.unseed(ctx, app.Namespace, written)
		return nil, apierrors.NewInternalError(fmt.Errorf("seed account Secrets: %w", err))
	}
	return written, nil
}

// unseed removes the Secrets a create wrote before its HelmRelease failed, so
// a retry starts clean. Best effort: a Secret nobody owns is reused by the
// retry in any case.
func (r *REST) unseed(ctx context.Context, namespace string, names []string) {
	for _, name := range names {
		sec := &corev1.Secret{}
		sec.Namespace, sec.Name = namespace, name
		if err := r.c.Delete(ctx, sec); err != nil && !apierrors.IsNotFound(err) {
			klog.Warningf("failed to remove account Secret %s/%s after a failed create: %v", namespace, name, err)
		}
	}
}

// ownSeeded gives the created HelmRelease the account Secrets seeded before
// it. The application exists by now, so a failure is reported as a warning
// rather than an error: the next write of the application retries it.
func (r *REST) ownSeeded(ctx context.Context, hr *helmv2.HelmRelease, names []string) {
	if len(names) == 0 {
		return
	}
	if err := store.SetOwner(ctx, r.c, hr.Namespace, names, store.OwnerReference(hr)); err != nil {
		klog.Errorf("failed to own account Secrets of %s/%s: %v", hr.Namespace, hr.Name, err)
		warning.AddWarning(ctx, "", fmt.Sprintf("account Secrets were written but not yet owned by the release: %v", err))
	}
}

// seedBeforeUpdate writes the account Secrets an update adds, owned by the
// live HelmRelease from the start, and the valuesFrom entries of the result.
func (r *REST) seedBeforeUpdate(ctx context.Context, app *appsv1alpha1.Application, hr, cur *helmv2.HelmRelease, accounts []account.Account) error {
	if r.credentials == nil {
		return nil
	}
	hr.Spec.ValuesFrom = store.ValuesFrom(r.credentials, hr.Name, accounts)
	owner := store.OwnerReference(cur)
	if _, err := store.Ensure(ctx, r.c, r.storeApp(app.Namespace, app.Name), accounts, &owner, metav1.Now(), actor(ctx)); err != nil {
		return apierrors.NewInternalError(fmt.Errorf("seed account Secrets: %w", err))
	}
	return nil
}

// pruneAfterUpdate deletes the account Secrets of the accounts the update
// removed, once the HelmRelease write has dropped them from the chart.
func (r *REST) pruneAfterUpdate(ctx context.Context, app *appsv1alpha1.Application, accounts []account.Account) {
	if r.credentials == nil {
		return
	}
	if _, err := store.Prune(ctx, r.c, r.storeApp(app.Namespace, app.Name), accounts); err != nil {
		klog.Errorf("failed to prune account Secrets of %s/%s: %v", app.Namespace, app.Name, err)
		warning.AddWarning(ctx, "", fmt.Sprintf("account Secrets of removed users were not all deleted: %v", err))
	}
}

// refuseDeleteSuspended refuses to delete a converted application whose
// release is suspended: Flux skips the uninstall of a suspended release, so
// the database would keep running with its last passwords after the account
// Secrets are gone with the HelmRelease.
func (r *REST) refuseDeleteSuspended(name string, hr *helmv2.HelmRelease) error {
	if r.credentials == nil || !hr.Spec.Suspend {
		return nil
	}
	return apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
		"release %s is suspended, and deleting it would leave the database running after its account Secrets are gone; resume the release first", hr.Name))
}

// warnMintedPasswords warns about a password set in the values of a converted
// kind: the chart never reads it, since every password is issued by a mint.
func (r *REST) warnMintedPasswords(ctx context.Context, app *appsv1alpha1.Application) {
	if r.credentials == nil || app == nil || app.Spec == nil || len(app.Spec.Raw) == 0 {
		return
	}
	var values map[string]any
	if err := json.Unmarshal(app.Spec.Raw, &values); err != nil {
		return
	}
	release := r.releaseConfig.Prefix + app.Name
	for _, path := range account.ValuesMapPaths(r.credentials) {
		users, _ := account.Lookup(values, path).(map[string]any)
		for _, user := range sortedKeys(users) {
			u, _ := users[user].(map[string]any)
			if _, present := u["password"]; !present {
				continue
			}
			mapped, err := account.MapName(user)
			if err != nil {
				continue
			}
			warning.AddWarning(ctx, "", fmt.Sprintf(
				"spec.%s[%q].password is ignored: passwords are issued by a mint on the credential %s and cannot be set from values.",
				path, user, account.CredentialName(release, mapped)))
		}
	}
}
