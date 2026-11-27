// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/util/dryrun"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

var (
	_ rest.NamedCreater             = &OperationREST{}
	_ rest.Scoper                   = &OperationREST{}
	_ rest.GroupVersionKindProvider = &OperationREST{}
)

// OperationREST is the mint or the revoke subresource of a Credential. Both
// take a CredentialRequest, build the whole account Secret from the template
// with the stored value of a fresh random password, and write it in one
// request: an update under the Secret version the template's status mirrors,
// or a create when the account has no Secret yet. The Secret is never read. A
// mint returns the password, once; a revoke discards it, so nobody holds a
// working one until the next mint.
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
func (o *OperationREST) Create(ctx context.Context, name string, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
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
	res, err := r.resolve(ctx, r.direct, ns, name)
	if err != nil {
		return nil, err
	}
	tmpl := res.tmpl
	if meta.IsStatusConditionTrue(tmpl.Status.Conditions, cozyv1alpha1.CredentialTemplateSecretConflict) {
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
			"credential %s has a Secret it did not create; a platform administrator has to remove it first", name))
	}
	// A template that names a BucketAccess is engine-issued: the COSI driver,
	// not the API, chooses its keys.
	engineIssued := tmpl.Spec.BucketAccess != nil
	if engineIssued && meta.IsStatusConditionTrue(tmpl.Status.Conditions, cozyv1alpha1.CredentialTemplateBucketAccessConflict) {
		// The controller leaves a BucketAccess it did not create alone, so
		// it could serve neither operation, and the request would only wait
		// out its timeout.
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
			"credential %s has a BucketAccess it did not create; a platform administrator has to remove it first", name))
	}
	if !format.Known(tmpl.Spec.Format.Name) {
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
			"credential %s names the format %q, which is not built in", name, tmpl.Spec.Format.Name))
	}

	var precondition string
	if req.Spec.Preconditions != nil {
		precondition = req.Spec.Preconditions.ResourceVersion
	}
	mirrored := tmpl.Status.SecretResourceVersion
	// The Credential's version is the account Secret's, as the template's
	// status mirrors it, so a caller that read the credential and sends that
	// version back learns, as a conflict, that somebody minted in between
	// instead of silently killing the password that person just received.
	if precondition != "" && precondition != mirrored {
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf(
			"credential %s changed: password %s by %s", name, describe(corev1alpha1.CredentialOrigin(tmpl.Status.Password.Origin)), tmpl.Status.Password.By))
	}

	if options != nil && dryrun.IsDryRun(options.DryRun) {
		// A dry run answers as the operation would, without a password: one
		// that is never stored would not log in.
		return &corev1alpha1.CredentialRequest{
			TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: "CredentialRequest"},
			Status:   corev1alpha1.CredentialRequestStatus{Username: tmpl.Spec.Username, IssuedAt: metav1.NewTime(r.now())},
		}, nil
	}

	if engineIssued {
		return o.regrant(ctx, name, res, user.GetName(), mirrored)
	}

	password, err := format.GeneratePassword()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	stored, err := format.Encode(tmpl.Spec.Format.Name, password)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	now := metav1.NewTime(r.now())
	by := user.GetName()
	sec := buildSecret(tmpl, stored, recordOf(tmpl).Apply(o.op, now, by))
	if err := o.write(ctx, name, sec, mirrored); err != nil {
		return nil, err
	}

	reason := ReasonCredentialIssued
	if o.op == corev1alpha1.CredentialOperationRevoke {
		reason = ReasonCredentialRevoked
		password = ""
	}
	Emit(ctx, r.direct, res.hr, reason, tmpl.Spec.Username, by)

	return &corev1alpha1.CredentialRequest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CredentialRequest",
		},
		Status: corev1alpha1.CredentialRequestStatus{
			Username: tmpl.Spec.Username,
			Password: password,
			IssuedAt: now,
		},
	}, nil
}

// write stores the account Secret under the version the template's status
// mirrors, or creates it when the account has none. The status is all the
// API knows of the Secret. A status that lags the Secret, or a Secret written
// since the status was read, makes the write fail rather than overwrite it,
// and the caller gets a conflict and no password.
func (o *OperationREST) write(ctx context.Context, name string, sec *corev1.Secret, mirrored string) error {
	r := o.parent
	if mirrored == "" {
		if err := r.direct.Create(ctx, sec); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf("credential %s is being seeded; retry", name))
			}
			return err
		}
		return nil
	}
	sec.ResourceVersion = mirrored
	if err := r.direct.Update(ctx, sec); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return apierrors.NewConflict(r.gvr.GroupResource(), name, fmt.Errorf("credential %s changed while the request was in flight; retry", name))
		}
		return err
	}
	return nil
}

// recordOf returns the record the template's status mirrors, which the mint
// extends with its own operation.
func recordOf(tmpl *cozyv1alpha1.CredentialTemplate) account.Record {
	st := tmpl.Status
	rec := account.Record{Origin: corev1alpha1.CredentialOrigin(st.Password.Origin), By: st.Password.By}
	if st.Password.At != nil {
		rec.At = *st.Password.At
	}
	for _, h := range st.History {
		rec.History = append(rec.History, corev1alpha1.CredentialHistoryEntry{Operation: corev1alpha1.CredentialOperation(h.Operation), At: h.At, By: h.By})
	}
	return rec
}

// buildSecret builds the whole account Secret of a template, as the
// controller would seed it, with the given stored value and record.
func buildSecret(tmpl *cozyv1alpha1.CredentialTemplate, stored string, rec account.Record) *corev1.Secret {
	controller := true
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        tmpl.Name + account.SecretSuffix,
			Namespace:   tmpl.Namespace,
			Labels:      account.Labels(tmpl.Spec.Secret.Labels),
			Annotations: map[string]string{account.FormatAnnotation: tmpl.Spec.Format.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         cozyv1alpha1.GroupVersion.String(),
				Kind:               "CredentialTemplate",
				Name:               tmpl.Name,
				UID:                tmpl.UID,
				Controller:         &controller,
				BlockOwnerDeletion: &controller,
			}},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			account.UsernameKey: []byte(tmpl.Spec.Username),
			account.PasswordKey: []byte(stored),
		},
	}
	account.WriteRecord(sec, rec)
	return sec
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
