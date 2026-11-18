// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"encoding/json"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/applock"
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

// direct is the client the account Secret writes read through. The cached
// client can miss an object written a moment ago, and a stale read here
// replaces or recreates a Secret that exists.
func (r *REST) direct() client.Client {
	if r.w != nil {
		return r.w
	}
	return r.c
}

// lockCredentials takes the per-application lock so this request's seeding and
// pruning do not interleave with a concurrent write to the same application. It
// returns a release function the caller defers. A lock another request holds is
// a retryable Conflict; a lock store that cannot be reached is logged and the
// request proceeds without it, since the lock only guards a rare race and its
// store being unreachable is no worse than not having it.
func (r *REST) lockCredentials(ctx context.Context, app *appsv1alpha1.Application) (func(), error) {
	if r.credentials == nil || r.lock == nil {
		return func() {}, nil
	}
	unlock, err := r.lock.Acquire(ctx, app.Namespace, r.storeApp(app.Namespace, app.Name).Release)
	switch {
	case errors.Is(err, applock.ErrHeld):
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), app.Name, errors.New("another change to this application's credentials is in progress; retry"))
	case err != nil:
		klog.Warningf("credential lock unavailable for %s/%s, proceeding without it: %v", app.Namespace, app.Name, err)
		return func() {}, nil
	}
	return unlock, nil
}

// seedBeforeCreate writes the account Secrets of a new application, with no
// owner yet, and the valuesFrom entries that carry them into the chart.
// It returns the Secrets it wrote, for the owner to be set once the
// HelmRelease exists, or for removal if the HelmRelease is not created.
// An existing release fails the create before any Secret is touched, since
// its own Secrets would otherwise be replaced.
func (r *REST) seedBeforeCreate(ctx context.Context, app *appsv1alpha1.Application, hr *helmv2.HelmRelease, accounts []account.Account, dryRun bool) ([]store.Seeded, error) {
	if r.credentials == nil {
		return nil, nil
	}
	hr.Spec.ValuesFrom = store.ValuesFrom(r.credentials, hr.Name, accounts)
	existing := &helmv2.HelmRelease{}
	err := r.direct().Get(ctx, client.ObjectKey{Namespace: hr.Namespace, Name: hr.Name}, existing)
	switch {
	case err == nil:
		return nil, apierrors.NewAlreadyExists(r.gvr.GroupResource(), app.Name)
	case !apierrors.IsNotFound(err):
		return nil, apierrors.NewInternalError(fmt.Errorf("read HelmRelease %s: %w", hr.Name, err))
	}
	if dryRun {
		return nil, nil
	}
	written, err := store.Ensure(ctx, r.direct(), r.storeApp(app.Namespace, app.Name), accounts, nil, metav1.Now(), actor(ctx))
	if err != nil {
		r.unseed(ctx, app.Namespace, hr.Name, written)
		if errors.Is(err, store.ErrLiveOwner) {
			return nil, apierrors.NewAlreadyExists(r.gvr.GroupResource(), app.Name)
		}
		return nil, apierrors.NewInternalError(fmt.Errorf("seed account Secrets: %w", err))
	}
	return written, nil
}

// unseed removes the Secrets a create wrote before its HelmRelease failed, so
// a retry starts clean. Ownership of an application's account Secrets follows
// its HelmRelease, not a Secret's identity: once a release exists, a concurrent
// create won it and owns the Secrets, and may have adopted by name the very one
// this create seeded, so unseed deletes nothing. A create that lost the race
// reaches here only after its own create failed with AlreadyExists, by when
// that release provably exists, and the read is uncached so it is seen. Only a
// create that left no release behind deletes its orphaned seed, and then only
// the exact object it wrote, so a concurrent retry's fresh Secret survives.
func (r *REST) unseed(ctx context.Context, namespace, release string, seeded []store.Seeded) {
	hr := &helmv2.HelmRelease{}
	switch err := r.direct().Get(ctx, client.ObjectKey{Namespace: namespace, Name: release}, hr); {
	case err == nil:
		return
	case !apierrors.IsNotFound(err):
		klog.Warningf("failed to check HelmRelease %s/%s before cleaning up a failed create; leaving its account Secrets: %v", namespace, release, err)
		return
	}
	for _, s := range seeded {
		cur := &corev1.Secret{}
		if err := r.direct().Get(ctx, client.ObjectKey{Namespace: namespace, Name: s.Name}, cur); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.Warningf("failed to read account Secret %s/%s before removing it after a failed create: %v", namespace, s.Name, err)
			}
			continue
		}
		if cur.UID != s.UID {
			continue
		}
		rv := cur.ResourceVersion
		if err := r.direct().Delete(ctx, cur, client.Preconditions{UID: &s.UID, ResourceVersion: &rv}); err != nil &&
			!apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			klog.Warningf("failed to remove account Secret %s/%s after a failed create: %v", namespace, s.Name, err)
		}
	}
}

// ownSeeded gives the created HelmRelease the account Secrets seeded before
// it. The application exists by now, so a failure is reported as a warning
// rather than an error.
func (r *REST) ownSeeded(ctx context.Context, hr *helmv2.HelmRelease, seeded []store.Seeded) {
	if len(seeded) == 0 {
		return
	}
	if err := store.SetOwner(ctx, r.direct(), hr.Namespace, seeded, store.OwnerReference(hr)); err != nil {
		klog.Errorf("failed to own account Secrets of %s/%s: %v", hr.Namespace, hr.Name, err)
		warning.AddWarning(ctx, "", fmt.Sprintf("account Secrets were written but not yet owned by the release: %v", err))
	}
}

// seedBeforeUpdate writes the account Secrets of the accounts the update adds,
// owned by the live HelmRelease from the start, and the valuesFrom entries of
// the result. An account the release already declared and that has no Secret
// is left alone: a seed would replace its working password with a discarded
// one, and a mint creates the Secret when someone needs it.
func (r *REST) seedBeforeUpdate(ctx context.Context, app *appsv1alpha1.Application, hr, cur *helmv2.HelmRelease, accounts []account.Account, dryRun bool) error {
	if r.credentials == nil {
		return nil
	}
	hr.Spec.ValuesFrom = store.ValuesFrom(r.credentials, hr.Name, accounts)
	if dryRun {
		return nil
	}
	prev, err := r.accountsOf(cur.Spec.Values)
	if err != nil {
		// The stored values were written around the API; nothing tells which
		// accounts are new, so seed none and leave the rest to a mint.
		klog.Warningf("accounts of %s/%s before the update cannot be resolved, seeding none: %v", cur.Namespace, cur.Name, err)
		return nil
	}
	release := r.storeApp(app.Namespace, app.Name).Release
	added := store.Added(prev, accounts)
	// An added account whose mapped name already holds a different account is a
	// rename that changed only the case of a user name: both map to one Secret.
	// Seeding it would replace the live account's Secret with a new password
	// before the release write, and a failed write would lose that password with
	// no way back. Refuse it, since the live account is not actually gone.
	for _, acc := range added {
		ex := &corev1.Secret{}
		switch err := r.direct().Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: account.SecretName(release, acc.Mapped)}, ex); {
		case err == nil && account.IsAccountSecret(ex) && string(ex.Data[account.UsernameKey]) != acc.Username:
			return r.invalidAccounts(app, fmt.Errorf("account %q maps to the name %q, which already belongs to account %q; rename it to a name that does not collide", acc.Username, acc.Mapped, string(ex.Data[account.UsernameKey])))
		case err != nil && !apierrors.IsNotFound(err):
			return apierrors.NewInternalError(fmt.Errorf("read account Secret %s: %w", account.SecretName(release, acc.Mapped), err))
		}
	}
	owner := store.OwnerReference(cur)
	if _, err := store.Ensure(ctx, r.direct(), r.storeApp(app.Namespace, app.Name), added, &owner, metav1.Now(), actor(ctx)); err != nil {
		return apierrors.NewInternalError(fmt.Errorf("seed account Secrets: %w", err))
	}
	return nil
}

// pruneAfterUpdate deletes the account Secrets of the accounts the update
// removed, once the HelmRelease write has dropped them from the chart.
func (r *REST) pruneAfterUpdate(ctx context.Context, app *appsv1alpha1.Application, dryRun bool) {
	if r.credentials == nil || dryRun {
		return
	}
	// Prune by the accounts the live release declares now, not the ones this
	// request sent: a concurrent update can have re-added an account and written
	// it back, and its Secret is referenced by the live release, so deleting it
	// by this request's older list would break the release. Read uncached, and
	// prune nothing when the release or its accounts cannot be resolved.
	cur := &helmv2.HelmRelease{}
	switch err := r.direct().Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: r.storeApp(app.Namespace, app.Name).Release}, cur); {
	case apierrors.IsNotFound(err):
		return
	case err != nil:
		klog.Warningf("failed to read the live HelmRelease of %s/%s before pruning, pruning none: %v", app.Namespace, app.Name, err)
		return
	}
	keep, err := r.accountsOf(cur.Spec.Values)
	if err != nil {
		klog.Warningf("accounts of the live %s/%s cannot be resolved, pruning none: %v", app.Namespace, app.Name, err)
		return
	}
	if _, err := store.Prune(ctx, r.direct(), r.storeApp(app.Namespace, app.Name), keep); err != nil {
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
