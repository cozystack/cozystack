// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cosiapi "sigs.k8s.io/container-object-storage-interface-api/apis"
	cosiv1alpha1 "sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage/v1alpha1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

const (
	cosiRelease = "bucket-photos"
	cosiName    = cosiRelease + ".web"
	cosiAccess  = cosiRelease + "-web"
	cosiClass   = "seaweedfs"
)

type cosiFixture struct {
	c        client.Client
	r        *COSICredentialReconciler
	recorder *record.FakeRecorder
	uids     int
}

func newCOSIFixture(t *testing.T, objs ...client.Object) *cosiFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, cozyv1alpha1.AddToScheme, cosiv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&cozyv1alpha1.CredentialTemplate{}, &cosiv1alpha1.BucketAccess{}).
		Build()
	rec := record.NewFakeRecorder(10)
	return &cosiFixture{
		c:        c,
		recorder: rec,
		r: &COSICredentialReconciler{
			Client: c, Reader: c, Scheme: scheme, Recorder: rec,
			Now: func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) },
		},
	}
}

func cosiTemplate() *cozyv1alpha1.CredentialTemplate {
	return &cozyv1alpha1.CredentialTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: cosiName, Namespace: ctNamespace, UID: "cosi-tmpl-uid",
			Annotations: map[string]string{account.ReleaseNameAnnotation: cosiRelease},
		},
		Spec: cozyv1alpha1.CredentialTemplateSpec{
			Username: "web",
			Format:   cozyv1alpha1.CredentialTemplateFormat{Name: format.Plaintext},
			BucketAccess: &cozyv1alpha1.CredentialTemplateBucketAccess{
				Name: cosiAccess, BucketClaimName: cosiRelease, BucketAccessClassName: cosiClass,
			},
		},
	}
}

func (f *cosiFixture) reconcile(t *testing.T) reconcile.Result {
	t.Helper()
	res, err := f.r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ctNamespace, Name: cosiName}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (f *cosiFixture) bucketAccess(t *testing.T) *cosiv1alpha1.BucketAccess {
	t.Helper()
	ba := &cosiv1alpha1.BucketAccess{}
	err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: cosiAccess}, ba)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return ba
}

func (f *cosiFixture) account(t *testing.T) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: cosiName + account.SecretSuffix}, sec)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

// grant does what the COSI sidecar does for a BucketAccess: it writes the
// Secret holding the BucketInfo of the grant, puts its finalizer on the
// BucketAccess and reports access granted. The fake client assigns no UID, so
// grant gives the BucketAccess one.
func (f *cosiFixture) grant(t *testing.T, accessKey, secretKey string) {
	t.Helper()
	f.grantUnfinalized(t, accessKey, secretKey)
	ctx := context.Background()
	ba := f.bucketAccess(t)
	ba.Finalizers = append(ba.Finalizers, bucketAccessFinalizer)
	if err := f.c.Update(ctx, ba); err != nil {
		t.Fatal(err)
	}
	ba.Status.AccessGranted = true
	ba.Status.AccountID = "ba-" + string(ba.UID)
	if err := f.c.Status().Update(ctx, ba); err != nil {
		t.Fatal(err)
	}
}

// grantUnfinalized stops the sidecar's grant where the driver has issued the
// keys and their Secret is written, before the finalizer goes on.
func (f *cosiFixture) grantUnfinalized(t *testing.T, accessKey, secretKey string) {
	t.Helper()
	ctx := context.Background()
	ba := f.bucketAccess(t)
	if ba == nil {
		t.Fatal("no BucketAccess to grant")
	}
	if ba.UID == "" {
		f.uids++
		ba.UID = types.UID("ba-uid-" + string(rune('a'+f.uids)))
		if err := f.c.Update(ctx, ba); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := json.Marshal(cosiapi.BucketInfo{
		ObjectMeta: metav1.ObjectMeta{Name: bucketInfoPrefix + string(ba.UID)},
		Spec: cosiapi.BucketInfoSpec{
			BucketName: "bucket-claim-uid",
			S3:         &cosiapi.SecretS3{Endpoint: "https://s3.example.org", AccessKeyID: accessKey, AccessSecretKey: secretKey},
		},
	})
	src := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ba.Spec.CredentialsSecretName, Namespace: ctNamespace}, Data: map[string][]byte{bucketInfoKey: info}}
	_ = f.c.Delete(ctx, src)
	if err := f.c.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
}

// finalize does what the COSI sidecar does once a BucketAccess is being
// deleted: the driver revokes the identity, and the sidecar removes the
// Secret it wrote and its finalizer, which lets the deletion complete.
func (f *cosiFixture) finalize(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	ba := f.bucketAccess(t)
	if ba == nil || ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want one being deleted", ba)
	}
	_ = f.c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ba.Spec.CredentialsSecretName, Namespace: ctNamespace}})
	ba.Finalizers = nil
	if err := f.c.Update(ctx, ba); err != nil {
		t.Fatal(err)
	}
	if f.bucketAccess(t) != nil {
		t.Fatal("the BucketAccess outlived its finalizer")
	}
}

func (f *cosiFixture) template(t *testing.T) *cozyv1alpha1.CredentialTemplate {
	t.Helper()
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: cosiName}, tmpl); err != nil {
		t.Fatal(err)
	}
	return tmpl
}

// request writes the account Secret the way a mint or a revoke does: rebuilt
// without keys, carrying a request id and the operation in its record.
func (f *cosiFixture) createRequest(t *testing.T, id string, op corev1alpha1.CredentialOperation) {
	t.Helper()
	controller := true
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: cosiName + account.SecretSuffix, Namespace: ctNamespace,
			Labels:      account.Labels(nil),
			Annotations: map[string]string{account.FormatAnnotation: format.Plaintext, account.BucketAccessAnnotation: cosiAccess, account.RequestAnnotation: id},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: cozyv1alpha1.GroupVersion.String(), Kind: "CredentialTemplate",
				Name: cosiName, UID: "cosi-tmpl-uid", Controller: &controller,
			}},
		},
		Data: map[string][]byte{account.UsernameKey: []byte("web")},
	}
	account.WriteRecord(sec, account.Record{}.Apply(op, metav1.NewTime(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)), "jane@example.org"))
	if err := f.c.Create(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
}

func (f *cosiFixture) request(t *testing.T, id string, op corev1alpha1.CredentialOperation) {
	t.Helper()
	sec := f.account(t)
	rec := account.ReadRecord(sec).Apply(op, metav1.NewTime(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)), "jane@example.org")
	updated := sec.DeepCopy()
	updated.Data = map[string][]byte{account.UsernameKey: []byte("web")}
	updated.Annotations = map[string]string{account.FormatAnnotation: format.Plaintext, account.BucketAccessAnnotation: cosiAccess, account.RequestAnnotation: id}
	account.WriteRecord(updated, rec)
	if err := f.c.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
}

func TestCOSICreatesTheBucketAccessOfATemplate(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	if res := f.reconcile(t); res.RequeueAfter == 0 {
		t.Error("expected a requeue while the grant is pending")
	}
	ba := f.bucketAccess(t)
	if ba == nil {
		t.Fatal("no BucketAccess was created")
	}
	if ba.Spec.BucketClaimName != cosiRelease || ba.Spec.BucketAccessClassName != cosiClass || ba.Spec.CredentialsSecretName != cosiAccess || ba.Spec.Protocol != "s3" {
		t.Errorf("spec = %+v", ba.Spec)
	}
	if ref := metav1.GetControllerOf(ba); ref == nil || ref.Name != cosiName || ref.Kind != "CredentialTemplate" {
		t.Errorf("controller = %v, want the template", ref)
	}
	if v, ok := ba.Annotations[account.RequestAnnotation]; !ok || v != "" {
		t.Errorf("request annotation = %q, %v; want present and empty", v, ok)
	}
	if f.account(t) != nil {
		t.Error("an account Secret exists before the grant")
	}
}

func TestCOSICopiesTheIssuedKeysIntoTheAccountSecret(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	sec := f.account(t)
	if sec == nil {
		t.Fatal("no account Secret")
	}
	if string(sec.Data[account.UsernameKey]) != "web" || string(sec.Data[account.PasswordKey]) != "first-secret" || string(sec.Data[account.AccessKeyIDKey]) != "AKIAFIRST" {
		t.Errorf("data = %v", sec.Data)
	}
	if sec.Labels[account.AccountLabel] != "true" || sec.Annotations[account.FormatAnnotation] != format.Plaintext || sec.Annotations[account.BucketAccessAnnotation] != cosiAccess {
		t.Errorf("labels = %v, annotations = %v", sec.Labels, sec.Annotations)
	}
	if ref := metav1.GetControllerOf(sec); ref == nil || ref.Name != cosiName {
		t.Errorf("controller = %v, want the template", ref)
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginNotIssued || len(rec.History) != 1 || rec.History[0].Operation != corev1alpha1.CredentialOperationCreate {
		t.Errorf("record = %+v, want one Create", rec)
	}
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: cosiName}, tmpl); err != nil {
		t.Fatal(err)
	}
	if tmpl.Status.SecretResourceVersion != sec.ResourceVersion {
		t.Errorf("status mirrors %q, Secret is at %q", tmpl.Status.SecretResourceVersion, sec.ResourceVersion)
	}
}

func TestCOSIMintReCreatesTheBucketAccessAndStoresTheNewKeysForItsRequest(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	firstUID := f.bucketAccess(t).UID

	f.request(t, "req-1", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want the one of the old keys deleted", ba)
	}
	f.finalize(t)
	f.reconcile(t)
	ba := f.bucketAccess(t)
	if ba == nil || ba.Annotations[account.RequestAnnotation] != "req-1" {
		t.Fatalf("BucketAccess = %+v, want one created for req-1", ba)
	}
	if sec := f.account(t); sec.Annotations[account.GrantedAnnotation] == "req-1" {
		t.Error("the Secret is marked issued before the new grant")
	}
	f.grant(t, "AKIASECOND", "second-secret")
	if f.bucketAccess(t).UID == firstUID {
		t.Fatal("the test grant reused the old UID")
	}
	f.reconcile(t)

	sec := f.account(t)
	if string(sec.Data[account.PasswordKey]) != "second-secret" || string(sec.Data[account.AccessKeyIDKey]) != "AKIASECOND" {
		t.Errorf("data = %v, want the keys of the new grant", sec.Data)
	}
	if sec.Annotations[account.GrantedAnnotation] != "req-1" {
		t.Errorf("granted = %q, want req-1", sec.Annotations[account.GrantedAnnotation])
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginMinted || rec.By != "jane@example.org" {
		t.Errorf("record = %+v, want the mint's", rec)
	}
	// A later pass leaves a served request alone.
	f.reconcile(t)
	if f.bucketAccess(t).Annotations[account.RequestAnnotation] != "req-1" {
		t.Error("a served request was re-granted")
	}
}

// A revoke locks the account out: the BucketAccess goes, none replaces it,
// and the Secret is served without keys until the next mint.
func TestCOSIRevokeDeletesTheBucketAccessAndIssuesNoKeys(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	f.request(t, "req-r", corev1alpha1.CredentialOperationRevoke)
	f.reconcile(t)
	if sec := f.account(t); sec.Annotations[account.GrantedAnnotation] == "req-r" {
		t.Error("the revoke is marked served while its BucketAccess lives")
	}
	f.finalize(t)
	f.reconcile(t)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba != nil {
		t.Fatalf("a revoke created BucketAccess %+v", ba)
	}
	sec := f.account(t)
	if _, ok := sec.Data[account.PasswordKey]; ok || len(sec.Data[account.AccessKeyIDKey]) > 0 || string(sec.Data[account.UsernameKey]) != "web" {
		t.Errorf("data = %v, want the user name only", sec.Data)
	}
	if sec.Annotations[account.GrantedAnnotation] != "req-r" {
		t.Errorf("granted = %q, want req-r", sec.Annotations[account.GrantedAnnotation])
	}
	tmpl := f.template(t)
	if tmpl.Status.Password.Origin != string(corev1alpha1.CredentialOriginRevoked) || tmpl.Status.SecretResourceVersion != sec.ResourceVersion {
		t.Errorf("status = %+v, want Revoked at %s", tmpl.Status, sec.ResourceVersion)
	}

	// The next mint issues keys again.
	f.request(t, "req-2", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || ba.Annotations[account.RequestAnnotation] != "req-2" {
		t.Fatalf("BucketAccess = %+v, want one created for req-2", ba)
	}
	f.grant(t, "AKIATHIRD", "third-secret")
	f.reconcile(t)
	if sec := f.account(t); string(sec.Data[account.PasswordKey]) != "third-secret" || sec.Annotations[account.GrantedAnnotation] != "req-2" {
		t.Errorf("data = %v, annotations = %v", sec.Data, sec.Annotations)
	}
}

// The sidecar has the driver grant an identity before it puts its finalizer
// on the BucketAccess; a delete in between skips the driver revoke and leaves
// that identity behind, so the reconciler waits for the finalizer.
func TestCOSIWaitsForTheSidecarToFinalizeBeforeDeleting(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grantUnfinalized(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	// A mint on an account that has no Secret yet creates it.
	f.createRequest(t, "req-1", corev1alpha1.CredentialOperationMint)
	if res := f.reconcile(t); res.RequeueAfter != cosiGrantRequeue {
		t.Errorf("requeue = %v, want %v", res.RequeueAfter, cosiGrantRequeue)
	}
	if ba := f.bucketAccess(t); ba == nil || !ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want it kept until the sidecar finalizes it", ba)
	}
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want it deleted once finalized", ba)
	}
}

// The template's status follows the account Secret while a request waits,
// so a request that never completes leaves the next one a version to write
// over rather than a conflict on every attempt.
func TestCOSIStatusMirrorsAPendingRequest(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	f.request(t, "req-1", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	sec := f.account(t)
	tmpl := f.template(t)
	if tmpl.Status.SecretResourceVersion != sec.ResourceVersion || tmpl.Status.Password.Origin != string(corev1alpha1.CredentialOriginMinted) {
		t.Fatalf("status = %+v, want the pending mint at %s", tmpl.Status, sec.ResourceVersion)
	}
	// A retry supersedes it, and the reconciler serves the retry.
	f.request(t, "req-2", corev1alpha1.CredentialOperationMint)
	f.finalize(t)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || ba.Annotations[account.RequestAnnotation] != "req-2" {
		t.Fatalf("BucketAccess = %+v, want one created for req-2", ba)
	}
}

// changeClass points the template at another class, as the chart renders it
// when a user's readonly flag or the bucket's storage pool changes.
func (f *cosiFixture) changeClass(t *testing.T, class string) {
	t.Helper()
	tmpl := f.template(t)
	tmpl.Spec.BucketAccess.BucketAccessClassName = class
	if err := f.c.Update(context.Background(), tmpl); err != nil {
		t.Fatal(err)
	}
}

// A class change re-grants the account, and nobody is shown the keys of that
// grant, so it is recorded as a revoke by the controller: the old grant goes,
// none replaces it, and the next mint is granted under the new class.
func TestCOSIClassChangeIsRecordedAsARevokeByTheController(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	f.request(t, "req-1", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	f.finalize(t)
	f.reconcile(t)
	f.grant(t, "AKIASECOND", "second-secret")
	f.reconcile(t)
	if rec := account.ReadRecord(f.account(t)); rec.Origin != corev1alpha1.CredentialOriginMinted {
		t.Fatalf("record = %+v, want the mint before the change", rec)
	}

	f.changeClass(t, "seaweedfs-readonly")
	f.reconcile(t)
	sec := f.account(t)
	if _, ok := sec.Data[account.PasswordKey]; ok || len(sec.Data[account.AccessKeyIDKey]) > 0 {
		t.Errorf("data = %v, want the keys of the old grant gone", sec.Data)
	}
	id := sec.Annotations[account.RequestAnnotation]
	if id == "" || id == "req-1" {
		t.Errorf("request = %q, want a new one of the controller's", id)
	}
	rec := account.ReadRecord(sec)
	if rec.Origin != corev1alpha1.CredentialOriginRevoked || rec.By != CredentialControllerActor ||
		len(rec.History) < 2 || rec.History[0].Operation != corev1alpha1.CredentialOperationRevoke || rec.History[0].By != CredentialControllerActor ||
		rec.History[1].Operation != corev1alpha1.CredentialOperationMint {
		t.Errorf("record = %+v, want a revoke by the controller on top of the mint", rec)
	}
	if tmpl := f.template(t); tmpl.Status.Password.Origin != string(corev1alpha1.CredentialOriginRevoked) || tmpl.Status.Password.By != CredentialControllerActor {
		t.Errorf("status = %+v, want Revoked by the controller", tmpl.Status.Password)
	}
	select {
	case ev := <-f.recorder.Events:
		if ev == "" {
			t.Error("empty event")
		}
	default:
		t.Error("no event for the revoke")
	}

	// The revoke path takes it from here: the old grant goes, none replaces it.
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want the old grant deleted", ba)
	}
	f.finalize(t)
	f.reconcile(t)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba != nil {
		t.Fatalf("a class change created BucketAccess %+v before any mint", ba)
	}
	if sec := f.account(t); sec.Annotations[account.GrantedAnnotation] != id {
		t.Errorf("granted = %q, want %q", sec.Annotations[account.GrantedAnnotation], id)
	}
	if rec := account.ReadRecord(f.account(t)); len(rec.History) != 3 {
		t.Errorf("history = %+v, want one revoke recorded once", rec.History)
	}

	// The next mint is granted under the new class.
	f.request(t, "req-2", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	ba := f.bucketAccess(t)
	if ba == nil || ba.Annotations[account.RequestAnnotation] != "req-2" || ba.Spec.BucketAccessClassName != "seaweedfs-readonly" {
		t.Fatalf("BucketAccess = %+v, want one for req-2 under the new class", ba)
	}
}

// A mint the Secret already carries is served under the new class and keeps
// its record: its caller is the one shown the keys.
func TestCOSIClassChangeDuringAMintServesTheMint(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	f.request(t, "req-1", corev1alpha1.CredentialOperationMint)
	f.changeClass(t, "seaweedfs-readonly")
	f.reconcile(t)
	if sec := f.account(t); sec.Annotations[account.RequestAnnotation] != "req-1" {
		t.Fatalf("request = %q, want the mint's req-1 kept", sec.Annotations[account.RequestAnnotation])
	}
	f.finalize(t)
	f.reconcile(t)
	ba := f.bucketAccess(t)
	if ba == nil || ba.Annotations[account.RequestAnnotation] != "req-1" || ba.Spec.BucketAccessClassName != "seaweedfs-readonly" {
		t.Fatalf("BucketAccess = %+v, want one for req-1 under the new class", ba)
	}
	f.grant(t, "AKIASECOND", "second-secret")
	f.reconcile(t)
	sec := f.account(t)
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginMinted || rec.By != "jane@example.org" {
		t.Errorf("record = %+v, want the mint's", rec)
	}
	if string(sec.Data[account.PasswordKey]) != "second-secret" || sec.Annotations[account.GrantedAnnotation] != "req-1" {
		t.Errorf("data = %v, annotations = %v", sec.Data, sec.Annotations)
	}
}

// A revoked account has no grant to change: a class change adds no entry.
func TestCOSIClassChangeOnARevokedAccountAddsNoEntry(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	f.request(t, "req-r", corev1alpha1.CredentialOperationRevoke)
	f.reconcile(t)
	f.finalize(t)
	f.reconcile(t)
	before := account.ReadRecord(f.account(t))

	f.changeClass(t, "seaweedfs-readonly")
	f.reconcile(t)
	f.reconcile(t)
	sec := f.account(t)
	if rec := account.ReadRecord(sec); len(rec.History) != len(before.History) || rec.By != "jane@example.org" {
		t.Errorf("record = %+v, want it unchanged from %+v", rec, before)
	}
	if sec.Annotations[account.RequestAnnotation] != "req-r" {
		t.Errorf("request = %q, want req-r", sec.Annotations[account.RequestAnnotation])
	}
	if ba := f.bucketAccess(t); ba != nil {
		t.Errorf("BucketAccess = %+v, want none", ba)
	}
}

// A lost account Secret is written again from the live grant instead of
// rotating the keys its clients use.
func TestCOSIRewritesALostAccountSecretFromTheLiveGrant(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)
	f.request(t, "req-1", corev1alpha1.CredentialOperationMint)
	f.reconcile(t)
	f.finalize(t)
	f.reconcile(t)
	f.grant(t, "AKIASECOND", "second-secret")
	f.reconcile(t)
	uid := f.bucketAccess(t).UID

	if err := f.c.Delete(context.Background(), f.account(t)); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	f.reconcile(t)
	ba := f.bucketAccess(t)
	if ba == nil || ba.UID != uid || !ba.DeletionTimestamp.IsZero() {
		t.Fatalf("BucketAccess = %+v, want the live grant kept", ba)
	}
	sec := f.account(t)
	if sec == nil || string(sec.Data[account.PasswordKey]) != "second-secret" || sec.Annotations[account.RequestAnnotation] != "req-1" || sec.Annotations[account.GrantedAnnotation] != "req-1" {
		t.Fatalf("account Secret = %+v, want the keys of the live grant for req-1", sec)
	}
}

func TestCOSIAdoptsAMarkedBucketAccessKeepingItsKeys(t *testing.T) {
	legacy := &cosiv1alpha1.BucketAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name: cosiAccess, Namespace: ctNamespace, UID: "legacy-uid",
			Labels:      map[string]string{account.AdoptLabel: "true"},
			Annotations: map[string]string{"meta.helm.sh/release-name": cosiRelease, "helm.sh/resource-policy": "keep"},
		},
		Spec: cosiv1alpha1.BucketAccessSpec{BucketClaimName: cosiRelease, BucketAccessClassName: cosiClass, CredentialsSecretName: cosiAccess, Protocol: "s3"},
	}
	f := newCOSIFixture(t, cosiTemplate(), legacy)
	f.grant(t, "AKIALEGACY", "legacy-secret")
	f.reconcile(t)

	ba := f.bucketAccess(t)
	if ba == nil || ba.UID != "legacy-uid" {
		t.Fatalf("BucketAccess = %+v, want the legacy one kept", ba)
	}
	if ref := metav1.GetControllerOf(ba); ref == nil || ref.Name != cosiName {
		t.Errorf("controller = %v, want the template", ref)
	}
	sec := f.account(t)
	if sec == nil || string(sec.Data[account.PasswordKey]) != "legacy-secret" || string(sec.Data[account.AccessKeyIDKey]) != "AKIALEGACY" {
		t.Fatalf("account Secret = %v, want the legacy keys", sec)
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginMigrated {
		t.Errorf("record = %+v, want Migrated", rec)
	}
}

func TestCOSILeavesAnUnmarkedBucketAccessAlone(t *testing.T) {
	foreign := &cosiv1alpha1.BucketAccess{
		ObjectMeta: metav1.ObjectMeta{Name: cosiAccess, Namespace: ctNamespace, UID: "foreign-uid"},
		Spec:       cosiv1alpha1.BucketAccessSpec{BucketClaimName: cosiRelease, BucketAccessClassName: cosiClass, CredentialsSecretName: cosiAccess},
	}
	f := newCOSIFixture(t, cosiTemplate(), foreign)
	f.reconcile(t)
	if ba := f.bucketAccess(t); ba == nil || metav1.GetControllerOf(ba) != nil {
		t.Errorf("BucketAccess = %+v, want it untouched", ba)
	}
	if f.account(t) != nil {
		t.Error("keys of a foreign BucketAccess were copied")
	}
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: cosiName}, tmpl); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(tmpl.Status.Conditions, cozyv1alpha1.CredentialTemplateBucketAccessConflict) {
		t.Errorf("conditions = %v, want BucketAccessConflict", tmpl.Status.Conditions)
	}
}

func TestCOSIRemovingTheUserRemovesItsBucketAccessAndSecret(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	if err := f.c.Delete(context.Background(), cosiTemplate()); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	f.finalize(t)
	if sec := f.account(t); sec != nil {
		t.Error("the account Secret outlived its template")
	}
}

func TestPasswordReconcilerSkipsACOSITemplate(t *testing.T) {
	tmpl := cosiTemplate()
	tmpl.Name = ctName
	tmpl.Annotations[account.ReleaseNameAnnotation] = ctRelease
	f := newCTFixture(t, true, tmpl)
	f.reconcile(t)
	sec := &corev1.Secret{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: ctName + account.SecretSuffix}, sec); !apierrors.IsNotFound(err) {
		t.Errorf("the password reconciler seeded an engine-issued account: %v", err)
	}
	got := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: ctName}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) != 0 {
		t.Errorf("conditions = %v, want none", got.Status.Conditions)
	}
}
