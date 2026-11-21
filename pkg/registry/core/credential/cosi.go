// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

// DefaultCOSIWait is how long a mint or a revoke of a cosi credential waits
// for its new grant unless cozystack-api is told otherwise. A COSI grant
// usually takes seconds.
const DefaultCOSIWait = 60 * time.Second

// regrant performs a mint or a revoke on an account whose keys the COSI
// driver issues, which the API cannot choose. It writes the account Secret,
// rebuilt from the template without keys and carrying a fresh request id, in
// the one write every operation makes; cozystack-controller serves the
// request by re-creating the BucketAccess, which revokes the keys the account
// held, and copies the new ones into the Secret marked with that id. The
// request waits for that mark and reads the keys back. A mint returns them,
// once: no other request carries the id, so no later caller is shown them. A
// revoke returns none. A wait that runs out answers a retryable timeout and
// shows nothing; the next mint re-creates the BucketAccess again.
func (o *OperationREST) regrant(ctx context.Context, name string, res *resolved, by, mirrored string) (*corev1alpha1.CredentialRequest, error) {
	r := o.parent
	tmpl := res.tmpl
	id, err := requestID()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	now := metav1.NewTime(r.now())
	sec := buildSecret(tmpl, "", recordOf(tmpl).Apply(o.op, now, by))
	delete(sec.Data, account.PasswordKey)
	sec.Annotations[account.RequestAnnotation] = id
	if err := o.write(ctx, name, sec, mirrored); err != nil {
		return nil, err
	}

	reason := ReasonCredentialIssued
	if o.op == corev1alpha1.CredentialOperationRevoke {
		reason = ReasonCredentialRevoked
	}
	Emit(ctx, r.direct, res.hr, reason, tmpl.Spec.Username, by)

	issued, err := o.awaitGrant(ctx, name, types.NamespacedName{Namespace: sec.Namespace, Name: sec.Name}, id)
	if err != nil {
		return nil, err
	}
	if issued == nil {
		return nil, apierrors.NewTimeoutError(fmt.Sprintf(
			"credential %s: the new keys were not issued within %s; they will not be shown, mint again", name, r.cosiWait), 1)
	}

	out := &corev1alpha1.CredentialRequest{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.SchemeGroupVersion.String(), Kind: "CredentialRequest"},
		Status:   corev1alpha1.CredentialRequestStatus{Username: tmpl.Spec.Username, IssuedAt: now},
	}
	if o.op == corev1alpha1.CredentialOperationMint {
		out.Status.Password = string(issued.Data[account.PasswordKey])
		out.Status.AccessKeyID = string(issued.Data[account.AccessKeyIDKey])
	}
	return out, nil
}

// awaitGrant polls the account Secret, uncached, until the controller marks
// it as issued for the request id, and returns it; nil when the wait runs
// out. A Secret marked for another id means a later request took over, which
// is a conflict for this one.
func (o *OperationREST) awaitGrant(ctx context.Context, name string, key types.NamespacedName, id string) (*corev1.Secret, error) {
	r := o.parent
	var issued *corev1.Secret
	var lost bool
	err := wait.PollUntilContextTimeout(ctx, r.cosiPoll, r.cosiWait, false, func(ctx context.Context) (bool, error) {
		sec := &corev1.Secret{}
		if err := r.direct.Get(ctx, key, sec); err != nil {
			return false, client404(err)
		}
		if sec.Annotations[account.RequestAnnotation] != id {
			lost = true
			return true, nil
		}
		if sec.Annotations[account.GrantedAnnotation] == id && len(sec.Data[account.PasswordKey]) > 0 {
			issued = sec
			return true, nil
		}
		return false, nil
	})
	if lost {
		return nil, apierrors.NewConflict(r.gvr.GroupResource(), name,
			fmt.Errorf("another request on this credential took over before its keys were issued; retry"))
	}
	if issued != nil {
		return issued, nil
	}
	if err != nil && !wait.Interrupted(err) {
		return nil, err
	}
	return nil, nil
}

// client404 keeps polling through a Secret that is momentarily missing.
func client404(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func requestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
