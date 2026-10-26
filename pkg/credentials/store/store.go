// SPDX-License-Identifier: Apache-2.0

// Package store writes and removes account Secrets around a HelmRelease write:
// it seeds one per account, prunes the ones a write removes, and builds the
// valuesFrom entries that carry a stored value into a chart whose operator
// cannot read a Secret.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

// App identifies the application whose account Secrets are written.
type App struct {
	Namespace string
	// Release is the HelmRelease name, prefix plus application name.
	Release string
	Group   string
	Kind    string
	Name    string
	Block   *cozyv1alpha1.ApplicationDefinitionCredentials
}

// Labels returns the labels every account Secret of the application carries.
func (a App) Labels() map[string]string {
	return account.Labels(a.Block, a.Group, a.Kind, a.Name)
}

// OwnerReference builds the HelmRelease owner reference the account Secrets
// carry, so they are collected with the release.
func OwnerReference(hr *helmv2.HelmRelease) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: helmv2.GroupVersion.String(),
		Kind:       helmv2.HelmReleaseKind,
		Name:       hr.Name,
		UID:        hr.UID,
	}
}

// SeedSecret builds the account Secret of a new account: the stored form of
// a random password that is discarded here, so the account exists in the
// engine while nobody can log in until a mint. owner may be nil on a create,
// where the HelmRelease does not exist yet and the reference is set after.
func SeedSecret(app App, acc account.Account, owner *metav1.OwnerReference, now metav1.Time, by string) (*corev1.Secret, error) {
	password, err := format.GeneratePassword()
	if err != nil {
		return nil, err
	}
	stored, err := format.Encode(app.Block.Format.Name, password)
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      account.SecretName(app.Release, acc.Mapped),
			Namespace: app.Namespace,
			Labels:    app.Labels(),
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			account.UsernameKey: []byte(acc.Username),
			account.PasswordKey: []byte(stored),
		},
	}
	if owner != nil {
		sec.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	account.WriteRecord(sec, account.Record{}.Apply(corev1alpha1.CredentialOperationCreate, now, by))
	return sec, nil
}

// Theirs reports whether an existing Secret is the application's own account
// Secret: marker, same username, owned by the live HelmRelease. With no
// HelmRelease yet (a create), one any HelmRelease owns is left from a deleted
// application of the same name and is replaced, so the account gets a new
// identity; one nobody owns is from a create that did not finish.
func Theirs(sec *corev1.Secret, acc account.Account, owner *metav1.OwnerReference) bool {
	if !account.IsAccountSecret(sec) || string(sec.Data[account.UsernameKey]) != acc.Username {
		return false
	}
	for _, ref := range sec.OwnerReferences {
		if owner == nil && ref.Kind == helmv2.HelmReleaseKind {
			return false
		}
		if owner != nil && ref.UID == owner.UID {
			return true
		}
	}
	return owner == nil
}

// Ensure writes an account Secret for every account that has none, or whose
// Secret is not the application's, and returns the names of the Secrets it
// wrote. It never touches a Secret that is the application's own, so a
// password already minted survives every later write of the application.
func Ensure(ctx context.Context, c client.Client, app App, accounts []account.Account, owner *metav1.OwnerReference, now metav1.Time, by string) ([]string, error) {
	var written []string
	for _, acc := range accounts {
		name := account.SecretName(app.Release, acc.Mapped)
		cur := &corev1.Secret{}
		err := c.Get(ctx, types.NamespacedName{Namespace: app.Namespace, Name: name}, cur)
		switch {
		case err == nil && Theirs(cur, acc, owner):
			continue
		case err == nil:
			if err := c.Delete(ctx, cur, client.Preconditions{UID: &cur.UID}); err != nil && !apierrors.IsNotFound(err) {
				return written, fmt.Errorf("replace account Secret %s: %w", name, err)
			}
		case !apierrors.IsNotFound(err):
			return written, fmt.Errorf("read account Secret %s: %w", name, err)
		}
		sec, err := SeedSecret(app, acc, owner, now, by)
		if err != nil {
			return written, fmt.Errorf("seed account %s: %w", acc.Username, err)
		}
		if err := c.Create(ctx, sec); err != nil {
			return written, fmt.Errorf("create account Secret %s: %w", name, err)
		}
		written = append(written, name)
	}
	return written, nil
}

// SetOwner adds the HelmRelease as owner of the named account Secrets. It is
// the second half of a create, once the HelmRelease exists.
func SetOwner(ctx context.Context, c client.Client, namespace string, names []string, owner metav1.OwnerReference) error {
	for _, name := range names {
		sec := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, sec); err != nil {
			return fmt.Errorf("read account Secret %s: %w", name, err)
		}
		owned := false
		for _, ref := range sec.OwnerReferences {
			if ref.UID == owner.UID {
				owned = true
			}
		}
		if owned {
			continue
		}
		patch := client.MergeFrom(sec.DeepCopy())
		sec.OwnerReferences = append(sec.OwnerReferences, owner)
		if err := c.Patch(ctx, sec, patch); err != nil {
			return fmt.Errorf("own account Secret %s: %w", name, err)
		}
	}
	return nil
}

// List returns the account Secrets of the application.
func List(ctx context.Context, c client.Client, app App) ([]corev1.Secret, error) {
	list := &corev1.SecretList{}
	if err := c.List(ctx, list, client.InNamespace(app.Namespace), client.MatchingLabels{
		account.AccountLabel:               "true",
		appsv1alpha1.ApplicationGroupLabel: app.Group,
		appsv1alpha1.ApplicationKindLabel:  app.Kind,
		appsv1alpha1.ApplicationNameLabel:  app.Name,
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Prune deletes the account Secrets of the application that no account in
// keep names, and returns the names it deleted. It runs after the HelmRelease
// write that removed the accounts, so the chart has already dropped them.
func Prune(ctx context.Context, c client.Client, app App, keep []account.Account) ([]string, error) {
	kept := map[string]bool{}
	for _, acc := range keep {
		kept[account.SecretName(app.Release, acc.Mapped)] = true
	}
	secrets, err := List(ctx, c, app)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for i := range secrets {
		sec := &secrets[i]
		if kept[sec.Name] {
			continue
		}
		if err := c.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}); err != nil && !apierrors.IsNotFound(err) {
			return deleted, fmt.Errorf("delete account Secret %s: %w", sec.Name, err)
		}
		deleted = append(deleted, sec.Name)
	}
	return deleted, nil
}

// DigestKey is the values key one account's stored value lands under: a hex
// digest of the user name, because a user name fits neither a targetPath nor
// a valuesKey, while a digest fits both and leaves no order to keep aligned.
func DigestKey(username string) string {
	sum := sha256.Sum256([]byte(username))
	return "u" + hex.EncodeToString(sum[:])
}

// ValuesFrom returns the HelmRelease valuesFrom entries of an application: the
// platform values, then for a kind with a valuesPath one entry per account
// reading the Secret's stored value into <valuesPath>.<DigestKey>. The API and
// the ApplicationDefinition reconciler must build the same list.
func ValuesFrom(block *cozyv1alpha1.ApplicationDefinitionCredentials, release string, accounts []account.Account) []helmv2.ValuesReference {
	refs := []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}}
	if block == nil || block.ValuesPath == "" {
		return refs
	}
	for _, acc := range accounts {
		refs = append(refs, helmv2.ValuesReference{
			Kind:       "Secret",
			Name:       account.SecretName(release, acc.Mapped),
			ValuesKey:  account.PasswordKey,
			TargetPath: block.ValuesPath + "." + DigestKey(acc.Username),
		})
	}
	return refs
}
