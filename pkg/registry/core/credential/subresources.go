// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
	"github.com/cozystack/cozystack/pkg/credentials/store"
)

var (
	_ rest.NamedCreater             = &OperationREST{}
	_ rest.Scoper                   = &OperationREST{}
	_ rest.GroupVersionKindProvider = &OperationREST{}
)

// OperationREST is the mint or the revoke subresource of a Credential. Both
// take a CredentialRequest, replace the account's stored value with that of a
// fresh random password in one conditional write of the account Secret, and
// answer with a CredentialRequest. A mint returns the password, once; a
// revoke discards it, so nobody holds a working one until the next mint.
type OperationREST struct {
	parent *REST
	op     corev1alpha1.CredentialOperation
}

// NewMintREST and NewRevokeREST return the two subresource storages of a
// credentials registry.
func NewMintREST(parent *REST) *OperationREST {
	return &OperationREST{parent: parent, op: corev1alpha1.CredentialOperationMint}
}

func NewRevokeREST(parent *REST) *OperationREST {
	return &OperationREST{parent: parent, op: corev1alpha1.CredentialOperationRevoke}
}

func (*OperationREST) New() runtime.Object   { return &corev1alpha1.CredentialRequest{} }
func (*OperationREST) Destroy()              {}
func (*OperationREST) NamespaceScoped() bool { return true }
func (o *OperationREST) GroupVersionKind(schema.GroupVersion) schema.GroupVersionKind {
	return o.parent.gvr.GroupVersion().WithKind("CredentialRequest")
}

// Create performs the operation on the named Credential.
func (o *OperationREST) Create(ctx context.Context, name string, obj runtime.Object, createValidation rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	req, ok := obj.(*corev1alpha1.CredentialRequest)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected CredentialRequest, got %T", obj))
	}
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	user, ok := request.UserFrom(ctx)
	if !ok {
		return nil, apierrors.NewUnauthorized("user missing in context")
	}
	if createValidation != nil {
		if err := createValidation(ctx, req); err != nil {
			return nil, err
		}
	}

	r := o.parent
	res, err := r.resolve(ctx, ns, name)
	if err != nil {
		return nil, err
	}

	var precondition string
	if req.Spec.Preconditions != nil {
		precondition = req.Spec.Preconditions.ResourceVersion
	}

	// The Secret's version is the Credential's, so a caller that read the
	// credential and sends that version back learns, as a conflict, that
	// somebody minted in between instead of silently killing the password that
	// person just received. A missing Secret takes no precondition.
	if res.secret != nil && precondition != "" && precondition != res.secret.ResourceVersion {
		rec := account.ReadRecord(res.secret)
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
			"credential %s changed: password %s by %s at %s", name, describe(rec.Origin), rec.By, rec.At.UTC().Format("2006-01-02T15:04:05Z")))
	}

	password, err := format.GeneratePassword()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	stored, err := format.Encode(res.kind.Block.Format.Name, password)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	now := metav1.NewTime(r.now())
	by := user.GetName()

	var username string
	if res.secret != nil {
		username = string(res.secret.Data[account.UsernameKey])
		sec := res.secret.DeepCopy()
		sec.Data[account.PasswordKey] = []byte(stored)
		account.WriteRecord(sec, account.ReadRecord(sec).Apply(o.op, now, by))
		// The update carries the Secret's own resourceVersion, so two concurrent
		// operations cannot both land: the second gets a conflict and no password.
		if err := r.c.Update(ctx, sec); err != nil {
			if apierrors.IsConflict(err) {
				return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf("credential %s changed while the request was in flight", name))
			}
			return nil, err
		}
	} else {
		// A declared account whose Secret is missing, as after a conversion
		// that left the release out, gets a Secret with a new identity. One that
		// is not declared does not exist.
		acc, err := o.declared(ctx, res)
		if err != nil {
			return nil, err
		}
		username = acc.Username
		app := store.App{
			Namespace: ns, Release: res.hr.Name,
			Group: res.hr.Labels[appsv1alpha1.ApplicationGroupLabel],
			Kind:  res.hr.Labels[appsv1alpha1.ApplicationKindLabel],
			Name:  res.hr.Labels[appsv1alpha1.ApplicationNameLabel],
			Block: res.kind.Block,
		}
		if res.stale != nil {
			if err := r.c.Delete(ctx, res.stale, client.Preconditions{UID: &res.stale.UID}); err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
		}
		owner := store.OwnerReference(res.hr)
		sec, err := store.SeedSecret(app, acc, &owner, now, by)
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}
		sec.Data[account.PasswordKey] = []byte(stored)
		account.WriteRecord(sec, account.Record{}.Apply(o.op, now, by))
		if err := r.c.Create(ctx, sec); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf("credential %s was created while the request was in flight", name))
			}
			return nil, err
		}
	}

	reason := store.ReasonCredentialIssued
	if o.op == corev1alpha1.CredentialOperationRevoke {
		reason = store.ReasonCredentialRevoked
		password = ""
	}
	store.Emit(ctx, r.c, res.hr, reason, username, by)

	return &corev1alpha1.CredentialRequest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CredentialRequest",
		},
		Status: corev1alpha1.CredentialRequestStatus{
			Username: username,
			Password: password,
			IssuedAt: now,
		},
	}, nil
}

// declared finds the declared account a Credential name maps to. A name no
// declared user maps to is NotFound; one two declared users map to is a
// Conflict, since the write that declared them should have been refused.
func (o *OperationREST) declared(ctx context.Context, res *resolved) (account.Account, error) {
	r := o.parent
	name := account.CredentialName(res.hr.Name, res.mapped)
	if res.kind.Accounts == nil {
		return account.Account{}, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	accounts, err := res.kind.Accounts(ctx, res.hr)
	if err != nil {
		return account.Account{}, apierrors.NewConflict(r.gvr.GroupResource(), name, err)
	}
	for _, acc := range accounts {
		if acc.Mapped == res.mapped {
			return acc, nil
		}
	}
	return account.Account{}, apierrors.NewNotFound(r.gvr.GroupResource(), name)
}

func describe(origin corev1alpha1.CredentialOrigin) string {
	switch origin {
	case corev1alpha1.CredentialOriginMinted:
		return "issued"
	case corev1alpha1.CredentialOriginRevoked:
		return "revoked"
	case corev1alpha1.CredentialOriginMigrated:
		return "migrated"
	}
	return "seeded"
}
