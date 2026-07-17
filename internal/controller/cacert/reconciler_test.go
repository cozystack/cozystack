/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cacert

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	testNamespace = "tenant-foo"
	// testRelease is the Helm release name — the HelmRelease the cozystack
	// API creates for application Postgres/mydb (prefix "postgres-"). Both
	// the projection and the engine's own CA Secret are named after it.
	testRelease    = "postgres-mydb"
	testProjection = testRelease + projectionSuffix
	testAppKind    = "Postgres"
	testAppName    = "mydb"
	testHRUID      = types.UID("11111111-2222-3333-4444-555555555555")

	// testOperatorCA is the CNPG shape: the operator creates <release>-ca and
	// puts the CA private key in it, next to the certificate. It carries no
	// publish label — CNPG cannot be made to write one.
	testOperatorCA = testRelease + "-ca"
	// testLabelledCA is the cert-manager shape: the chart labels the Secret
	// through Certificate.spec.secretTemplate.
	testLabelledCA = testRelease + "-tls-ca"

	// testKey is the private-key fixture. Unlike the certificates it stays a
	// bare header with a stub body, and deliberately so: the key guard is a
	// match on the PEM header line and runs BEFORE any parse, so a real key
	// would prove nothing this does not — and a test suite that mints real
	// private keys to check that they are refused invites someone to copy the
	// pattern somewhere it matters.
	testKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n"

	caKeyKey = "ca.key"
)

// testCA and testCARot are REAL self-signed certificates, minted once per run.
//
// They used to be certificate armour wrapped around a truncated DER prefix,
// which the write-path guard accepted because it only matched the BEGIN line.
// Now that the guard parses what it publishes (see certificateChainPEM), a
// fixture has to be the thing it claims to be — and the fact that these two
// constants had to change at all is the clearest statement of what the old
// guard was worth.
//
// testCARot is a second, distinct certificate: rotation tests need the
// projection to track a value CHANGE, so it must not be equal to testCA.
//
// testCAForged is the attacker's CA, and it is REAL for a reason worth stating.
// It used to be armour around the word FORGED repeated — which no parser accepts
// — and that quietly inverted the tests that use it. An attacker forging a trust
// anchor mints a VALID CA: a certificate the tenant's verifier loads happily and
// that the attacker holds the key to. That is the entire point of the attack. A
// garbage fixture models an adversary who submits something unusable, lets the
// write-path guard reject it, and leaves the precedence rule those tests exist to
// prove completely unexercised.
var (
	testCA       = mustCertPEM("cozystack-test-ca")
	testCARot    = mustCertPEM("cozystack-test-ca-rotated")
	testCAForged = mustCertPEM("cozystack-test-ca-attacker")
)

// mustCertPEM mints a self-signed CA certificate and returns it PEM-encoded.
// It panics rather than returning an error so the fixtures can stay package
// vars; a failure here is a broken toolchain, not a test outcome.
func mustCertPEM(commonName string) string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("generate test CA key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("mint test CA certificate: " + err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// mustPrivateKeyDER returns a RAW DER-encoded PKCS#8 private key, with no PEM
// armour at all. It is the fixture for the smuggling cases: key material that
// wears no "-----BEGIN ... PRIVATE KEY-----" header slips past the header guard,
// and pem.Decode skips it as inter-block noise, so only a projection rebuilt
// from the parsed certificates can keep it away from the tenant.
func mustPrivateKeyDER() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("generate test private key: " + err.Error())
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic("marshal test private key: " + err.Error())
	}
	return der
}

// mustPublicKeyDER returns a DER-encoded public key, for the fixture that
// checks a well-formed PEM block of the WRONG type is still refused.
func mustPublicKeyDER() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("generate test key: " + err.Error())
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic("marshal test public key: " + err.Error())
	}
	return der
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := helmv2.AddToScheme(s); err != nil {
		t.Fatalf("helm scheme: %v", err)
	}
	if err := cozyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("cozystack scheme: %v", err)
	}
	return s
}

// helmRelease builds the HelmRelease backing an application instance. The
// cozystack API stamps the three application.* labels on every HelmRelease
// it creates (pkg/registry/apps/application/rest.go); they are what makes a
// HelmRelease recognisable as an application release.
func helmRelease() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testRelease,
			Namespace: testNamespace,
			UID:       testHRUID,
			Labels: map[string]string{
				appsv1alpha1.ApplicationGroupLabel: appsGroup,
				appsv1alpha1.ApplicationKindLabel:  testAppKind,
				appsv1alpha1.ApplicationNameLabel:  testAppName,
			},
		},
	}
}

// appDef builds the ApplicationDefinition for the Postgres kind. With
// caCert set it declares the operator-created CA Secret, which is the only
// path available for engines that cannot label their own output.
func appDef(caCert *cozyv1alpha1.ApplicationDefinitionCACert) *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{
				Kind: testAppKind, Plural: "postgreses", Singular: "postgres",
			},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{Prefix: "postgres-"},
			CACert:  caCert,
		},
	}
}

// declaredCA is the ApplicationDefinition entry for the CNPG shape.
func declaredCA() *cozyv1alpha1.ApplicationDefinitionCACert {
	return &cozyv1alpha1.ApplicationDefinitionCACert{
		SourceSecretName: "{{ .release }}-ca",
		SourceKey:        caCertKey,
	}
}

// operatorSecret builds a Secret an operator created: no publish label, but
// carrying the lineage labels the admission webhook stamps on every object
// tracing back to a cozystack application.
func operatorSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				appsv1alpha1.ApplicationGroupLabel: appsGroup,
				appsv1alpha1.ApplicationKindLabel:  testAppKind,
				appsv1alpha1.ApplicationNameLabel:  testAppName,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

// labelledSecret builds a Secret that opts into publication through the
// label — the default contract, used by the cert-manager-minting charts.
func labelledSecret(name string, data map[string][]byte, annotations map[string]string) *corev1.Secret {
	s := operatorSecret(name, data)
	s.Labels[SourceLabel] = trueValue
	s.Annotations = annotations
	return s
}

func newReconciler(c client.Client, rec record.EventRecorder) *Reconciler {
	// The fake client has no cache, so the cached Client and the uncached
	// Reader coincide — production splits them only to keep the scoped
	// Secret cache small.
	return &Reconciler{Client: c, Reader: c, Cache: c, Recorder: rec}
}

// reconcileRelease runs one reconcile of the application release.
func reconcileRelease(t *testing.T, c client.Client) (*record.FakeRecorder, ctrl.Result, error) {
	t.Helper()
	rec := record.NewFakeRecorder(16)
	res, err := newReconciler(c, rec).Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testRelease},
	})
	return rec, res, err
}

func mustReconcile(t *testing.T, c client.Client) *record.FakeRecorder {
	t.Helper()
	rec, _, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return rec
}

func getSecret(t *testing.T, c client.Client, name string) (*corev1.Secret, bool) {
	t.Helper()
	got := &corev1.Secret{}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: testNamespace, Name: name}, got)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get secret %s: %v", name, err)
	}
	return got, true
}

// warnings drains the fake recorder and returns the Warning events it saw.
func warnings(t *testing.T, rec *record.FakeRecorder) []string {
	t.Helper()
	var out []string
	for {
		select {
		case e := <-rec.Events:
			if strings.HasPrefix(e, corev1.EventTypeWarning) {
				out = append(out, e)
			}
		default:
			return out
		}
	}
}

func mustProjection(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	got, ok := getSecret(t, c, testProjection)
	if !ok {
		t.Fatalf("expected projection %s", testProjection)
	}
	return got
}

// assertKeyFree pins the whole point of the controller: whatever the source
// held, the projection holds exactly one key, ca.crt, and no key material.
func assertKeyFree(t *testing.T, got *corev1.Secret, want string) {
	t.Helper()
	if got.Type != corev1.SecretTypeOpaque {
		t.Errorf("projection type = %q, want Opaque", got.Type)
	}
	if len(got.Data) != 1 {
		t.Fatalf("projection must hold exactly one key, got %v", keysOf(got.Data))
	}
	if string(got.Data[caCertKey]) != want {
		t.Errorf("projection ca.crt = %q, want %q", got.Data[caCertKey], want)
	}
	for k, v := range got.Data {
		if containsPrivateKey(string(v)) {
			t.Fatalf("projection leaked private key material under %q", k)
		}
	}
}

// TestReconcile_DeclaredSource_ExtractsCACertOnly is the CNPG case, and the
// reason the name-driven leg exists at all: the operator creates
// <release>-ca itself, refuses to label it, and puts the CA PRIVATE KEY in
// it next to the certificate. The projection must lift ca.crt and nothing
// else.
func TestReconcile_DeclaredSource_ExtractsCACertOnly(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{
			caCertKey: []byte(testCA),
			caKeyKey:  []byte(testKey),
		}),
	).Build()

	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)

	// The source itself is left exactly as the operator wrote it.
	src, ok := getSecret(t, c, testOperatorCA)
	if !ok {
		t.Fatalf("the operator source must be kept")
	}
	if len(src.Data) != 2 || string(src.Data[caKeyKey]) != testKey {
		t.Errorf("the operator source must not be modified, got %v", keysOf(src.Data))
	}
	if src.Labels[ManagedLabel] == trueValue {
		t.Errorf("the operator source must never be adopted as a projection")
	}
}

// TestReconcile_NameDrivenSource_ReadThroughUncachedReader locks the contract
// this controller's cache split depends on — and which the scoped manager cache
// (post wildcard-secret merge) makes load-bearing: the name-driven source, an
// unlabelled operator-created <release>-ca, must be read through the UNCACHED
// Reader, never the cached Client. The cached Client is backed by a Secret cache
// scoped to objects this controller does not own, so an unlabelled CNPG/PSMDB
// source is invisible to it.
//
// The other cacert tests wire Client, Reader and Cache to one fake, so that
// split is otherwise only a code-review guarantee. Here the source lives ONLY in
// the Reader's store and never the Client's; the projection must still appear. A
// refactor that read the declared source through the cached Client (r.Get instead
// of r.Reader.Get) would make it vanish here.
func TestReconcile_NameDrivenSource_ReadThroughUncachedReader(t *testing.T) {
	scheme := newScheme(t)
	// The cached Client sees the release and its definition, and receives the
	// projection write — but NOT the operator-created source Secret.
	clientFake := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		helmRelease(), appDef(declaredCA()),
	).Build()
	// The uncached Reader is the only place the operator source exists, exactly
	// as in production, where an unlabelled CNPG CA is absent from the scoped
	// informer and reachable only through a live API read.
	readerFake := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	rec := &Reconciler{Client: clientFake, Reader: readerFake, Cache: clientFake, Recorder: record.NewFakeRecorder(8)}

	if _, err := rec.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testRelease},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The projection is written through the Client, so it lands in that store.
	got, ok := getSecret(t, clientFake, testProjection)
	if !ok {
		t.Fatalf("the name-driven source must be read through the uncached Reader; the projection is missing, which means the declared source was looked up in the (scoped) cached Client")
	}
	assertKeyFree(t, got, testCA)
}

// TestReconcile_UpsertExistingCheck_ReadThroughUncachedReader pins the SECOND
// uncached read the Reader-field doc requires: upsertProjection reads the
// EXISTING projection through the uncached Reader, not the cached Client. Now
// that the shared Secret cache is scoped (post wildcard-secret merge) it does not
// hold this controller's projections, so a cached read of the existing projection
// would return NotFound forever — turning every steady-state reconcile into a
// Create that fails AlreadyExists.
//
// The Client here mimics that scoped cache: it answers every Secret Get with
// NotFound while passing writes and non-Secret reads through to the real store,
// where an up-to-date managed projection already exists. The reconcile must be a
// clean no-op. A refactor reading the existing projection via r.Get would take the
// Create branch and error AlreadyExists — which this test would catch and the
// single-fake tests would not.
func TestReconcile_UpsertExistingCheck_ReadThroughUncachedReader(t *testing.T) {
	scheme := newScheme(t)
	def := appDef(declaredCA())
	digest, err := selectorsDigest(def)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	owner := releaseOwnerRef(helmRelease())
	// A projection already published and fully up to date — steady state.
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			Annotations: map[string]string{
				SourceRefAnnotation:       testNamespace + "/" + testOperatorCA,
				SelectorsDigestAnnotation: digest,
			},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCA)},
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		helmRelease(), def,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		existing,
	).Build()
	// The Client mimics the scoped shared cache: no Secret is ever visible through
	// it, but writes and non-Secret reads pass through to the real store.
	scopedClient := interceptor.NewClient(store, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "secrets"}, key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	rec := &Reconciler{Client: scopedClient, Reader: store, Cache: store, Recorder: record.NewFakeRecorder(8)}

	if _, err := rec.Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testRelease},
	}); err != nil {
		t.Fatalf("the existing projection must be read through the uncached Reader; got an error a cached read would produce (NotFound then AlreadyExists on create): %v", err)
	}
}

// TestReconcile_DeclaredSource_CarriesOwnerAndMarkers pins the projection's
// metadata on the name-driven leg: identical to the label-driven leg, since
// both legs share one write path.
func TestReconcile_DeclaredSource_CarriesOwnerAndMarkers(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()

	mustReconcile(t, c)

	got := mustProjection(t, c)
	if got.Labels[TenantCALabel] != trueValue || got.Labels[ManagedLabel] != trueValue {
		t.Errorf("projection must carry the tenant-CA and managed markers, labels=%v", got.Labels)
	}
	if got.Annotations[SourceRefAnnotation] != testNamespace+"/"+testOperatorCA {
		t.Errorf("projection must back-reference its source, got %q", got.Annotations[SourceRefAnnotation])
	}
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("projection must carry one owner reference, got %v", got.OwnerReferences)
	}
	ref := got.OwnerReferences[0]
	if ref.Kind != helmv2.HelmReleaseKind || ref.Name != testRelease || ref.UID != testHRUID ||
		ref.APIVersion != helmv2.GroupVersion.String() {
		t.Errorf("owner reference must point at the application release, got %+v", ref)
	}
	// BlockOwnerDeletion must stay false: a trust anchor must never hold up the
	// teardown of the application it belongs to.
	if ptr.Deref(ref.BlockOwnerDeletion, true) {
		t.Errorf("owner reference must not block owner deletion, got %+v", ref)
	}
}

// TestReconcile_DeclaredSource_HonoursSourceKey pins that the declaration
// names the key to lift, and that it is always republished under ca.crt.
func TestReconcile_DeclaredSource_HonoursSourceKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}-ca",
			SourceKey:        corev1.TLSCertKey,
		}),
		operatorSecret(testOperatorCA, map[string][]byte{
			corev1.TLSCertKey:       []byte(testCA),
			corev1.TLSPrivateKeyKey: []byte(testKey),
		}),
	).Build()

	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_DeclaredSource_DefaultsToCACertKey pins the default key when
// the declaration omits it.
func TestReconcile_DeclaredSource_DefaultsToCACertKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{SourceSecretName: "{{ .release }}-ca"}),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()

	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_DeclaredSource_WaitsForOperator pins the async tolerance the
// name-driven leg needs MORE of than the label leg: CNPG and PSMDB create
// their CA Secret long after the chart renders, so "not there yet" is the
// normal state at startup — a quiet, retried wait, never an error and never
// a Warning.
func TestReconcile_DeclaredSource_WaitsForOperator(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(declaredCA()),
	).Build()

	rec, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("an operator source that does not exist yet must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a missing operator source must be retried, got %+v", res)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("nothing must be projected before the operator writes its CA")
	}
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("waiting for an operator source must not warn, got %v", w)
	}
}

// TestReconcile_DeclaredSource_RotationPropagates pins that a CA rotation on
// the operator-owned source reaches the projection. The source is not
// watched (it carries no label the informer can select on), so the resync is
// what drives this — hence a resync must be requested on the happy path.
func TestReconcile_DeclaredSource_RotationPropagates(t *testing.T) {
	src := operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), src,
	).Build()

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("an unwatched source must be resynced, got %+v", res)
	}

	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(src), live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data[caCertKey] = []byte(testCARot)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("rotate source: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCARot)
}

// TestReconcile_DeclaredSource_RefusesKeyMaterialUnderTheLiftedKey pins the
// guard where it matters most. On the name-driven leg the source is
// key-bearing BY CONSTRUCTION, so a declaration that names the wrong key
// (say tls.key) must be refused at write time, not published.
func TestReconcile_DeclaredSource_RefusesKeyMaterialUnderTheLiftedKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}-ca",
			SourceKey:        caKeyKey,
		}),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()

	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("private key material must never be published")
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonPrivateKeyRefused) {
		t.Errorf("expected a %s Warning event, got %v", reasonPrivateKeyRefused, w)
	}
}

// TestReconcile_PerconaShape_ProjectsAwayFromTheOperatorsSecret pins the
// mongodb case that drove the canonical name away from "<release>-ca-cert":
// PSMDB creates a KEY-BEARING Secret of exactly that name. Under the canonical
// name "<release>.tenant-ca" it is just an ordinary declared source — the
// certificate is lifted out of it, the projection lands on a free name, and
// the operator's Secret is left byte-for-byte alone.
func TestReconcile_PerconaShape_ProjectsAwayFromTheOperatorsSecret(t *testing.T) {
	perconaCA := testRelease + "-ca-cert"
	psmdb := operatorSecret(perconaCA, map[string][]byte{
		caCertKey:               []byte(testCA),
		corev1.TLSCertKey:       []byte(testCA),
		corev1.TLSPrivateKeyKey: []byte(testKey),
	})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}-ca-cert",
			SourceKey:        caCertKey,
		}),
		psmdb,
	).Build()

	rec := mustReconcile(t, c)

	if perconaCA == testProjection {
		t.Fatalf("the canonical projection name must not collide with the engine's own CA Secret (%s)", perconaCA)
	}
	assertKeyFree(t, mustProjection(t, c), testCA)

	src, ok := getSecret(t, c, perconaCA)
	if !ok {
		t.Fatalf("the operator's Secret must be kept")
	}
	if len(src.Data) != 3 || string(src.Data[corev1.TLSPrivateKeyKey]) != testKey {
		t.Errorf("the operator's Secret must not be modified, got %v", keysOf(src.Data))
	}
	if src.Labels[ManagedLabel] == trueValue || src.Labels[TenantCALabel] == trueValue {
		t.Errorf("a key-bearing operator Secret must never be marked as a tenant trust anchor, labels=%v", src.Labels)
	}
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("the engine's own CA Secret is an ordinary source now; it must not warn, got %v", w)
	}
}

// TestReconcile_StrimziShape_CopiesKeyFreeCA pins kafka: Strimzi publishes a
// key-free CA under <release>-clients-ca-cert, a name of its own choosing. The
// projection is a straight copy onto the canonical name, so a tenant learns one
// name for every engine instead of one name per engine.
func TestReconcile_StrimziShape_CopiesKeyFreeCA(t *testing.T) {
	clientsCA := testRelease + "-clients-ca-cert"
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}-clients-ca-cert",
			SourceKey:        caCertKey,
		}),
		operatorSecret(clientsCA, map[string][]byte{caCertKey: []byte(testCA)}),
	).Build()

	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
	if _, ok := getSecret(t, c, clientsCA); !ok {
		t.Errorf("the operator's own CA Secret must be kept")
	}
}

// TestReconcile_SourceAtCanonicalName_KeyBearing pins the dangerous shape of a
// source that names the canonical projection itself — reachable now only
// through a misconfigured declaration or a stray publish label. A Secret cannot
// be projected over itself, and this one carries key material, so the canonical
// name is occupied by an object no tenant may read: refuse loudly, never
// overwrite, never adopt.
func TestReconcile_SourceAtCanonicalName_KeyBearing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}" + projectionSuffix,
			SourceKey:        caCertKey,
		}),
		operatorSecret(testProjection, map[string][]byte{
			caCertKey:               []byte(testCA),
			corev1.TLSPrivateKeyKey: []byte(testKey),
		}),
	).Build()

	rec := mustReconcile(t, c)

	got := mustProjection(t, c)
	if len(got.Data) != 2 || string(got.Data[corev1.TLSPrivateKeyKey]) != testKey {
		t.Errorf("the colliding Secret must be left untouched, got %v", keysOf(got.Data))
	}
	if got.Labels[ManagedLabel] == trueValue || got.Labels[TenantCALabel] == trueValue {
		t.Errorf("a key-bearing Secret must never be marked as a tenant trust anchor, labels=%v", got.Labels)
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCanonicalNameOccupied) {
		t.Errorf("expected a %s Warning event, got %v", reasonCanonicalNameOccupied, w)
	}
}

// TestReconcile_SourceAtCanonicalName_KeyFree pins the harmless half of the
// same case: a key-free Secret already sitting at the canonical name IS the
// trust anchor the tenant needs. There is nothing to extract, it is not the
// controller's to adopt, and it must not be warned about.
func TestReconcile_SourceAtCanonicalName_KeyFree(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}" + projectionSuffix,
			SourceKey:        caCertKey,
		}),
		operatorSecret(testProjection, map[string][]byte{caCertKey: []byte(testCA)}),
	).Build()

	rec := mustReconcile(t, c)

	got := mustProjection(t, c)
	if got.Labels[ManagedLabel] == trueValue {
		t.Errorf("a Secret the controller did not create must never be adopted")
	}
	if string(got.Data[caCertKey]) != testCA || len(got.Data) != 1 {
		t.Errorf("the existing Secret must be left untouched, got %v", keysOf(got.Data))
	}
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("a key-free CA already at the canonical name must not warn, got %v", w)
	}
}

// TestReconcile_SourceAtCanonicalName_KeyFree_Resyncs pins that the branch which
// decides to leave a Secret alone still comes back to look at it.
//
// It returned no RequeueAfter while every other outcome in the reconciler
// requeues on the resync, and that asymmetry had teeth: this branch is reachable
// only through a declaration, and a declared source is NOT watched — nothing
// observes its contents. So the loud refusal above was effectively one-shot. A
// private key added to the Secret after this pass would never be noticed, and
// the canonical name would go on being served to the tenant with key material in
// it, silently, for the lifetime of the release.
func TestReconcile_SourceAtCanonicalName_KeyFree_Resyncs(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
			SourceSecretName: "{{ .release }}" + projectionSuffix,
			SourceKey:        caCertKey,
		}),
		operatorSecret(testProjection, map[string][]byte{caCertKey: []byte(testCA)}),
	).Build()

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter != resyncInterval {
		t.Fatalf("a key-free source at the canonical name must be re-examined on the resync, got %+v", res)
	}

	// And the refusal actually fires when key material shows up later — which is
	// the whole point of coming back.
	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: testNamespace, Name: testProjection}, live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data[caKeyKey] = []byte(testKey)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("add key material: %v", err)
	}

	rec := mustReconcile(t, c)
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCanonicalNameOccupied) {
		t.Errorf("expected a %s Warning once key material appears, got %v", reasonCanonicalNameOccupied, w)
	}
}

// TestReconcile_SourceAtCanonicalName_ContractDeviation pins that the branch
// asserts the contract it is silently endorsing.
//
// Deciding "this Secret IS already the trust anchor, leave it alone" hands the
// canonical name to the tenant unexamined. But the canonical name promises one
// specific shape — an Opaque Secret carrying ca.crt and nothing else — and the
// shared write path GUARANTEES that shape for every projection it writes. Here
// the object is the engine's own, so the controller cannot enforce it; the least
// it can do is not pretend it checked. Key material is already refused above;
// these are the deviations that are not key material and were accepted in
// silence.
func TestReconcile_SourceAtCanonicalName_ContractDeviation(t *testing.T) {
	for name, secret := range map[string]*corev1.Secret{
		"extra keys beside the anchor": {
			ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{caCertKey: []byte(testCA), "notes.txt": []byte("hello")},
		},
		"no anchor at all": {
			ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"something.else": []byte(testCA)},
		},
		"an anchor that is not a certificate": {
			ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{caCertKey: []byte("-----BEGIN CERTIFICATE-----\nnot a cert\n-----END CERTIFICATE-----\n")},
		},
		"not an Opaque Secret": {
			ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
			Type:       corev1.SecretType("cozystack.io/something"),
			Data:       map[string][]byte{caCertKey: []byte(testCA)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
				helmRelease(),
				appDef(&cozyv1alpha1.ApplicationDefinitionCACert{
					SourceSecretName: "{{ .release }}" + projectionSuffix,
					SourceKey:        caCertKey,
				}),
				secret,
			).Build()

			rec := mustReconcile(t, c)

			if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCanonicalNameContract) {
				t.Errorf("expected a %s Warning event, got %v", reasonCanonicalNameContract, w)
			}
			// Warned about, never rewritten: it is still not the controller's object.
			got := mustProjection(t, c)
			if got.Labels[ManagedLabel] == trueValue {
				t.Errorf("a Secret the controller did not create must never be adopted")
			}
		})
	}
}

// TestReconcile_LabelledSource_Projects pins the default contract: the
// cert-manager-minting charts label the Secret, and the controller does not
// need any per-engine declaration for them.
func TestReconcile_LabelledSource_Projects(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{
			caCertKey:               []byte(testCA),
			corev1.TLSPrivateKeyKey: []byte(testKey),
		}, nil),
	).Build()

	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCA)
	if got.Annotations[SourceRefAnnotation] != testNamespace+"/"+testLabelledCA {
		t.Errorf("projection must back-reference its source, got %q", got.Annotations[SourceRefAnnotation])
	}
}

// certManagerSecret builds the Secret cert-manager ACTUALLY produces from a
// Certificate carrying spec.secretTemplate.labels: the publish label and the
// release label, and NOTHING else.
//
// Deliberately no lineage labels and no Helm labels — cert-manager sets no
// OwnerReference (the platform ships enableCertificateOwnerRef=false) and Helm
// did not create the Secret, so the lineage webhook can resolve no ancestor and
// stamps no application.* labels. A fixture that hand-stamps them would be
// testing an object that never exists in the cluster.
func certManagerSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				SourceLabel:        trueValue,
				SourceReleaseLabel: testRelease,
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: data,
	}
}

// TestReconcile_CertManagerShape_Projects pins the DEFAULT leg against the
// object it actually exists to serve. A cert-manager-issued Secret is
// attributable by the release label alone: it has no OwnerReference, no Helm
// metadata, and therefore no lineage labels either. If attribution depended on
// any of those, this leg would be dead for every cert-manager-minting engine —
// and worse than dead, since a release with no resolvable source now has its
// projection withdrawn.
func TestReconcile_CertManagerShape_Projects(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(nil),
		certManagerSecret(testLabelledCA, map[string][]byte{
			caCertKey:               []byte(testCA),
			corev1.TLSCertKey:       []byte(testCA),
			corev1.TLSPrivateKeyKey: []byte(testKey),
		}),
	).Build()

	rec := mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("a well-formed cert-manager source must not warn, got %v", w)
	}
}

// TestReleaseOfSecret_CertManagerShape pins the watch half of the same case: the
// release label is what maps a cert-manager source back to its release, so a
// rotation propagates immediately instead of never.
func TestReleaseOfSecret_CertManagerShape(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease()).Build()
	rec := record.NewFakeRecorder(16)

	got := newReconciler(c, rec).releaseOfSecret(context.TODO(),
		certManagerSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}))

	if len(got) != 1 || got[0].Name != testRelease || got[0].Namespace != testNamespace {
		t.Fatalf("a cert-manager source must map to its release, got %+v", got)
	}
}

// TestReleaseOfSecret_LineageLabelsResolveViaList pins the last attribution
// route: a source that carries only the application.* lineage labels — no
// release label, no Helm ownership label — is mapped to its release by listing
// the HelmReleases of that application. This is the operator-created-source path,
// where the admission webhook stamped lineage labels but no direct release name.
func TestReleaseOfSecret_LineageLabelsResolveViaList(t *testing.T) {
	// operatorSecret carries exactly the lineage labels and nothing that names the
	// release directly.
	src := operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA)})
	src.Labels[SourceLabel] = trueValue
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), src).Build()

	got := newReconciler(c, record.NewFakeRecorder(4)).releaseOfSecret(context.TODO(), src)

	if len(got) != 1 || got[0].Name != testRelease || got[0].Namespace != testNamespace {
		t.Fatalf("a lineage-labelled source must resolve to its release via the HelmRelease list, got %+v", got)
	}
}

// TestReleaseOfSecret_UnattributableSourceWarns pins that a source which opts in
// but names no release fails LOUDLY. This is the mistake a chart author makes
// when they add the publish label to a Certificate and forget the release
// label, and every other refusal in this controller is a Warning Event — a
// silent log line would leave them with a trust anchor that never appears and
// nothing to explain why.
func TestReleaseOfSecret_UnattributableSourceWarns(t *testing.T) {
	orphan := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testLabelledCA,
			Namespace: testNamespace,
			// Opted in, but attributable to nothing: no release label, no Helm
			// metadata, no lineage labels.
			Labels: map[string]string{SourceLabel: trueValue},
		},
		Data: map[string][]byte{caCertKey: []byte(testCA)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease()).Build()
	rec := record.NewFakeRecorder(16)

	got := newReconciler(c, rec).releaseOfSecret(context.TODO(), orphan)

	if len(got) != 0 {
		t.Errorf("an unattributable source must enqueue nothing, got %+v", got)
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonUnattributableSource) {
		t.Errorf("expected a %s Warning event, got %v", reasonUnattributableSource, w)
	}
}

// TestReconcile_MissingApplicationDefinition_KeepsProjection pins the second
// indeterminate state. ApplicationDefinitions are registered dynamically at
// runtime, so a kind whose definition is momentarily absent must not read as
// "this engine publishes nothing" — that would withdraw the trust anchor of
// every release of the kind at once.
func TestReconcile_MissingApplicationDefinition_KeepsProjection(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	if err := c.Delete(context.TODO(), def); err != nil {
		t.Fatalf("delete definition: %v", err)
	}

	if _, _, err := reconcileRelease(t, c); err != nil {
		t.Fatalf("a missing ApplicationDefinition must not error: %v", err)
	}

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_RefusedValue_UnwatchedSourceKeepsRetrying pins that a refusal on
// the name-driven leg is not TERMINAL. Nothing watches that source, so without a
// resync an operator that writes a placeholder before the real certificate would
// never be published at all.
func TestReconcile_RefusedValue_UnwatchedSourceKeepsRetrying(t *testing.T) {
	src := operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte("placeholder")})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), src,
	).Build()

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a refused value must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("a refused value on an UNWATCHED source must keep being retried, got %+v", res)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("a non-certificate value must not be projected")
	}

	// The operator replaces the placeholder with the real certificate. Only the
	// resync can carry that through, since nothing watches this source.
	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(src), live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data[caCertKey] = []byte(testCA)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("write the real certificate: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_RefusedValue_WatchedSourceDoesNotPoll pins the other half: a
// labelled source IS watched, so a refusal there needs no timer — the next write
// to the Secret wakes the reconciler. Polling it would be pure churn.
func TestReconcile_RefusedValue_WatchedSourceDoesNotPoll(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte("placeholder")}, nil),
	).Build()

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a refused value must not error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a refused value on a WATCHED source must not poll, got %+v", res)
	}
}

// TestReconcile_LabelledSource_HonoursKeyAnnotation pins the per-object key
// override on the label leg.
func TestReconcile_LabelledSource_HonoursKeyAnnotation(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{
			corev1.TLSCertKey:       []byte(testCA),
			corev1.TLSPrivateKeyKey: []byte(testKey),
		}, map[string]string{SourceKeyAnnotation: corev1.TLSCertKey}),
	).Build()

	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_DeclaredSourceAbsent_DoesNotFallBackToLabel pins that the
// declared leg's authority does not lapse when its source is merely absent. A
// declared engine whose operator has not yet minted <release>-ca, with a forged
// labelled Secret already sitting in the namespace, must WAIT for the operator —
// never fall back to the label. A fallback-on-absence would hand the window
// between "release created" and "operator mints CA" to a forger.
func TestReconcile_DeclaredSourceAbsent_DoesNotFallBackToLabel(t *testing.T) {
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testLabelledCA,
			Namespace: testNamespace,
			Labels:    map[string]string{SourceLabel: trueValue, SourceReleaseLabel: testRelease},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCAForged)},
	}
	// declaredCA() names <release>-ca, which is deliberately NOT created here.
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), forged,
	).Build()

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("an absent declared source must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("an absent declared source must be retried (waiting for the operator), got %+v", res)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("nothing must be projected: the label leg must not be used as a fallback for a declared engine")
	}
}

// TestReconcile_ForgedLabelledSecretCannotOverrideDeclaration pins the security
// contract behind the precedence: for a DECLARED engine the platform-set
// declaration is authoritative, and a labelled Secret — which anyone with
// Secret-write in the namespace can forge — is never consulted.
//
// The scenario is the attack. CNPG's genuine, operator-created <release>-ca is
// present; alongside it sits a forged Secret carrying the exact opt-in labels an
// attacker would set (publish-ca-cert=true, publish-ca-cert-release naming this
// release) and an attacker-chosen certificate. The projection must lift CNPG's
// CA, not the forged one — otherwise a namespace-local Secret could swap out the
// trust anchor the tenant is handed as vouched.
func TestReconcile_ForgedLabelledSecretCannotOverrideDeclaration(t *testing.T) {
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testLabelledCA,
			Namespace: testNamespace,
			Labels: map[string]string{
				SourceLabel:        trueValue,
				SourceReleaseLabel: testRelease,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCAForged)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		forged,
	).Build()

	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCA) // CNPG's declared CA, never the forged one
	if got.Annotations[SourceRefAnnotation] != testNamespace+"/"+testOperatorCA {
		t.Errorf("the declared source must be authoritative, got source %q", got.Annotations[SourceRefAnnotation])
	}
}

// TestReconcile_LabelledSource_RefusesPrivateKeyMaterial pins the fail-closed
// guard on the label leg too — both legs share the write path, so both are
// guarded by construction.
func TestReconcile_LabelledSource_RefusesPrivateKeyMaterial(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA + testKey)}, nil),
	).Build()

	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("a source carrying private key material must not be projected")
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonPrivateKeyRefused) {
		t.Errorf("expected a %s Warning event, got %v", reasonPrivateKeyRefused, w)
	}
}

// TestReconcile_PoisonedRotationKeepsCleanProjection pins that the guard also
// protects an EXISTING projection: a source that starts carrying key material
// must not overwrite the key-free copy the tenant already trusts.
func TestReconcile_PoisonedRotationKeepsCleanProjection(t *testing.T) {
	src := labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), appDef(nil), src).Build()
	mustReconcile(t, c)

	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(src), live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data[caCertKey] = []byte(testCA + testKey)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("poison source: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_RequiresCertificatePEM pins the positive half of the content
// check: a value that is not a PEM certificate is not a trust anchor.
func TestReconcile_RequiresCertificatePEM(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte("not a certificate")}, nil),
	).Build()

	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("a non-certificate value must not be projected")
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonInvalidCACert) {
		t.Errorf("expected a %s Warning event, got %v", reasonInvalidCACert, w)
	}
}

// TestReconcile_UpdatePreservesAdmissionLabels pins that refreshing a
// projection keeps the labels the lineage admission webhook stamped on it.
// Replacing the label map wholesale would strip the tenantresource label the
// tenant's read path depends on.
func TestReconcile_UpdatePreservesAdmissionLabels(t *testing.T) {
	src := labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), appDef(nil), src).Build()
	mustReconcile(t, c)

	proj := mustProjection(t, c)
	proj.Labels[corev1alpha1.TenantResourceLabelKey] = corev1alpha1.TenantResourceLabelValue
	proj.Labels[managedByCozystackLabel] = trueValue
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("stamp admission labels: %v", err)
	}

	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(src), live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Data[caCertKey] = []byte(testCARot)
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("rotate source: %v", err)
	}
	mustReconcile(t, c)

	got := mustProjection(t, c)
	if got.Labels[corev1alpha1.TenantResourceLabelKey] != corev1alpha1.TenantResourceLabelValue {
		t.Errorf("update stripped the tenantresource label, labels=%v", got.Labels)
	}
	if got.Labels[managedByCozystackLabel] != trueValue {
		t.Errorf("update stripped the managed-by-cozystack label, labels=%v", got.Labels)
	}
}

// TestReconcile_NeverOverwritesForeignSecret pins the collision contract for
// a Secret at the canonical name that is neither a projection nor the source.
func TestReconcile_NeverOverwritesForeignSecret(t *testing.T) {
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{caCertKey: []byte("FOREIGN"), caKeyKey: []byte(testKey)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil),
		foreign,
	).Build()

	rec := mustReconcile(t, c)

	got := mustProjection(t, c)
	if string(got.Data[caCertKey]) != "FOREIGN" || len(got.Data) != 2 {
		t.Errorf("the foreign Secret must be left untouched, got %v", keysOf(got.Data))
	}
	if got.Labels[ManagedLabel] == trueValue {
		t.Errorf("the foreign Secret must never be adopted")
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCollision) {
		t.Errorf("expected a %s Warning event, got %v", reasonCollision, w)
	}
}

// TestReconcile_AmbiguousLabelledSources pins that two labelled sources in
// one release is a refusal, not a coin flip: publishing the wrong CA breaks
// verification for every tenant client.
func TestReconcile_AmbiguousLabelledSources(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
		labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil),
		labelledSecret(testRelease+"-other-ca", map[string][]byte{caCertKey: []byte(testCARot)}, nil),
	).Build()

	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("an ambiguous source must not be projected")
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonAmbiguousSource) {
		t.Errorf("expected a %s Warning event, got %v", reasonAmbiguousSource, w)
	}
}

// TestReconcile_ForeignLabelledSecretIsIgnored pins that a labelled Secret
// belonging to a DIFFERENT application is never projected onto this release.
func TestReconcile_ForeignLabelledSecretIsIgnored(t *testing.T) {
	other := labelledSecret("kafka-bar-ca", map[string][]byte{caCertKey: []byte(testCA)}, nil)
	other.Labels[appsv1alpha1.ApplicationKindLabel] = "Kafka"
	other.Labels[appsv1alpha1.ApplicationNameLabel] = "bar"
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil), other,
	).Build()

	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("another application's CA must never be projected onto this release")
	}
}

// TestReconcile_NoSourceIsNoOp pins that a release with neither a labelled
// source nor a declaration is left alone, and is not requeued forever.
func TestReconcile_NoSourceIsNoOp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil),
	).Build()

	rec, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a release with no CA source must not error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a release with no CA source must not be requeued, got %+v", res)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("nothing must be projected")
	}
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("a release with no CA source must not warn, got %v", w)
	}
}

// TestReconcile_NonApplicationReleaseIsNoOp pins that the platform's own
// HelmReleases (which carry no application labels) are skipped.
func TestReconcile_NonApplicationReleaseIsNoOp(t *testing.T) {
	hr := helmRelease()
	hr.Labels = nil
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(hr).Build()

	rec, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a non-application release must not error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a non-application release must not be requeued, got %+v", res)
	}
	if w := warnings(t, rec); len(w) != 0 {
		t.Errorf("a non-application release must not warn, got %v", w)
	}
}

// TestReconcile_MissingReleaseIsNoOp pins that a deleted release reconciles
// cleanly — the projection is owner-referenced to it, so its removal is the
// garbage collector's business.
func TestReconcile_MissingReleaseIsNoOp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	if _, _, err := reconcileRelease(t, c); err != nil {
		t.Fatalf("absent release must not error: %v", err)
	}
}

// TestReconcile_ProjectionSelfHeals pins that a projection deleted out of
// band is recreated on the next reconcile.
func TestReconcile_ProjectionSelfHeals(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)

	if err := c.Delete(context.TODO(), mustProjection(t, c)); err != nil {
		t.Fatalf("delete projection: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_OptOut_WithdrawsProjection pins the opt-out contract. Flipping
// the publish label to "false" is how a chart turns TLS off, and the owner
// reference does nothing about it — it only collects the projection when the
// whole application is deleted. Without an explicit withdrawal the tenant would
// keep reading a trust anchor the platform has stopped standing behind.
func TestReconcile_OptOut_WithdrawsProjection(t *testing.T) {
	src := labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), appDef(nil), src).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	live := &corev1.Secret{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(src), live); err != nil {
		t.Fatalf("get source: %v", err)
	}
	live.Labels[SourceLabel] = "false"
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatalf("opt out: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("opting out must withdraw the projection, but %s is still published", testProjection)
	}
}

// TestReconcile_LabelLeg_DeletedSourceKeepsProjection pins the distinction the
// withdrawal turns on, on the leg where getting it wrong is routine.
//
// Deleting the CA Secret is the STANDARD way to force a cert-manager reissuance,
// and the operator writes a new one moments later. If a momentarily absent source
// were read as "this release stopped publishing", the controller would rip the
// trust anchor out from under every client of a release that is merely rotating
// its CA — and it would do so deterministically, not as a race: the DELETE event
// carries the label, so the reconcile is triggered by the deletion itself.
//
// The declared leg already waits in this situation. The legs must not disagree
// about the identical fact.
func TestReconcile_LabelLeg_DeletedSourceKeepsProjection(t *testing.T) {
	src := certManagerSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), appDef(nil), src).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	// cert-manager reissuance: the source Secret is deleted and will be recreated.
	if err := c.Delete(context.TODO(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a transiently absent source must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("an absent source must keep being retried, got %+v", res)
	}
	assertKeyFree(t, mustProjection(t, c), testCA)

	// And when the operator reissues, the new certificate is published.
	reissued := certManagerSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCARot)})
	if err := c.Create(context.TODO(), reissued); err != nil {
		t.Fatalf("reissue source: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCARot)
}

// TestReconcile_Prune_NeverDeletesForeignSecret pins that the withdrawal obeys
// the same never-touch-a-stranger rule as the write path: a Secret at the
// canonical name that the controller did not create is not its to delete.
func TestReconcile_Prune_NeverDeletesForeignSecret(t *testing.T) {
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{caCertKey: []byte("FOREIGN"), caKeyKey: []byte(testKey)},
	}
	// No labelled source and no declaration: the release publishes nothing, so
	// the prune path runs — and must leave this Secret alone.
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil), foreign,
	).Build()

	mustReconcile(t, c)

	got, ok := getSecret(t, c, testProjection)
	if !ok {
		t.Fatalf("a Secret the controller did not create must never be deleted")
	}
	if string(got.Data[caCertKey]) != "FOREIGN" || len(got.Data) != 2 {
		t.Errorf("the foreign Secret must be left untouched, got %v", keysOf(got.Data))
	}
}

// TestReconcile_BrokenDeclaration_KeepsProjection pins the blast radius of a
// platform-side typo. An ApplicationDefinition is shipped once and applies to
// EVERY release of its kind, so if an unrenderable template were treated as
// "this engine publishes nothing", one bad character would withdraw the trust
// anchor of every postgres in the cluster at once. It must not.
func TestReconcile_BrokenDeclaration_KeepsProjection(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.CACert.SourceSecretName = "{{ .release" // never renders
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("break the declaration: %v", err)
	}

	rec := mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonInvalidDeclaration) {
		t.Errorf("expected a %s Warning event, got %v", reasonInvalidDeclaration, w)
	}
}

// TestReconcile_UnnameableDeclaration_WarnsAndKeepsProjection pins that a
// declaration rendering to something that is not a Secret name fails the same
// way as one that does not render at all.
//
// Not because the name is dangerous — it comes from a shipped
// ApplicationDefinition, which no tenant can write — but because of how it
// failed. An invalid name is simply a name nothing is stored under, so the Get
// returned NotFound and the reconciler read that as "the operator has not minted
// the CA yet" and settled into a quiet, permanent wait. A platform typo
// presented as a normal bootstrap: no event, no error, and a trust anchor that
// never appears with nothing to say why. The reconciler already has the right
// verdict for this class — errUnusableDeclaration, which warns and pointedly
// does NOT prune — so route it there.
func TestReconcile_UnnameableDeclaration_WarnsAndKeepsProjection(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	// Renders cleanly. Is not a Secret name.
	def.Spec.CACert.SourceSecretName = "{{ .kind }}_CA"
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("break the declaration: %v", err)
	}

	rec, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("an unusable declaration must not error: %v", err)
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonInvalidDeclaration) {
		t.Errorf("expected a %s Warning event, got %v", reasonInvalidDeclaration, w)
	}
	// A platform-side typo is not a tenant's decision to stop publishing, and
	// applies to every release of the kind at once.
	assertKeyFree(t, mustProjection(t, c), testCA)
	// ApplicationDefinitions are watched, so correcting it wakes the reconciler:
	// polling would only add load to a string that will never resolve.
	if res.RequeueAfter != 0 {
		t.Errorf("an unusable declaration must not poll, got %+v", res)
	}
}

// TestReconcile_VanishedDeclaredSource_KeepsProjection pins the other
// indeterminate state: a declared source that is not there right now may be an
// operator that has not minted it yet, or one that is mid-rotation. Absence is
// not an opt-out, so the trust anchor the tenant already holds stays put.
func TestReconcile_VanishedDeclaredSource_KeepsProjection(t *testing.T) {
	src := operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), src,
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	if err := c.Delete(context.TODO(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a vanished source must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a vanished source must keep being retried, got %+v", res)
	}
	assertKeyFree(t, mustProjection(t, c), testCA)
}

// TestReconcile_DeclarationRemovedAfterSourceVanished_WithdrawsProjection is
// the ordering that used to strand a retired trust anchor in a tenant's
// namespace forever.
//
// Withdrawal asks "does the Secret this projection came from still exist?", and
// treats absence as indeterminate — correctly, because deleting the CA Secret is
// how a cert-manager reissue is forced. But that question is the WRONG one once
// the DECLARATION is gone: the source vanishing first (an operator tearing its
// PKI down, a reissue in flight) and the declaration being removed second is an
// ordinary sequence, and it left the controller answering "the source is merely
// absent, hold" about a release the platform had definitively stopped vouching
// for. Nothing ever revisited it: the projection carries no publish label, so it
// is neither cached nor watched.
//
// Removing a declaration is not the absence of evidence, it is evidence of
// absence — a platform-side edit to an object no tenant can write. It withdraws,
// whatever became of the Secret it used to name.
func TestReconcile_DeclarationRemovedAfterSourceVanished_WithdrawsProjection(t *testing.T) {
	src := operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), src,
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	// 1. The source disappears. Indeterminate while the declaration stands, so
	//    the anchor is held — this is the reissue-safety property, and it must
	//    still hold at this point.
	if err := c.Delete(context.TODO(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	mustReconcile(t, c)
	assertKeyFree(t, mustProjection(t, c), testCA)

	// 2. THEN the declaration is removed. The platform has stopped publishing a
	//    CA for this kind, and it said so.
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.CACert = nil
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("remove the declaration: %v", err)
	}

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("removing a declaration must not error: %v", err)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("removing the declaration must withdraw %s, but the tenant still holds a retired trust anchor", testProjection)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a withdrawn projection needs no requeue, got %+v", res)
	}
}

// TestReconcile_DeclarationRemovedWhileSourceStands_WithdrawsProjection is the
// same opt-out arriving in the other order, and pins that recording the leg did
// not cost the simpler case its answer.
func TestReconcile_DeclarationRemovedWhileSourceStands_WithdrawsProjection(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	mustProjection(t, c)

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.CACert = nil
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("remove the declaration: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("removing the declaration must withdraw %s", testProjection)
	}
}

// TestReconcile_ExtraOwnerReferenceIsStripped pins that the projection is owned
// by the release and by NOTHING else.
//
// The owner reference is the entire mechanism that retires a trust anchor when
// its application is deleted — the controller has no other hook for it, and says
// so. But Kubernetes garbage collection removes a dependent only once EVERY
// owner in its list is gone, and appending a second, non-controller reference is
// something any Secret writer in the namespace can do: the API server rejects
// only a second Controller=true. Checking that the release is PRESENT among the
// owners let that reference through, and it would then outlive the release and
// keep a trust anchor for a deleted application readable.
//
// Nothing legitimate co-owns a projection, so the extra reference is drift and
// the write path normalizes it away.
func TestReconcile_ExtraOwnerReferenceIsStripped(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)

	// A namespace actor appends an owner of their own. It is not the controller,
	// so the API server accepts it alongside the real one — and it is enough to
	// keep the projection alive after the HelmRelease is collected.
	proj := mustProjection(t, c)
	proj.OwnerReferences = append(proj.OwnerReferences, metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Name:       "squatter",
		UID:        types.UID("99999999-9999-9999-9999-999999999999"),
	})
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("append a second owner: %v", err)
	}

	mustReconcile(t, c)

	got := mustProjection(t, c)
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("the projection must be owned by the release alone, got %d owners: %+v",
			len(got.OwnerReferences), got.OwnerReferences)
	}
	if got.OwnerReferences[0].UID != testHRUID {
		t.Errorf("the surviving owner must be the release, got %+v", got.OwnerReferences[0])
	}
	// The anchor itself is untouched by the normalization.
	assertKeyFree(t, got, testCA)
}

// TestReconcile_BlockOwnerDeletionDriftIsNormalized pins that the projection's
// sole owner reference is re-homed when only its BlockOwnerDeletion flag drifts
// to true.
//
// releaseOwnerRef sets BlockOwnerDeletion:false deliberately — the trust anchor
// must never hold up the teardown of the application it belongs to. If an actor
// or another controller flips that flag to true on the projection's owner
// reference, a foreground deletion of the HelmRelease would then block on the
// projection: the exact opposite of the invariant the flag exists to keep. So
// the flag is part of the owner's identity for the drift check, and the write
// path replaces the reference to restore BlockOwnerDeletion:false.
func TestReconcile_BlockOwnerDeletionDriftIsNormalized(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)

	// Flip BlockOwnerDeletion on the sole, otherwise-correct owner reference.
	// Nothing else about the reference or the projection changes, so this flag
	// is the only thing the next reconcile could act on.
	proj := mustProjection(t, c)
	if len(proj.OwnerReferences) != 1 {
		t.Fatalf("precondition: projection must have exactly one owner, got %+v", proj.OwnerReferences)
	}
	proj.OwnerReferences[0].BlockOwnerDeletion = ptr.To(true)
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("set BlockOwnerDeletion drift: %v", err)
	}

	mustReconcile(t, c)

	got := mustProjection(t, c)
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("the projection must be owned by the release alone, got %+v", got.OwnerReferences)
	}
	if ptr.Deref(got.OwnerReferences[0].BlockOwnerDeletion, false) {
		t.Errorf("BlockOwnerDeletion drift must be normalized back to false so teardown is never held up, got %+v", got.OwnerReferences[0])
	}
}

// TestOwnedSolelyBy separates the two questions the drift check used to
// conflate: is the release among the owners, and is it the only one.
func TestOwnedSolelyBy(t *testing.T) {
	want := releaseOwnerRef(helmRelease())
	other := metav1.OwnerReference{
		APIVersion: "v1", Kind: "ConfigMap", Name: "squatter",
		UID: types.UID("99999999-9999-9999-9999-999999999999"),
	}
	for name, tc := range map[string]struct {
		refs []metav1.OwnerReference
		want bool
	}{
		"exactly the release":   {refs: []metav1.OwnerReference{want}, want: true},
		"release plus a second": {refs: []metav1.OwnerReference{want, other}},
		"second plus release":   {refs: []metav1.OwnerReference{other, want}},
		"someone else entirely": {refs: []metav1.OwnerReference{other}},
		"no owners":             {refs: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ownedSolelyBy(tc.refs, want); got != tc.want {
				t.Errorf("ownedSolelyBy(%d refs) = %v, want %v", len(tc.refs), got, tc.want)
			}
		})
	}
}

// TestReconcile_Prune_IgnoresForeignNamespaceSourceRef pins that the withdrawal
// decision does not follow a source reference out of the release namespace.
//
// The recorded reference is an annotation on a Secret in the TENANT's namespace,
// so the tenant can write it; the reader that consumes it is the controller's
// cluster-wide APIReader. Pointing it at a Secret that exists nowhere would
// otherwise pin the prune decision on "merely absent, hold" forever and keep a
// retired trust anchor readable — a withdrawal the tenant suppresses by editing
// their own object. Every reference this controller writes is same-namespace by
// construction, so anything else is a record it did not write: unusable, treated
// as no record, which withdraws.
func TestReconcile_Prune_IgnoresForeignNamespaceSourceRef(t *testing.T) {
	src := labelledSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}, nil)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(helmRelease(), appDef(nil), src).Build()
	mustReconcile(t, c)

	// The tenant re-points the record at another namespace, then opts the real
	// source out. Deleting the source instead would be the same trick.
	proj := mustProjection(t, c)
	proj.Annotations[SourceRefAnnotation] = "kube-system/never-going-to-exist"
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("forge the source ref: %v", err)
	}
	if err := c.Delete(context.TODO(), src); err != nil {
		t.Fatalf("delete source: %v", err)
	}

	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a source ref outside %s must not suppress the withdrawal of %s", testNamespace, testProjection)
	}
}

// TestRecordedSource pins the parse and the namespace confinement together: a
// reference is usable only when it is well-formed AND names the release's own
// namespace.
func TestRecordedSource(t *testing.T) {
	for name, tc := range map[string]struct {
		ref      string
		wantName string
		wantOK   bool
	}{
		"same namespace":      {ref: testNamespace + "/" + testLabelledCA, wantName: testLabelledCA, wantOK: true},
		"other namespace":     {ref: "kube-system/some-secret"},
		"no namespace":        {ref: testLabelledCA},
		"empty namespace":     {ref: "/" + testLabelledCA},
		"empty name":          {ref: testNamespace + "/"},
		"empty":               {ref: ""},
		"namespace-like name": {ref: "kube-system/" + testLabelledCA},
	} {
		t.Run(name, func(t *testing.T) {
			proj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{SourceRefAnnotation: tc.ref},
			}}
			gotName, gotOK := recordedSource(proj, testNamespace)
			if gotOK != tc.wantOK || gotName != tc.wantName {
				t.Errorf("recordedSource(%q) = (%q, %v), want (%q, %v)", tc.ref, gotName, gotOK, tc.wantName, tc.wantOK)
			}
		})
	}
}

// TestReconcile_ProjectionRecordsItsLeg pins the annotation the withdrawal
// decision reads. It is not decoration: without it "no source resolved" cannot
// be told apart from "the declaration that produced this is gone", because by
// then there is no declaration left to ask.
func TestReconcile_ProjectionRecordsItsLeg(t *testing.T) {
	for name, tc := range map[string]struct {
		objects []client.Object
		want    string
	}{
		"declared leg": {
			objects: []client.Object{
				appDef(declaredCA()),
				operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
			},
			want: modeDeclared,
		},
		"label leg": {
			objects: []client.Object{
				appDef(nil),
				certManagerSecret(testLabelledCA, map[string][]byte{caCertKey: []byte(testCA)}),
			},
			want: modeLabelled,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).
				WithObjects(append([]client.Object{helmRelease()}, tc.objects...)...).Build()
			mustReconcile(t, c)

			if got := mustProjection(t, c).Annotations[SourceModeAnnotation]; got != tc.want {
				t.Errorf("projection %s = %q, want %q", SourceModeAnnotation, got, tc.want)
			}
		})
	}
}

// TestReconcile_RecreatedRelease_RehomesProjection pins the owner reference on
// a release that was deleted and recreated under the same name — the ordinary
// shape of "delete the app, create it again". The new HelmRelease has a NEW
// UID, so the projection left behind by the old one carries a stale reference.
// It must be REPLACED: the API server rejects an object carrying two owner
// references with Controller=true, so appending would wedge the reconciler on
// an Invalid error instead of re-homing the projection.
func TestReconcile_RecreatedRelease_RehomesProjection(t *testing.T) {
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: helmv2.GroupVersion.String(),
				Kind:       helmv2.HelmReleaseKind,
				Name:       testRelease,
				UID:        types.UID("99999999-9999-9999-9999-999999999999"), // the PREVIOUS release
				Controller: ptr.To(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCA)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCARot), caKeyKey: []byte(testKey)}),
		stale,
	).Build()

	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCARot)

	var controllers int
	for _, ref := range got.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			controllers++
		}
	}
	if controllers != 1 {
		t.Fatalf("a projection must carry exactly one controller owner reference, got %d: %+v",
			controllers, got.OwnerReferences)
	}
	if got.OwnerReferences[0].UID != testHRUID {
		t.Errorf("the projection must re-home onto the live release (uid %s), got %s",
			testHRUID, got.OwnerReferences[0].UID)
	}
}

// TestReconcile_IsIdempotent pins that a second reconcile with an unchanged
// source is a no-op — no spurious writes, no event storm.
func TestReconcile_IsIdempotent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	first := mustProjection(t, c)

	mustReconcile(t, c)
	second := mustProjection(t, c)

	if first.ResourceVersion != second.ResourceVersion {
		t.Errorf("an unchanged source must not rewrite the projection (%s -> %s)",
			first.ResourceVersion, second.ResourceVersion)
	}
}

func TestRenderSourceName(t *testing.T) {
	app := application{Group: appsGroup, Kind: testAppKind, Name: testAppName}
	for _, tc := range []struct {
		name     string
		template string
		want     string
		wantErr  bool
	}{
		{"release", "{{ .release }}-ca", "postgres-mydb-ca", false},
		{"name", "postgres-{{ .name }}-ca", "postgres-mydb-ca", false},
		{"lowercased kind", "{{ .kind }}-{{ .name }}-ca", "postgres-mydb-ca", false},
		{"namespace", "{{ .namespace }}-ca", "tenant-foo-ca", false},
		{"literal", "static-ca", "static-ca", false},
		{"broken template", "{{ .release", "", true},
		// A template that renders cleanly to something that is not a Secret name
		// is the same class of mistake as one that does not render at all: a
		// platform-side typo in a shipped ApplicationDefinition. It must be
		// reported as one rather than smuggled into a Get.
		{"uppercase", "Postgres-CA", "", true},
		{"underscore", "postgres_ca", "", true},
		{"leading dash", "-postgres-ca", "", true},
		{"path separator", "{{ .namespace }}/{{ .release }}-ca", "", true},
		{"trailing dot", "postgres-ca.", "", true},
		{"space inside", "postgres ca", "", true},
		{"too long", "{{ .release }}-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", true},
		{"renders empty", "{{ .missing }}", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderSourceName(tc.template, app, testRelease, testNamespace)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("renderSourceName(%q) = %q, want an error", tc.template, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("renderSourceName(%q): %v", tc.template, err)
			}
			if got != tc.want {
				t.Errorf("renderSourceName(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestMissingSourceWait pins the retry cadence for an absent declared source:
// fast while the release is bootstrapping (this is the tenant's wait for a
// usable trust anchor), and back to the ordinary resync afterwards — a release
// with TLS switched off declares a source whose CA is never minted, and must
// not poll at the bootstrap cadence forever.
func TestMissingSourceWait(t *testing.T) {
	young := helmRelease()
	young.CreationTimestamp = metav1.NewTime(time.Now().Add(-1 * time.Minute))
	if got := missingSourceWait(young); got != missingSourceRetry {
		t.Errorf("a bootstrapping release must retry fast, got %v", got)
	}

	old := helmRelease()
	old.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * bootstrapWindow))
	if got := missingSourceWait(old); got != resyncInterval {
		t.Errorf("a settled release must fall back to the resync, got %v", got)
	}
}

func TestContainsPrivateKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"pkcs1 rsa", "x\n-----BEGIN RSA PRIVATE KEY-----\n", true},
		{"pkcs8", "-----BEGIN PRIVATE KEY-----", true},
		{"ec", "-----BEGIN EC PRIVATE KEY-----", true},
		{"encrypted", "-----BEGIN ENCRYPTED PRIVATE KEY-----", true},
		{"lowercase header", "-----begin rsa private key-----", true},
		{"openssh", "-----BEGIN OPENSSH PRIVATE KEY-----", true},
		// PGP puts BLOCK after "PRIVATE KEY", so a guard anchored to the closing
		// dashes never sees it. It is a private key by any reading, and gpg
		// exports one with a single flag.
		{"pgp", "-----BEGIN PGP PRIVATE KEY BLOCK-----", true},
		{"certificate only", testCA, false},
		{"private key words in body", "-----BEGIN CERTIFICATE-----\nPRIVATE KEY\n-----END CERTIFICATE-----", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsPrivateKey(tc.in); got != tc.want {
				t.Errorf("containsPrivateKey(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestProjectionData pins the single write-path sanitizer: it emits exactly
// one key and refuses anything that is not a bare certificate.
func TestProjectionData(t *testing.T) {
	got, err := projectionData([]byte(testCA))
	if err != nil {
		t.Fatalf("projectionData(cert): %v", err)
	}
	if len(got) != 1 || string(got[caCertKey]) != testCA {
		t.Errorf("projectionData must emit exactly ca.crt, got %v", got)
	}
	if _, err := projectionData([]byte(testCA + testKey)); err == nil {
		t.Errorf("projectionData must refuse private key material")
	}
	if _, err := projectionData([]byte("garbage")); err == nil {
		t.Errorf("projectionData must refuse a non-certificate value")
	}
}

// TestProjectionData_RefusesCertificateArmouredNonCertificate is the guard's
// actual claim, as opposed to the one a header match can make.
//
// The controller's fail-closed boundary is "this value IS a trust anchor", and
// a tenant is handed the result as a vouched CA. Matching a BEGIN CERTIFICATE
// line only establishes that someone typed one: every case below wears correct
// certificate armour and carries no key header, so a header match admits them
// all, and the tenant would be handed bytes no verifier can load as a CA.
//
// The guard therefore DECODES the PEM and hands every block to
// x509.ParseCertificate. That is not belt-and-braces — a header match cannot
// distinguish a certificate from its costume, and the reviewers who flagged this
// were right that the code did not do what its comment claimed.
func TestProjectionData_RefusesCertificateArmouredNonCertificate(t *testing.T) {
	for name, value := range map[string]string{
		// The exact shape this suite's own fixtures used to have: valid base64,
		// correct armour, and a DER prefix that stops mid-structure.
		"armour around truncated DER": "-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIQ\n-----END CERTIFICATE-----\n",
		// Not even base64 inside the armour.
		"armour around non-base64": "-----BEGIN CERTIFICATE-----\nROTATEDROTATEDROTATED\n-----END CERTIFICATE-----\n",
		// Well-formed base64 that decodes to bytes x509 rejects.
		"armour around plain text": "-----BEGIN CERTIFICATE-----\n" +
			base64.StdEncoding.EncodeToString([]byte("this is not a certificate, it is a sentence")) +
			"\n-----END CERTIFICATE-----\n",
		"header with no body":  "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n",
		"header with no close": "-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIQ\n",
		"empty":                "",
		"whitespace only":      "   \n\t\n",
		// A PEM block of the right shape but the wrong type is not a trust
		// anchor either, however parseable its contents.
		"a public key, not a certificate": string(pem.EncodeToMemory(&pem.Block{
			Type: "PUBLIC KEY", Bytes: mustPublicKeyDER(),
		})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := projectionData([]byte(value)); !errors.Is(err, errNotCertificate) {
				t.Errorf("projectionData(%q) error = %v, want errNotCertificate", value, err)
			}
		})
	}
}

// TestProjectionData_AcceptsRealCertificates keeps the guard from failing closed
// on the values it exists to publish: a single CA, and the multi-certificate
// bundle an intermediate chain arrives as.
//
// The projection is the certificate blocks re-encoded, not the input echoed
// back. For a value that is already canonical PEM the two are byte-identical
// (want == in); a value with surrounding whitespace comes back as the bare
// blocks, because everything the guard did not parse as a certificate is
// dropped.
func TestProjectionData_AcceptsRealCertificates(t *testing.T) {
	bundle := testCA + testCARot
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"one certificate", testCA, testCA},
		{"a chain of two", bundle, bundle},
		{"trailing whitespace", testCA + "\n\n  \n", testCA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projectionData([]byte(tc.in))
			if err != nil {
				t.Fatalf("projectionData(%s) = %v, want accepted", tc.name, err)
			}
			if string(got[caCertKey]) != tc.want {
				t.Errorf("projectionData(%s) published %q, want the validated blocks %q", tc.name, got[caCertKey], tc.want)
			}
		})
	}
}

// TestProjectionData_RefusesTrailingGarbage pins that the guard accounts for
// EVERY byte it publishes, not just the first block it can find. A value that
// parses as a certificate and then carries something else is not a clean trust
// anchor, and appending is the cheapest way to try to smuggle bytes past a
// first-match check.
func TestProjectionData_RefusesTrailingGarbage(t *testing.T) {
	if _, err := projectionData([]byte(testCA + "and then some trailing bytes")); !errors.Is(err, errNotCertificate) {
		t.Errorf("projectionData(cert+garbage) error = %v, want errNotCertificate", err)
	}
}

// TestProjectionData_KeyGuardWinsOverTheParse pins the ORDER of the two checks.
// A private key wrapped in certificate armour must be refused as key material —
// the reason the controller exists — rather than as a parse failure, because the
// two produce different Warning Events and the operator must be told which
// mistake they made.
func TestProjectionData_KeyGuardWinsOverTheParse(t *testing.T) {
	if _, err := projectionData([]byte(testCA + testKey)); !errors.Is(err, errPrivateKey) {
		t.Errorf("projectionData(cert+key) error = %v, want errPrivateKey", err)
	}
}

// TestProjectionData_StripsKeyMaterialSmuggledAroundBlocks is the write path's
// deepest guarantee: the projection is REBUILT from the certificate blocks it
// validated, never copied from the input.
//
// The header guard (containsPrivateKey) only sees "-----BEGIN ... PRIVATE
// KEY-----". A raw PKCS#8 DER key, or a JSON Web Key, carries no such header, so
// it walks past that guard. pem.Decode then skips it — text before the first
// block and between blocks is silently dropped by the decoder — so the
// certificate loop never inspects it either. A projection that cloned the input
// once it validated would therefore hand the tenant a trust anchor with a
// private key tucked in front of, or between, the certificates. Rebuilding from
// the parsed DER makes that byte structurally unreachable.
func TestProjectionData_StripsKeyMaterialSmuggledAroundBlocks(t *testing.T) {
	derKey := mustPrivateKeyDER()
	// A private JWK: text, no PEM header, sits before the certificate.
	jwkKey := `{"kty":"EC","crv":"P-256","d":"c2VjcmV0LXByaXZhdGUta2V5LW1hdGVyaWFs"}`

	concat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{
			// Raw DER key, newline, then a real certificate.
			name: "leading DER key",
			in:   concat(derKey, []byte("\n"), []byte(testCA)),
			want: testCA,
		},
		{
			// A private JWK ahead of the certificate.
			name: "leading JWK key",
			in:   []byte(jwkKey + "\n" + testCA),
			want: testCA,
		},
		{
			// Key bytes wedged BETWEEN two certificate blocks.
			name: "interstitial DER key",
			in:   concat([]byte(testCA), derKey, []byte("\n"), []byte(testCARot)),
			want: testCA + testCARot,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projectionData(tc.in)
			if err != nil {
				t.Fatalf("projectionData(%s) = %v, want the value accepted and sanitized", tc.name, err)
			}
			if string(got[caCertKey]) != tc.want {
				t.Errorf("projectionData(%s) published %q, want only the validated blocks %q", tc.name, got[caCertKey], tc.want)
			}
			if bytes.Contains(got[caCertKey], derKey) {
				t.Errorf("projectionData(%s) leaked raw DER private-key material into the tenant projection", tc.name)
			}
		})
	}
}

// TestProjectionData_DropsHumanReadablePreamble pins that the text dump
// `openssl x509 -text` prints before each certificate — which pem.Decode
// tolerates — is not republished to the tenant. The projection carries the
// certificate blocks and nothing else.
func TestProjectionData_DropsHumanReadablePreamble(t *testing.T) {
	preamble := "Certificate:\n    Data:\n        Version: 3 (0x2)\n"
	in := preamble + testCA + preamble + testCARot
	got, err := projectionData([]byte(in))
	if err != nil {
		t.Fatalf("projectionData(preamble+chain) = %v, want accepted", err)
	}
	if string(got[caCertKey]) != testCA+testCARot {
		t.Errorf("projectionData must publish only the certificate blocks, got %q", got[caCertKey])
	}
	if bytes.Contains(got[caCertKey], []byte("Certificate:")) {
		t.Errorf("projectionData leaked human-readable preamble into the tenant projection")
	}
}

func TestIsSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"labelled", map[string]string{SourceLabel: trueValue}, true},
		{"explicitly disabled", map[string]string{SourceLabel: "false"}, false},
		{"unlabelled", map[string]string{"app.kubernetes.io/name": "postgres"}, false},
		{"no labels", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}}
			if got := isSource(s); got != tc.want {
				t.Errorf("isSource(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func TestSourceKey(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{"default", nil, caCertKey},
		{"annotated", map[string]string{SourceKeyAnnotation: corev1.TLSCertKey}, corev1.TLSCertKey},
		{"empty annotation falls back", map[string]string{SourceKeyAnnotation: "  "}, caCertKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: tc.annotations}}
			if got := sourceKey(s); got != tc.want {
				t.Errorf("sourceKey(%v) = %q, want %q", tc.annotations, got, tc.want)
			}
		})
	}
}

// TestBelongsToRelease pins how a labelled Secret is attributed to a
// release: by the lineage labels the admission webhook stamps, or by the
// Helm ownership label Flux stamps on every rendered object.
func TestBelongsToRelease(t *testing.T) {
	app := application{Group: appsGroup, Kind: testAppKind, Name: testAppName}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"release label", map[string]string{SourceReleaseLabel: testRelease}, true},
		{"release label of another release", map[string]string{SourceReleaseLabel: "postgres-other"}, false},
		// An explicit release label is the whole answer: a source that names a
		// DIFFERENT release must not be dragged onto this one by a stale lineage
		// label it also happens to carry.
		{"release label overrides contradicting lineage labels", map[string]string{
			SourceReleaseLabel:                 "postgres-other",
			appsv1alpha1.ApplicationGroupLabel: appsGroup,
			appsv1alpha1.ApplicationKindLabel:  testAppKind,
			appsv1alpha1.ApplicationNameLabel:  testAppName,
		}, false},
		{"lineage labels", map[string]string{
			appsv1alpha1.ApplicationGroupLabel: appsGroup,
			appsv1alpha1.ApplicationKindLabel:  testAppKind,
			appsv1alpha1.ApplicationNameLabel:  testAppName,
		}, true},
		{"helm ownership label", map[string]string{helmNameLabel: testRelease}, true},
		{"another application", map[string]string{
			appsv1alpha1.ApplicationGroupLabel: appsGroup,
			appsv1alpha1.ApplicationKindLabel:  "Kafka",
			appsv1alpha1.ApplicationNameLabel:  "bar",
		}, false},
		{"another release", map[string]string{helmNameLabel: "postgres-other"}, false},
		{"no attribution", map[string]string{"app.kubernetes.io/name": "postgres"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}}
			if got := belongsToRelease(s, app, testRelease); got != tc.want {
				t.Errorf("belongsToRelease(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

// TestReconcile_DefinitionChange_ForcesReadmission pins the fix for the
// tenant's read path.
//
// The reconciler does not decide whether a tenant may read the projection — the
// lineage admission webhook does, from the ApplicationDefinition's spec.secrets
// selectors, and it only runs on an actual API write. A projection created
// before its definition selects internal.cozystack.io/tenant-ca is therefore
// stamped tenantresource=false.
//
// When the definition later gains that selector the release IS reconciled
// (definitions are watched), but the data, labels, source and owner have all
// stayed the same. Without the recorded definition revision in the drift check,
// that reconcile writes nothing — so there is no admission, the verdict is never
// revisited, and the tenant stays locked out of a Secret that is now theirs.
// The revision changing must therefore BE drift.
// TestReconcile_SelectorChange_ReleasesTheProjectionToTheWebhook pins the half
// of the re-admission mechanism that a fake client cannot see, and that the
// digest alone does not deliver.
//
// The digest forces a WRITE. A write is not an admission. The lineage webhook
// carries objectSelector managed-by-cozystack DoesNotExist, and Kubernetes
// evaluates objectSelector against BOTH oldObject and newObject, running the
// webhook only if EITHER matches. The webhook stamps that label in the same pass
// as tenantresource, so from the first admission onward both objects carry it,
// neither matches DoesNotExist, and every later UPDATE is skipped — however many
// writes the digest forces.
//
// So the projection has to be handed BACK to the webhook, and dropping the label
// is the only lever that does it: newObject then matches DoesNotExist, the
// webhook runs, and it re-stamps both labels off the new selectors. Without this
// the digest is an elaborate no-op, and it fails in the direction that matters —
// a definition that REVOKES tenant access leaves tenantresource="true" and the
// tenant keeps reading a trust anchor the platform has withdrawn.
//
// Only a selector change does this. Data rotation must not (see
// TestReconcile_UpdatePreservesAdmissionLabels): the verdict is still valid, so
// re-admitting on every rotation would be load for nothing.
func TestReconcile_SelectorChange_ReleasesTheProjectionToTheWebhook(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)

	// Stand in for the admission the real API server would have run on CREATE:
	// the webhook stamped its verdict and its own marker.
	proj := mustProjection(t, c)
	proj.Labels[managedByCozystackLabel] = trueValue
	proj.Labels[corev1alpha1.TenantResourceLabelKey] = "false"
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("stamp admission labels: %v", err)
	}

	// The platform now grants the tenant read access to the trust anchor.
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.Secrets.Include = []*cozyv1alpha1.ApplicationDefinitionResourceSelector{{
		LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{TenantCALabel: trueValue}},
	}}
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("grant access: %v", err)
	}
	mustReconcile(t, c)

	// There is no webhook behind the fake client, so what is asserted here is the
	// PAYLOAD: the object the API server would admit no longer carries the marker,
	// which is exactly what makes objectSelector match and the webhook run.
	got := mustProjection(t, c)
	if _, found := got.Labels[managedByCozystackLabel]; found {
		t.Errorf("a selector change must hand the projection back to the lineage webhook by dropping %q, labels=%v",
			managedByCozystackLabel, got.Labels)
	}
}

func TestReconcile_DefinitionChange_ForcesReadmission(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	).Build()
	mustReconcile(t, c)
	before := mustProjection(t, c)

	// Nothing changed: the projection must not be rewritten.
	mustReconcile(t, c)
	if got := mustProjection(t, c); got.ResourceVersion != before.ResourceVersion {
		t.Fatalf("an unchanged definition must not rewrite the projection (%s -> %s)",
			before.ResourceVersion, got.ResourceVersion)
	}

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}

	// An edit that does NOT touch spec.secrets cannot change the webhook's verdict,
	// so it must not rewrite anything: the digest is deliberately narrower than the
	// definition's resourceVersion, which would churn every projection of the kind
	// on any unrelated field change.
	def.Spec.Application.Plural = "postgresqls"
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("update definition: %v", err)
	}
	mustReconcile(t, c)
	if got := mustProjection(t, c); got.ResourceVersion != before.ResourceVersion {
		t.Errorf("an edit that cannot affect tenant visibility must not rewrite the projection")
	}

	// Now the selectors themselves change — this IS the platform granting the
	// tenant read access to the trust anchor, and it must produce a write so the
	// webhook re-evaluates the tenantresource label it stamped as "false".
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.Secrets.Include = []*cozyv1alpha1.ApplicationDefinitionResourceSelector{{
		LabelSelector: metav1.LabelSelector{
			MatchLabels: map[string]string{TenantCALabel: trueValue},
		},
	}}
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("grant tenant access: %v", err)
	}
	mustReconcile(t, c)

	after := mustProjection(t, c)
	if after.ResourceVersion == before.ResourceVersion {
		t.Errorf("a change to spec.secrets must rewrite the projection so the webhook re-evaluates tenant access")
	}
	if after.Annotations[SelectorsDigestAnnotation] == "" {
		t.Errorf("the projection must record the selectors digest it was written against")
	}
	// The re-admission must not have disturbed the payload.
	assertKeyFree(t, after, testCA)

	// And it must settle: a second reconcile with the same selectors writes nothing.
	settled := mustProjection(t, c)
	mustReconcile(t, c)
	if got := mustProjection(t, c); got.ResourceVersion != settled.ResourceVersion {
		t.Errorf("the re-admission must be one write, not a rewrite on every reconcile")
	}
}

// TestReconcile_TerminatingProjection_WaitsForCollection pins that the
// reconciler does not write to a projection the garbage collector is already
// removing — the state a delete-and-recreate of an application under the same
// name leaves behind. The write would land and then be thrown away with the
// object, so it waits and republishes onto a clean name instead.
func TestReconcile_TerminatingProjection_WaitsForCollection(t *testing.T) {
	terminating := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testProjection,
			Namespace:  testNamespace,
			Labels:     map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			Finalizers: []string{"cozystack.io/test-hold"}, // keeps it terminating
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte("STALE")},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		terminating,
	).Build()
	if err := c.Delete(context.TODO(), terminating); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}

	_, res, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a terminating projection must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a terminating projection must be retried once it is collected, got %+v", res)
	}

	got := mustProjection(t, c)
	if got.DeletionTimestamp == nil {
		t.Fatalf("the fixture must still be terminating for this test to mean anything")
	}
	if string(got.Data[caCertKey]) != "STALE" {
		t.Errorf("nothing may be written to a terminating projection, got %q", got.Data[caCertKey])
	}
}

// TestReconcile_ForgedProjectionIsHealed pins the SAFE direction of the
// adoption gate: an Opaque Secret sitting at the canonical name with the marker
// label and an attacker-chosen ca.crt is overwritten with the genuine CA and the
// genuine owner reference, not left in place.
//
// This is the counterpart to the non-Opaque collision case. Refusing to touch a
// marked Opaque forgery (say, on the theory that its owner reference is not
// provably the controller's) would be strictly worse: the forged copy carries the
// tenant-ca visibility label, so it would keep being served to the tenant as a
// vouched trust anchor. Overwriting it destroys no legitimate data — nothing
// legitimate carries this controller-internal marker on a Secret it did not
// create — and it heals the forgery.
func TestReconcile_ForgedProjectionIsHealed(t *testing.T) {
	const attackerCA = "-----BEGIN CERTIFICATE-----\nATTACKERATTACKERATTACKER\n-----END CERTIFICATE-----\n"
	forgedProjection := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			// The marker and the visibility label an attacker would forge — but no
			// owner reference, because they never actually created it here.
			Labels: map[string]string{ManagedLabel: trueValue, TenantCALabel: trueValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(attackerCA)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(),
		appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		forgedProjection,
	).Build()

	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCA) // healed to the genuine CA, not the attacker's
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != testHRUID {
		t.Errorf("the healed projection must carry the genuine release owner reference, got %+v", got.OwnerReferences)
	}
}

// TestReconcile_NonOpaqueSecretAtCanonicalName_IsACollision pins the adoption
// gate against a tenant-forgeable label.
//
// ManagedLabel is an ordinary label, so anyone who can write Secrets in the
// namespace can forge it. Secret.type cannot be forged INTO this controller's
// output, because every projection it creates is Opaque — and type is immutable
// in Kubernetes. So if a non-Opaque Secret bearing a forged ManagedLabel were
// adopted, the update path would try to write type=Opaque and the API server
// would reject it Invalid, forever: a tenant could permanently deny their own
// release its trust anchor with a single Secret. It must be a collision instead.
// TestReconcile_DeclaredSourceIsAnotherReleasesProjection_IsRefused pins the
// direction of the canonical-name overlap that the guards did not cover.
//
// The projection suffix ends in "-ca", which is CloudNativePG's own CA suffix,
// so two releases in one namespace can name the same object from opposite ends:
//
//	app "mydb"        -> projection      "postgres-mydb.tenant-ca"
//	app "mydb-tenant" -> declared source "postgres-mydb.tenant-ca"   (same object)
//
// Both are ordinary names a tenant may pick, and several Postgres per namespace
// is the normal model. The write path already refuses the race where the
// operator's key-bearing CA gets there first — isOurProjection sees a stranger
// and will not overwrite it. This is the other race, and it fails silently in
// the worst possible direction: the sibling's declaration resolves to mydb's
// PROJECTION, lifts mydb's CA out of it, and republishes it as the sibling's own
// vouched trust anchor. A client of the sibling then verifies against a CA that
// signs for a different application, and nothing warns.
//
// A projection is never a CA source. It carries the controller's own marker, so
// it can say so.
// TestProjectionNameCannotCollideWithAnEngineCASecret pins the property the
// canonical name exists to have, and the one its previous two spellings lacked.
//
// "<release>-ca-cert" was rejected because Percona claims it — a cross-ENGINE
// collision. "<release>-tenant-ca" was then chosen because no engine claims that
// SUFFIX, which is a true statement about suffixes and the wrong question: names
// are <prefix><app><suffix>, so a suffix free of engines is not a name free of
// releases. postgres app "foo" projected to postgres-foo-tenant-ca, which is
// precisely CloudNativePG's "<cluster>-ca" for a sibling app named "foo-tenant".
// Same bug, one level up, and both times the reasoning read as thorough.
//
// The dot is not a third guess. Application names are validated as DNS-1035
// LABELS, whose grammar cannot contain a dot; release prefixes and engine CA
// suffixes are dot-free too. So every name an engine can produce is dot-free,
// and a dotted name is unreachable — by character class, for every application
// name that can exist, not for the ones someone thought of.
func TestProjectionNameCannotCollideWithAnEngineCASecret(t *testing.T) {
	// Every CA-Secret suffix the catalog's engines use, per the package doc.
	engineSuffixes := []string{"-ca", "-ca-cert", "-cluster-ca-cert", "-clients-ca-cert", "-ssl", "-tls"}
	// Prefixes as the ApplicationDefinitions declare them, plus the empty one.
	prefixes := []string{"", "postgres-", "kafka-", "clickhouse-", "http-cache-"}
	// Application names, including the adversarial ones: the shapes that made the
	// dash form collide, and every DNS-1035-legal character.
	appNames := []string{
		"foo", "foo-tenant", "foo-tenant-ca", "tenant-ca", "a", "a0-b9",
		"mydb-tenant", "x-tenant-ca-cert", "abcdefghijklmnopqrstuvwxyz0123456789",
	}

	if !strings.HasPrefix(projectionSuffix, ".") {
		t.Fatalf("the canonical suffix must start with a dot; it is what makes the name "+
			"unreachable by any engine. Got %q", projectionSuffix)
	}

	for _, prefix := range prefixes {
		for _, app := range appNames {
			// The guarantee is only as good as the claim about app names, so assert
			// that claim rather than assume it: an app name that could contain a dot
			// would sink the whole argument.
			if errs := validation.IsDNS1035Label(app); len(errs) > 0 {
				t.Fatalf("test fixture %q is not a legal application name: %v", app, errs)
			}
			release := prefix + app
			projection := release + projectionSuffix

			for _, otherApp := range appNames {
				otherRelease := prefix + otherApp
				for _, suffix := range engineSuffixes {
					if engineName := otherRelease + suffix; engineName == projection {
						t.Errorf("projection %q collides with the CA Secret of release %q (%q)",
							projection, otherRelease, engineName)
					}
				}
			}
		}
	}
}

func TestReconcile_DeclaredSourceIsAnotherReleasesProjection_IsRefused(t *testing.T) {
	// Stand in for the neighbour's projection: exactly what this controller
	// writes, sitting at the name this release's declaration renders to.
	neighbour := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testRelease + "-ca",
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCARot)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()), neighbour,
	).Build()

	rec, _, err := reconcileRelease(t, c)
	if err != nil {
		t.Fatalf("a colliding declaration must not error: %v", err)
	}

	if got, ok := getSecret(t, c, testProjection); ok {
		t.Fatalf("a projection must never be republished as another release's trust anchor, "+
			"but %s was created carrying %q", testProjection, got.Data[caCertKey])
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCollision) {
		t.Errorf("expected a %s Warning naming the collision, got %v", reasonCollision, w)
	}
	// The neighbour's projection is untouched — it is not ours to rewrite from here.
	got, ok := getSecret(t, c, testRelease+"-ca")
	if !ok || string(got.Data[caCertKey]) != testCARot {
		t.Errorf("the neighbouring projection must be left exactly as it was")
	}
}

// TestReconcile_Prune_NeverDeletesNonOpaqueSecret is the prune-path twin of
// TestReconcile_NonOpaqueSecretAtCanonicalName_IsACollision, and it exists
// because the two paths disagreed about the same object.
//
// The write path refuses to touch a Secret at the canonical name unless it
// carries the marker AND is Opaque, and is emphatic that the TYPE is the
// load-bearing half: the marker is an ordinary label anyone with namespace
// Secret write can forge, while every projection this controller creates is
// Opaque, so a non-Opaque Secret here did not come from here whatever its labels
// say. Withdrawal checked only the marker — so the exact object the write path
// classifies as a stranger's, and refuses to overwrite, was deleted outright
// when the same release was reached through prune instead.
//
// Reachable on the label leg, which is every engine that declares no source.
// "Never adopt a stranger" and "never delete a stranger" have to be the same
// rule, or the marker becomes a way to make the controller destroy an object it
// has just declared off-limits.
func TestReconcile_Prune_NeverDeletesNonOpaqueSecret(t *testing.T) {
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			// The label claims the controller made this. The type proves it did not.
			Labels: map[string]string{ManagedLabel: trueValue},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: []byte(testCA), corev1.TLSPrivateKeyKey: []byte(testKey)},
	}
	// No declaration and no labelled source: the release publishes nothing, so
	// the prune path runs.
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(nil), forged,
	).Build()

	mustReconcile(t, c)

	got, ok := getSecret(t, c, testProjection)
	if !ok {
		t.Fatalf("a non-Opaque Secret the controller did not create must never be deleted, "+
			"but %s was withdrawn — the write path refuses to even overwrite it", testProjection)
	}
	if got.Type != corev1.SecretTypeTLS || len(got.Data) != 2 {
		t.Errorf("the stranger's Secret must be left exactly as it was, got type=%q keys=%v",
			got.Type, keysOf(got.Data))
	}
}

func TestReconcile_NonOpaqueSecretAtCanonicalName_IsACollision(t *testing.T) {
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			// The label claims the controller made this. The type proves it did not.
			Labels: map[string]string{ManagedLabel: trueValue},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: []byte(testCA), corev1.TLSPrivateKeyKey: []byte(testKey)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(
		helmRelease(), appDef(declaredCA()),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		forged,
	).Build()

	rec := mustReconcile(t, c)

	got := mustProjection(t, c)
	if got.Type != corev1.SecretTypeTLS || len(got.Data) != 2 {
		t.Errorf("a non-Opaque Secret must never be adopted or rewritten, got type=%q data=%v",
			got.Type, keysOf(got.Data))
	}
	if w := warnings(t, rec); len(w) != 1 || !strings.Contains(w[0], reasonCollision) {
		t.Errorf("expected a %s Warning event, got %v", reasonCollision, w)
	}
}

// TestReconcile_Prune_ConflictIsRetried pins that a failed withdrawal is not
// abandoned. Reconciles for one release are serialized, so a Conflict on the
// delete means an EXTERNAL writer touched the projection — exactly the case that
// must be re-read and re-decided. And nothing else would ever come back to it:
// the projection carries no publish label, so it is neither cached nor watched,
// and a release with no source requests no requeue. Swallowing the Conflict would
// leave the tenant holding a retired trust anchor indefinitely.
func TestReconcile_Prune_ConflictIsRetried(t *testing.T) {
	managed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(testCA)},
	}
	// No labelled source and no declaration: the release publishes nothing, so the
	// withdrawal runs — and the delete loses a race with an external writer.
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(helmRelease(), appDef(nil), managed).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
				return apierrors.NewConflict(
					schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("precondition failed"))
			},
		}).Build()

	_, _, err := reconcileRelease(t, c)

	if err == nil {
		t.Fatalf("a Conflict on withdrawal must be retried, not swallowed")
	}
	if !apierrors.IsConflict(err) {
		t.Errorf("the conflict must be surfaced so the workqueue backs off, got %v", err)
	}
}

// TestReleasesOfApplicationDefinition pins the wake-up that two no-requeue
// branches depend on. Reconcile returns without a RequeueAfter for both an
// unusable declaration and an unregistered definition, justified by "definitions
// are watched". This map function IS that watch — if it is wrong, both branches
// are terminal and the release is never reconciled again.
func TestReleasesOfApplicationDefinition(t *testing.T) {
	mine := helmRelease()
	other := helmRelease()
	other.Name = "kafka-bar"
	other.Labels[appsv1alpha1.ApplicationKindLabel] = "Kafka"
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(mine, other).Build()

	got := newReconciler(c, record.NewFakeRecorder(4)).
		releasesOfApplicationDefinition(context.TODO(), appDef(declaredCA()))

	if len(got) != 1 {
		t.Fatalf("a definition must wake exactly the releases of its own kind, got %+v", got)
	}
	if got[0].Name != testRelease || got[0].Namespace != testNamespace {
		t.Errorf("expected the Postgres release, got %+v", got[0])
	}
}

// TestSourceCandidate pins the watch predicate. It is deliberately WIDER than
// isSource: a source whose label is flipped to "false" must still be DELIVERED,
// because that event is what triggers the withdrawal of its projection. A
// predicate that filtered on the value would drop the very event the opt-out
// path depends on.
func TestSourceCandidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"opted in", map[string]string{SourceLabel: trueValue}, true},
		{"opted out must still be delivered", map[string]string{SourceLabel: "false"}, true},
		{"unrelated secret", map[string]string{"app.kubernetes.io/name": "postgres"}, false},
		{"no labels", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}}
			if got := sourceCandidate(s); got != tc.want {
				t.Errorf("sourceCandidate(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

// TestSecretCacheByObject pins the manager-wide Secret cache scoping. It keeps
// every unrelated Secret — and so every private key in the cluster — out of the
// controller's cache, but if the selector were wrong the label-driven leg would
// silently find nothing at all. It must be an EXISTS requirement, not
// publish-ca-cert=true, so that a source whose label flips to "false" is still
// delivered rather than vanishing from the cache.
func TestSecretCacheByObject(t *testing.T) {
	sel := SecretCacheByObject().Label
	if sel == nil {
		t.Fatal("the Secret cache must be scoped by a label selector")
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"opted in", map[string]string{SourceLabel: trueValue}, true},
		{"opted out is still cached", map[string]string{SourceLabel: "false"}, true},
		{"unrelated secret is not cached", map[string]string{"app.kubernetes.io/name": "postgres"}, false},
		{"no labels", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sel.Matches(labels.Set(tc.labels)); got != tc.want {
				t.Errorf("cache selector matched %v = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
