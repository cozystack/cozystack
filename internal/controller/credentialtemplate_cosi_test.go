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
			Format:   cozyv1alpha1.CredentialTemplateFormat{Name: format.COSI},
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
// Secret holding the BucketInfo of the grant and reports access granted. The
// fake client assigns no UID, so grant gives the BucketAccess one.
func (f *cosiFixture) grant(t *testing.T, accessKey, secretKey string) {
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
	ba.Status.AccessGranted = true
	ba.Status.AccountID = "ba-" + string(ba.UID)
	if err := f.c.Status().Update(ctx, ba); err != nil {
		t.Fatal(err)
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

// request writes the account Secret the way a mint or a revoke does: rebuilt
// without keys, carrying a request id and the operation in its record.
func (f *cosiFixture) request(t *testing.T, id string, op corev1alpha1.CredentialOperation) {
	t.Helper()
	sec := f.account(t)
	rec := account.ReadRecord(sec).Apply(op, metav1.NewTime(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)), "jane@example.org")
	updated := sec.DeepCopy()
	updated.Data = map[string][]byte{account.UsernameKey: []byte("web")}
	updated.Annotations = map[string]string{account.FormatAnnotation: format.COSI, account.RequestAnnotation: id}
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
	if sec.Labels[account.AccountLabel] != "true" || sec.Annotations[account.FormatAnnotation] != format.COSI {
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
	if ba := f.bucketAccess(t); ba != nil {
		t.Fatalf("the BucketAccess of the old keys survived the mint: %v", ba.UID)
	}
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

func TestCOSIRevokeReCreatesTheBucketAccessToo(t *testing.T) {
	f := newCOSIFixture(t, cosiTemplate())
	f.reconcile(t)
	f.grant(t, "AKIAFIRST", "first-secret")
	f.reconcile(t)

	f.request(t, "req-r", corev1alpha1.CredentialOperationRevoke)
	f.reconcile(t)
	f.reconcile(t)
	f.grant(t, "AKIATHIRD", "third-secret")
	f.reconcile(t)

	sec := f.account(t)
	if string(sec.Data[account.PasswordKey]) != "third-secret" || sec.Annotations[account.GrantedAnnotation] != "req-r" {
		t.Errorf("data = %v, annotations = %v", sec.Data, sec.Annotations)
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginRevoked {
		t.Errorf("record = %+v, want Revoked", rec)
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
	if ba := f.bucketAccess(t); ba != nil {
		t.Error("the BucketAccess outlived its template")
	}
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
		t.Errorf("the password reconciler seeded a cosi account: %v", err)
	}
	got := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: ctName}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) != 0 {
		t.Errorf("conditions = %v, want none", got.Status.Conditions)
	}
}
