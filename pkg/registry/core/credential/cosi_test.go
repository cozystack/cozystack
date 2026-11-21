// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

// cosiSeeded returns a cosi template with the account Secret the controller
// leaves after the first grant, and the status mirroring it.
func (f *fixture) cosiSeeded(t *testing.T) *cozyv1alpha1.CredentialTemplate {
	t.Helper()
	tmpl := template(cred, "web")
	tmpl.Spec.Format.Name = format.COSI
	tmpl.Spec.Secret = cozyv1alpha1.CredentialTemplateSecret{}
	tmpl.Spec.BucketAccess = &cozyv1alpha1.CredentialTemplateBucketAccess{Name: release + "-web", BucketClaimName: release, BucketAccessClassName: "seaweedfs"}
	if err := f.c.Create(f.ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(f.r.now())
	sec := buildSecret(tmpl, "first-secret", account.Record{}.Apply(corev1alpha1.CredentialOperationCreate, at, "controller"))
	sec.Data[account.AccessKeyIDKey] = []byte("AKIAFIRST")
	sec.Annotations[account.GrantedAnnotation] = ""
	if err := f.c.Create(f.ctx, sec); err != nil {
		t.Fatal(err)
	}
	tmpl.Status.SecretResourceVersion = sec.ResourceVersion
	if err := f.c.Status().Update(f.ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.writes = nil
	f.mu.Unlock()
	f.r.cosiPoll = 5 * time.Millisecond
	f.r.cosiWait = 2 * time.Second
	return tmpl
}

// serveGrants does what cozystack-controller does for each request a mint or
// a revoke writes: it stores a new key pair marked with the request id, and
// mirrors the Secret's version into the template's status. It stops with the
// test.
func (f *fixture) serveGrants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	key := types.NamespacedName{Namespace: ns, Name: cred + account.SecretSuffix}
	n := 0
	go func() {
		for ctx.Err() == nil {
			time.Sleep(5 * time.Millisecond)
			sec := &corev1.Secret{}
			if err := f.c.Get(ctx, key, sec); err != nil {
				continue
			}
			id := sec.Annotations[account.RequestAnnotation]
			if id == "" || sec.Annotations[account.GrantedAnnotation] == id {
				continue
			}
			n++
			sec.Data[account.PasswordKey] = []byte("secret-" + id)
			sec.Data[account.AccessKeyIDKey] = []byte("AKIA" + string(rune('A'+n)))
			sec.Annotations[account.GrantedAnnotation] = id
			if err := f.c.Update(ctx, sec); err != nil {
				continue
			}
			tmpl := &cozyv1alpha1.CredentialTemplate{}
			if err := f.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: cred}, tmpl); err == nil {
				tmpl.Status.SecretResourceVersion = sec.ResourceVersion
				_ = f.c.Status().Update(ctx, tmpl)
			}
		}
	}()
}

func TestCOSIMintReturnsTheKeysOfItsOwnGrantOnce(t *testing.T) {
	f := newFixture(t)
	f.cosiSeeded(t)
	f.serveGrants(t)

	first, err := f.do(NewMintREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	firstWrite := f.writes[0]
	f.mu.Unlock()
	if firstWrite != "update "+cred+account.SecretSuffix {
		t.Errorf("first write = %q, want the account Secret update", firstWrite)
	}
	if first.Status.Username != "web" || first.Status.Password == "" || first.Status.AccessKeyID == "" {
		t.Fatalf("answer = %+v, want the new key pair", first.Status)
	}
	if first.Status.Password == "first-secret" || first.Status.AccessKeyID == "AKIAFIRST" {
		t.Error("the mint returned the keys it replaced")
	}
	sec := f.secret(t, cred+account.SecretSuffix)
	if string(sec.Data[account.PasswordKey]) != first.Status.Password {
		t.Error("the answer is not what the Secret holds")
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginMinted || rec.By != "jane@example.org" {
		t.Errorf("record = %+v", rec)
	}

	second, err := f.do(NewMintREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status.Password == first.Status.Password || second.Status.AccessKeyID == first.Status.AccessKeyID {
		t.Error("a second mint showed the first mint's keys again")
	}
}

func TestCOSIRevokeReturnsNoKeys(t *testing.T) {
	f := newFixture(t)
	f.cosiSeeded(t)
	f.serveGrants(t)

	res, err := f.do(NewRevokeREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Password != "" || res.Status.AccessKeyID != "" {
		t.Errorf("answer = %+v, want no keys", res.Status)
	}
	sec := f.secret(t, cred+account.SecretSuffix)
	if string(sec.Data[account.PasswordKey]) == "first-secret" {
		t.Error("the revoked keys are still stored")
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginRevoked {
		t.Errorf("record = %+v, want Revoked", rec)
	}
}

func TestCOSIMintThatOutwaitsTheGrantShowsNothing(t *testing.T) {
	f := newFixture(t)
	f.cosiSeeded(t)
	f.r.cosiWait = 50 * time.Millisecond

	_, err := f.do(NewMintREST(f.r), cred, "", false)
	if !apierrors.IsTimeout(err) {
		t.Fatalf("err = %v, want a retryable timeout", err)
	}
	sec := f.secret(t, cred+account.SecretSuffix)
	if _, held := sec.Data[account.PasswordKey]; held {
		t.Error("the Secret still holds keys the mint replaced")
	}
	if _, held := sec.Data[account.AccessKeyIDKey]; held {
		t.Error("the Secret still holds an access key the mint replaced")
	}
	if sec.Annotations[account.RequestAnnotation] == "" {
		t.Error("the mint wrote no request for the controller")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 {
		t.Errorf("writes = %v, want the one Secret update", f.writes)
	}
}
