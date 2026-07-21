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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	internalv1alpha1 "github.com/cozystack/cozystack/api/internalapi/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const (
	testNamespace = "tenant-foo"
	// testRelease is the Helm release name — the HelmRelease the cozystack API
	// creates for application Postgres/mydb (prefix "postgres-"). The projection
	// is named after it; Flux stamps it on the sentinel as helm.toolkit.fluxcd.io/name.
	testRelease    = "postgres-mydb"
	testProjection = testRelease + projectionSuffix
	testAppKind    = "Postgres"
	testAppName    = "mydb"
	testHRUID      = types.UID("11111111-2222-3333-4444-555555555555")

	// testSentinel is the sentinel's name, and testOperatorCA the source it
	// declares. The chart names both "<release>-ca" — the sentinel is a
	// TenantProjection, the source a Secret, so the shared name is not a
	// collision. CNPG creates the source itself with the CA private key in it.
	testSentinel    = testRelease + "-ca"
	testOperatorCA  = testRelease + "-ca"
	testSentinelUID = types.UID("99999999-8888-7777-6666-555555555555")

	// testKey is the private-key fixture. Unlike the certificates it stays a bare
	// header with a stub body, and deliberately so: the key guard is a match on
	// the PEM header line and runs BEFORE any parse, so a real key would prove
	// nothing this does not — and a test suite that mints real private keys to
	// check that they are refused invites someone to copy the pattern somewhere
	// it matters.
	testKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n"

	caKeyKey = "ca.key"
)

// testCA and testCARot are REAL self-signed certificates, minted once per run.
// The write-path guard parses what it publishes (see certificateChainPEM), so a
// fixture has to be the thing it claims to be. testCARot is a second, distinct
// certificate: rotation and chain tests need a value that is not equal to testCA.
var (
	testCA    = mustCertPEM("cozystack-test-ca")
	testCARot = mustCertPEM("cozystack-test-ca-rotated")
)

// mustCertPEM mints a self-signed CA certificate and returns it PEM-encoded. It
// panics rather than returning an error so the fixtures can stay package vars; a
// failure here is a broken toolchain, not a test outcome.
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
// and pem.Decode skips it as inter-block noise, so only a projection rebuilt from
// the parsed certificates can keep it away from the tenant.
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

// mustPublicKeyDER returns a DER-encoded public key, for the fixture that checks
// a well-formed PEM block of the WRONG type is still refused.
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
	if err := internalv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("internal scheme: %v", err)
	}
	return s
}

// helmRelease builds the HelmRelease backing an application instance. The
// cozystack API stamps the three application.* labels on every HelmRelease it
// creates; they are what makes a HelmRelease recognisable as an application
// release, and what the selectors-digest path resolves the definition through.
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

// appDef builds the ApplicationDefinition for the Postgres kind. Its spec.secrets
// selectors are what the selectors digest is computed from.
func appDef() *cozyv1alpha1.ApplicationDefinition {
	return &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres"},
		Spec: cozyv1alpha1.ApplicationDefinitionSpec{
			Application: cozyv1alpha1.ApplicationDefinitionApplication{
				Kind: testAppKind, Plural: "postgreses", Singular: "postgres",
			},
			Release: cozyv1alpha1.ApplicationDefinitionRelease{Prefix: "postgres-"},
		},
	}
}

// sentinel builds the TenantProjection a chart renders: named "<release>-ca",
// carrying the helm.toolkit.fluxcd.io/name label Flux stamps and NO owner
// reference, declaring one CACert projection of the operator's CA Secret.
func sentinel() *internalv1alpha1.TenantProjection {
	return sentinelWithSource(testOperatorCA, caCertKey)
}

func sentinelWithSource(sourceName, sourceKey string) *internalv1alpha1.TenantProjection {
	return &internalv1alpha1.TenantProjection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSentinel,
			Namespace: testNamespace,
			UID:       testSentinelUID,
			Labels:    map[string]string{helmNameLabel: testRelease},
		},
		Spec: internalv1alpha1.TenantProjectionSpec{
			Projections: []internalv1alpha1.TenantProjectionEntry{{
				Type:             internalv1alpha1.ProjectionTypeCACert,
				SourceSecretName: sourceName,
				SourceKey:        sourceKey,
			}},
		},
	}
}

// operatorSecret builds a Secret an operator created in the release namespace.
func operatorSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func newReconciler(c client.Client, rec record.EventRecorder) *Reconciler {
	// The fake client has no cache, so the cached Client and the uncached Reader
	// coincide — production splits them only so the metadata-only Secret watch
	// holds no key material.
	return &Reconciler{Client: c, Reader: c, Recorder: rec}
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&internalv1alpha1.TenantProjection{}).
		WithObjects(objs...).
		Build()
}

// reconcileSentinel runs one reconcile of the sentinel.
func reconcileSentinel(t *testing.T, c client.Client) (*record.FakeRecorder, ctrl.Result, error) {
	t.Helper()
	rec := record.NewFakeRecorder(16)
	res, err := newReconciler(c, rec).Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testSentinel},
	})
	return rec, res, err
}

func mustReconcile(t *testing.T, c client.Client) *record.FakeRecorder {
	t.Helper()
	rec, _, err := reconcileSentinel(t, c)
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

func mustProjection(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	got, ok := getSecret(t, c, testProjection)
	if !ok {
		t.Fatalf("expected projection %s", testProjection)
	}
	return got
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

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

// assertOwnedBySentinel pins the load-bearing ownership: the projection is owned
// by the TenantProjection sentinel alone, which is what carries the lineage walk
// (sentinel -> helm label -> HelmRelease -> app) and the owner-reference GC.
func assertOwnedBySentinel(t *testing.T, proj *corev1.Secret) {
	t.Helper()
	if len(proj.OwnerReferences) != 1 {
		t.Fatalf("projection must be owned by the sentinel alone, got %+v", proj.OwnerReferences)
	}
	o := proj.OwnerReferences[0]
	if o.UID != testSentinelUID || o.Kind != "TenantProjection" || o.Name != testSentinel {
		t.Errorf("projection owner = %+v, want the sentinel %s/%s", o, testSentinel, testSentinelUID)
	}
	if !ptr.Deref(o.Controller, false) {
		t.Errorf("the sentinel must be the controlling owner, got %+v", o)
	}
}

func readyCond(t *testing.T, c client.Client) metav1.Condition {
	t.Helper()
	tp := &internalv1alpha1.TenantProjection{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: testNamespace, Name: testSentinel}, tp); err != nil {
		t.Fatalf("get sentinel: %v", err)
	}
	cond := meta.FindStatusCondition(tp.Status.Conditions, conditionReady)
	if cond == nil {
		t.Fatalf("sentinel carries no Ready condition; status = %+v", tp.Status)
	}
	return *cond
}

func assertReady(t *testing.T, c client.Client, status metav1.ConditionStatus, reason string) {
	t.Helper()
	cond := readyCond(t, c)
	if cond.Status != status || cond.Reason != reason {
		t.Errorf("Ready = {status:%s reason:%s}, want {status:%s reason:%s}",
			cond.Status, cond.Reason, status, reason)
	}
}

// --- Reconcile: the sentinel-driven happy path and its status ---

// TestReconcile_ProjectsKeyFreeCA is the core case: CNPG creates <release>-ca
// with the CA private key in it, the sentinel declares it, and the projection
// must lift ca.crt and nothing else, owned by the sentinel.
func TestReconcile_ProjectsKeyFreeCA(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCA)
	assertOwnedBySentinel(t, got)
	if got.Labels[TenantCALabel] != trueValue {
		t.Errorf("projection must carry the tenant-ca visibility label, got %v", got.Labels)
	}
	assertReady(t, c, metav1.ConditionTrue, reasonProjected)
}

// TestReconcile_SourceNotFound_StatusIsReadable is the whole point of the
// sentinel model: a source the operator has not minted yet is a visible
// Ready=False/SourceNotFound, not a silent requeue.
func TestReconcile_SourceNotFound_StatusIsReadable(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel())
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("no projection may be published while the source is absent")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonSourceNotFound)
}

// TestReconcile_SourceNotReady_EmptyValue pins the state between an operator
// creating its CA Secret and populating it: the source exists but carries no
// certificate yet.
func TestReconcile_SourceNotReady_EmptyValue(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte("")}),
	)
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("no projection may be published before the source carries its certificate")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonSourceNotReady)
}

// TestReconcile_EmptySourceSecretName_ReportsCondition pins that an entry with an
// empty sourceSecretName resolves to a Ready=False condition, not a returned error.
// The CRD now forbids the empty string (MinLength=1), but an object admitted by an
// older CRD can still carry one.
//
// The threat is what the apiserver does with a Get for an empty name: it returns
// "resource name may not be empty", which is NOT NotFound, so without a guard it
// escapes the not-found branch and is returned from Reconcile — exponential backoff
// forever, and because the error path returns before any status write, no Ready
// condition is ever recorded. That is the silent retry the sentinel exists to
// eliminate. The bare fake client masks it (an empty-name Get returns NotFound), so
// the test installs a Get interceptor that reproduces the apiserver's BadRequest,
// making the pre-guard reconcile wedge exactly as production would.
func TestReconcile_EmptySourceSecretName_ReportsCondition(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&internalv1alpha1.TenantProjection{}).
		WithObjects(helmRelease(), appDef(), sentinelWithSource("", caCertKey)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == "" {
					// What the real apiserver returns, and what makes the bug bite:
					// not NotFound, so the not-found branch does not catch it.
					return apierrors.NewBadRequest("resource name may not be empty")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	rec := record.NewFakeRecorder(16)
	_, err := newReconciler(c, rec).Reconcile(context.TODO(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testSentinel},
	})
	if err != nil {
		t.Fatalf("an empty sourceSecretName must resolve to a condition, not an error: %v", err)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("no projection may be published for an empty sourceSecretName")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonSourceInvalid)
}

// TestReconcile_NoReleaseLabel pins that a sentinel Flux did not render — one
// carrying no helm.toolkit.fluxcd.io/name — cannot derive a canonical name and
// reports it rather than guessing.
func TestReconcile_NoReleaseLabel(t *testing.T) {
	s := sentinel()
	delete(s.Labels, helmNameLabel)
	c := newClient(t, s,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA)}),
	)
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a sentinel with no release label must not project anything")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonNoRelease)
}

func TestReconcile_MissingSentinelIsNoOp(t *testing.T) {
	c := newClient(t) // no sentinel at all
	if _, _, err := reconcileSentinel(t, c); err != nil {
		t.Fatalf("a missing sentinel must be a no-op, got %v", err)
	}
	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("nothing may be created for a sentinel that does not exist")
	}
}

func TestReconcile_DeletingSentinelIsNoOp(t *testing.T) {
	s := sentinel()
	s.Finalizers = []string{"cozystack.io/test-hold"} // lets the fake client retain a deleting object
	c := newClient(t, helmRelease(), appDef(), s,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA)}),
	)
	if err := c.Delete(context.TODO(), s); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a sentinel under deletion must not project — the GC is collecting it")
	}
}

func TestReconcile_IsIdempotent(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)
	before := mustProjection(t, c)

	mustReconcile(t, c)
	after := mustProjection(t, c)
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("an unchanged reconcile must not rewrite the projection (%s -> %s)",
			before.ResourceVersion, after.ResourceVersion)
	}
}

// TestReconcile_MultipleCACertEntries_Rejected exercises the threat directly:
// two CACert entries both resolve to the single canonical name. Without the
// guard, entry 0 projects its CA and entry 1 overwrites it — a projection is
// published (the last entry's CA) with Ready=True, and every reconcile churns
// the write. The sentinel must publish nothing and say why instead.
func TestReconcile_MultipleCACertEntries_Rejected(t *testing.T) {
	s := sentinel()
	s.Spec.Projections = append(s.Spec.Projections, internalv1alpha1.TenantProjectionEntry{
		Type:             internalv1alpha1.ProjectionTypeCACert,
		SourceSecretName: "second-src",
		SourceKey:        caCertKey,
	})
	c := newClient(t, helmRelease(), appDef(), s,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA)}),
		operatorSecret("second-src", map[string][]byte{caCertKey: []byte(testCARot)}),
	)
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a sentinel declaring more than one CACert projection must publish nothing")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonMultipleCACert)
}

// TestReconcile_EntryRemoved_WithdrawsProjection exercises the withdrawal path: a
// sentinel that stops declaring a CACert projection must have the anchor it
// published removed. Garbage collection only fires when the whole sentinel is
// pruned, so without an explicit delete a sentinel that keeps existing with its
// CACert entry gated off (e.g. on tls.enabled) would serve a stale trust anchor
// forever.
func TestReconcile_EntryRemoved_WithdrawsProjection(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)
	if _, ok := getSecret(t, c, testProjection); !ok {
		t.Fatalf("the projection must exist while the CACert entry is declared")
	}

	// Drop the CACert entry through the internal type. The CRD's MinItems=1 forbids
	// an empty projections list at the API server, but a chart that gates the entry
	// — or a future non-CACert entry replacing it — leaves the sentinel with zero
	// CACert projections while the old anchor still stands.
	tp := &internalv1alpha1.TenantProjection{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: testNamespace, Name: testSentinel}, tp); err != nil {
		t.Fatalf("get sentinel: %v", err)
	}
	tp.Spec.Projections = nil
	if err := c.Update(context.TODO(), tp); err != nil {
		t.Fatalf("drop the CACert entry: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("withdrawing the last CACert entry must delete the projection it published")
	}
}

// TestReconcile_EntryRemoved_WithdrawsStaleUIDProjection pins that withdrawal
// recognises a projection this sentinel owns by NAME, not by an exact owner-
// reference match. Delete-and-recreate an application under the same name leaves a
// projection carrying this sentinel's name in its owner reference but the PREVIOUS
// incarnation's UID; if the live sentinel then stops declaring the anchor, an exact
// UID match would read the retired projection as "not ours" and walk away, leaving
// it tenant-readable forever — owner-reference GC only fires when the sentinel
// itself is pruned. Adoption already re-homes such a stale-UID projection, so
// withdrawal must recognise the same object.
func TestReconcile_EntryRemoved_WithdrawsStaleUIDProjection(t *testing.T) {
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: internalv1alpha1.GroupVersion.String(),
				Kind:       "TenantProjection",
				Name:       testSentinel,
				UID:        types.UID("dead0000-0000-0000-0000-000000000000"), // previous incarnation
				Controller: new(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte("STALE")},
	}
	// The live sentinel no longer declares a CACert projection (a future non-CACert
	// entry replaced it), so the reconcile takes the withdrawal path.
	s := sentinel()
	s.Spec.Projections = nil
	c := newClient(t, helmRelease(), appDef(), s, stale)
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a retired anchor owned by a stale sentinel UID must be withdrawn, not left tenant-readable forever")
	}
}

// TestReconcile_EntryRemoved_WithdrawsDespiteBlockOwnerDeletionDrift pins that a
// drift of the owner reference's BlockOwnerDeletion flag — the exact drift the
// adoption path normalizes — does not defeat withdrawal. An exact owner-reference
// match reads the drifted projection as "not ours" and no-ops; matching by name
// withdraws it.
func TestReconcile_EntryRemoved_WithdrawsDespiteBlockOwnerDeletionDrift(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)

	proj := mustProjection(t, c)
	proj.OwnerReferences[0].BlockOwnerDeletion = new(true)
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("set BlockOwnerDeletion drift: %v", err)
	}

	tp := &internalv1alpha1.TenantProjection{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: testNamespace, Name: testSentinel}, tp); err != nil {
		t.Fatalf("get sentinel: %v", err)
	}
	tp.Spec.Projections = nil
	if err := c.Update(context.TODO(), tp); err != nil {
		t.Fatalf("drop the CACert entry: %v", err)
	}
	mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("withdrawal must not be blocked by a BlockOwnerDeletion flag drift on the owner reference")
	}
}

// TestReconcile_TwoSentinelsForOneRelease_Refused pins the two-sentinel guard: two
// sentinels in one namespace carrying the same release label both resolve to the
// single canonical name. Without the guard, whichever reconciles first publishes an
// arbitrary CA (a silent winner) and the two flap ownership of one projection. Both
// must refuse instead, so nothing is published until the declaration lives on
// exactly one sentinel.
func TestReconcile_TwoSentinelsForOneRelease_Refused(t *testing.T) {
	second := &internalv1alpha1.TenantProjection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSentinel + "-dup",
			Namespace: testNamespace,
			UID:       types.UID("77777777-8888-9999-aaaa-bbbbbbbbbbbb"),
			Labels:    map[string]string{helmNameLabel: testRelease},
		},
		Spec: internalv1alpha1.TenantProjectionSpec{
			Projections: []internalv1alpha1.TenantProjectionEntry{{
				Type:             internalv1alpha1.ProjectionTypeCACert,
				SourceSecretName: testOperatorCA,
				SourceKey:        caCertKey,
			}},
		},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(), second,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	_, res, err := reconcileSentinel(t, c)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("two sentinels contesting one release must publish nothing, not an arbitrary winner")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonReleaseContested)
	// The contest is cleared by deleting the sibling, which nothing else enqueues
	// this sentinel for, so it must requeue itself or the anchor stays withheld
	// until the far-off global resync.
	if res.RequeueAfter == 0 {
		t.Errorf("a contested sentinel must requeue itself; nothing else wakes it when the sibling is removed")
	}
}

// TestReconcile_ContestResolved_Republishes pins the recovery half of the
// two-sentinel guard: once the operator removes the duplicate, the surviving
// sentinel must publish its anchor. Nothing enqueues this sentinel when a sibling
// is deleted, so the recovery rides the RequeueAfter the contested reconcile
// returns — without it the anchor stays withheld until the global resync.
func TestReconcile_ContestResolved_Republishes(t *testing.T) {
	second := &internalv1alpha1.TenantProjection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSentinel + "-dup",
			Namespace: testNamespace,
			UID:       types.UID("77777777-8888-9999-aaaa-bbbbbbbbbbbb"),
			Labels:    map[string]string{helmNameLabel: testRelease},
		},
		Spec: internalv1alpha1.TenantProjectionSpec{
			Projections: []internalv1alpha1.TenantProjectionEntry{{
				Type:             internalv1alpha1.ProjectionTypeCACert,
				SourceSecretName: testOperatorCA,
				SourceKey:        caCertKey,
			}},
		},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(), second,
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	_, res, err := reconcileSentinel(t, c)
	if err != nil {
		t.Fatalf("reconcile while contested: %v", err)
	}
	assertReady(t, c, metav1.ConditionFalse, reasonReleaseContested)
	if res.RequeueAfter == 0 {
		t.Fatalf("the contested reconcile must schedule its own recovery requeue")
	}

	// The operator removes the duplicate. The scheduled requeue is the only thing
	// that brings this sentinel back — no sibling-delete watch exists.
	if err := c.Delete(context.TODO(), second); err != nil {
		t.Fatalf("remove the duplicate sentinel: %v", err)
	}
	mustReconcile(t, c)

	assertKeyFree(t, mustProjection(t, c), testCA)
	assertReady(t, c, metav1.ConditionTrue, reasonProjected)
}

// --- Reconcile: the write-path guards, exercised through a real source ---

// TestReconcile_RefusesPrivateKeyUnderLiftedKey pins the reason the controller
// exists: a source value that carries key material is refused, with an event and
// a Ready=False status, never published.
func TestReconcile_RefusesPrivateKeyUnderLiftedKey(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA + testKey)}),
	)
	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a source carrying private key material must never be published")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonSourceRejected)
	if w := warnings(t, rec); len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), reasonPrivateKeyRefused) {
		t.Errorf("a private-key refusal must surface a %s Warning event, got %v", reasonPrivateKeyRefused, w)
	}
}

func TestReconcile_RefusesNonCertificate(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte("not a certificate")}),
	)
	rec := mustReconcile(t, c)

	if _, ok := getSecret(t, c, testProjection); ok {
		t.Errorf("a source that is not a certificate must never be published")
	}
	assertReady(t, c, metav1.ConditionFalse, reasonSourceRejected)
	if w := warnings(t, rec); len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), reasonInvalidCACert) {
		t.Errorf("a non-certificate refusal must surface a %s Warning event, got %v", reasonInvalidCACert, w)
	}
}

// TestReconcile_HonoursSourceKey pins that a sentinel naming a non-default key
// lifts from that key and still republishes under the canonical ca.crt.
func TestReconcile_HonoursSourceKey(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(),
		sentinelWithSource(testOperatorCA, "tls.crt"),
		operatorSecret(testOperatorCA, map[string][]byte{"tls.crt": []byte(testCA), "tls.key": []byte(testKey)}),
	)
	mustReconcile(t, c)
	assertKeyFree(t, mustProjection(t, c), testCA)
}

// --- Reconcile: ownership and collisions ---

// TestReconcile_ExtraOwnerReferenceIsStripped pins that a projection is owned by
// the sentinel ALONE — an extra owner appended by a namespace actor would keep
// the trust anchor alive past the sentinel's deletion, so it is normalized away.
func TestReconcile_ExtraOwnerReferenceIsStripped(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)

	proj := mustProjection(t, c)
	proj.OwnerReferences = append(proj.OwnerReferences, metav1.OwnerReference{
		APIVersion: "v1", Kind: "ConfigMap", Name: "squatter",
		UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
	})
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("append a second owner: %v", err)
	}

	mustReconcile(t, c)
	assertOwnedBySentinel(t, mustProjection(t, c))
}

// TestReconcile_BlockOwnerDeletionDriftIsNormalized pins that a drift of the sole
// owner reference's BlockOwnerDeletion flag to true is re-homed to false — the
// trust anchor must never hold up teardown of the application it belongs to.
func TestReconcile_BlockOwnerDeletionDriftIsNormalized(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)

	proj := mustProjection(t, c)
	proj.OwnerReferences[0].BlockOwnerDeletion = ptr.To(true)
	if err := c.Update(context.TODO(), proj); err != nil {
		t.Fatalf("set BlockOwnerDeletion drift: %v", err)
	}

	mustReconcile(t, c)
	got := mustProjection(t, c)
	if ptr.Deref(got.OwnerReferences[0].BlockOwnerDeletion, false) {
		t.Errorf("BlockOwnerDeletion drift must be normalized back to false, got %+v", got.OwnerReferences[0])
	}
}

// TestReconcile_RecreatedRelease_RehomesProjection pins that a projection left
// owned by a stale sentinel UID — what delete-and-recreate under the same name
// leaves behind — is re-homed onto the live sentinel.
func TestReconcile_RecreatedRelease_RehomesProjection(t *testing.T) {
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: internalv1alpha1.GroupVersion.String(),
				Kind:       "TenantProjection",
				Name:       testSentinel,
				UID:        types.UID("dead0000-0000-0000-0000-000000000000"), // previous incarnation
				Controller: ptr.To(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte("STALE")},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		stale,
	)
	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertOwnedBySentinel(t, got)
	assertKeyFree(t, got, testCA)
}

// TestReconcile_ForeignSecretIsNeverOverwritten pins that a Secret at the
// canonical name the controller did NOT create — which may hold live key
// material — is left untouched, surfaced as a collision.
func TestReconcile_ForeignSecretIsNeverOverwritten(t *testing.T) {
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{caCertKey: []byte(testCARot), caKeyKey: []byte(testKey)},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		foreign,
	)
	rec := mustReconcile(t, c)

	got := mustProjection(t, c)
	if string(got.Data[caCertKey]) != testCARot || string(got.Data[caKeyKey]) != testKey {
		t.Errorf("a foreign Secret must be left exactly as it was, got %v", keysOf(got.Data))
	}
	assertReady(t, c, metav1.ConditionFalse, reasonProjectionCollision)
	if w := warnings(t, rec); len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), reasonCollision) {
		t.Errorf("a collision must surface a %s Warning event, got %v", reasonCollision, w)
	}
}

// TestReconcile_NonOpaqueAtCanonicalNameIsCollision pins the adoption gate
// against a tenant-forgeable label: a non-Opaque Secret carrying a forged marker
// is a collision, not adopted — adopting it would make every Update fail Invalid
// (Secret.type is immutable), letting a tenant permanently deny their own anchor.
func TestReconcile_NonOpaqueAtCanonicalNameIsCollision(t *testing.T) {
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{ManagedLabel: trueValue, TenantCALabel: trueValue},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte(testCARot), "tls.key": []byte(testKey)},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		forged,
	)
	mustReconcile(t, c)

	got := mustProjection(t, c)
	if got.Type != corev1.SecretTypeTLS {
		t.Errorf("a non-Opaque Secret must be treated as a collision and left untouched, got type %q", got.Type)
	}
	assertReady(t, c, metav1.ConditionFalse, reasonProjectionCollision)
}

// TestReconcile_MarkerStrippedFromOwnedProjectionIsHealed exercises the finding
// the owner-reference adoption gate exists to close. An actor with Secret write in
// the namespace STRIPS the controller's marker off the GENUINE projection — owner
// reference and tenantresource verdict intact — and writes their own bytes under
// ca.crt. Keying adoption on the strippable marker would make the controller
// disown its own object, refuse it as a collision, and serve the forged anchor
// forever. Keying on the owner reference heals it: the genuine CA is restored, the
// marker is put back, and the sentinel is Ready.
func TestReconcile_MarkerStrippedFromOwnedProjectionIsHealed(t *testing.T) {
	const attackerCA = "-----BEGIN CERTIFICATE-----\nATTACKERATTACKERATTACKER\n-----END CERTIFICATE-----\n"
	tampered := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			// The marker is STRIPPED; the owner reference and the webhook's
			// tenantresource verdict are left exactly as the genuine projection
			// carried them.
			Labels: map[string]string{
				TenantCALabel:                       trueValue,
				corev1alpha1.TenantResourceLabelKey: trueValue,
			},
			OwnerReferences: []metav1.OwnerReference{sentinelOwnerRef(sentinel())},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(attackerCA)},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		tampered,
	)
	mustReconcile(t, c)

	got := mustProjection(t, c)
	assertKeyFree(t, got, testCA) // healed to the genuine CA, not the attacker's
	assertOwnedBySentinel(t, got)
	if got.Labels[ManagedLabel] != trueValue {
		t.Errorf("healing must restore the %q marker, labels=%v", ManagedLabel, got.Labels)
	}
	assertReady(t, c, metav1.ConditionTrue, reasonProjected)
}

// TestReconcile_ForgedMarkerWithoutOwnerRefIsCollision pins the other side of the
// owner-reference gate: a Secret at the canonical name that carries the marker and
// the tenant-ca label but NO owner reference back to this sentinel is a stranger,
// not one of ours. It is refused as a collision and left untouched — never
// overwritten, because it may hold data this controller does not own, and it
// carries no owner chain to an application so no tenant can read it anyway.
func TestReconcile_ForgedMarkerWithoutOwnerRefIsCollision(t *testing.T) {
	const attackerCA = "-----BEGIN CERTIFICATE-----\nATTACKERATTACKERATTACKER\n-----END CERTIFICATE-----\n"
	forged := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProjection,
			Namespace: testNamespace,
			Labels:    map[string]string{ManagedLabel: trueValue, TenantCALabel: trueValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte(attackerCA)},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		forged,
	)
	mustReconcile(t, c)

	got := mustProjection(t, c)
	if string(got.Data[caCertKey]) != attackerCA {
		t.Errorf("a Secret with no owner reference to the sentinel must be left untouched, got %q", got.Data[caCertKey])
	}
	assertReady(t, c, metav1.ConditionFalse, reasonProjectionCollision)
}

// TestReconcile_SourceAtCanonicalName_KeyBearing pins the degenerate case where
// the declared source sits at the canonical name and carries key material: no
// projection is written over live key material, and it is surfaced loudly.
func TestReconcile_SourceAtCanonicalName_KeyBearing(t *testing.T) {
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)},
	}
	c := newClient(t, helmRelease(), appDef(),
		sentinelWithSource(testProjection, caCertKey), src,
	)
	rec := mustReconcile(t, c)

	assertReady(t, c, metav1.ConditionFalse, reasonCanonicalOccupied)
	if w := warnings(t, rec); len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), reasonCanonicalNameOccupied) {
		t.Errorf("a key-bearing Secret at the canonical name must warn %s, got %v", reasonCanonicalNameOccupied, w)
	}
}

// TestReconcile_SourceAtCanonicalName_KeyFree pins the benign case: a key-free
// Secret already at the canonical name IS the tenant's trust anchor, so the
// controller leaves it and reports Ready.
func TestReconcile_SourceAtCanonicalName_KeyFree(t *testing.T) {
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testProjection, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{caCertKey: []byte(testCA)},
	}
	c := newClient(t, helmRelease(), appDef(),
		sentinelWithSource(testProjection, caCertKey), src,
	)
	mustReconcile(t, c)
	assertReady(t, c, metav1.ConditionTrue, reasonProjected)
}

// TestReconcile_TerminatingProjection_WaitsForCollection pins that the reconciler
// does not write to a projection the garbage collector is already removing.
func TestReconcile_TerminatingProjection_WaitsForCollection(t *testing.T) {
	terminating := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testProjection,
			Namespace:       testNamespace,
			Labels:          map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
			OwnerReferences: []metav1.OwnerReference{sentinelOwnerRef(sentinel())},
			Finalizers:      []string{"cozystack.io/test-hold"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{caCertKey: []byte("STALE")},
	}
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
		terminating,
	)
	if err := c.Delete(context.TODO(), terminating); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}

	_, res, err := reconcileSentinel(t, c)
	if err != nil {
		t.Fatalf("a terminating projection must not error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("a terminating projection must be retried once collected, got %+v", res)
	}
	got := mustProjection(t, c)
	if string(got.Data[caCertKey]) != "STALE" {
		t.Errorf("nothing may be written to a terminating projection, got %q", got.Data[caCertKey])
	}
	assertReady(t, c, metav1.ConditionFalse, reasonProjectionTerminating)
}

// --- Reconcile: the selectors-digest re-admission ---

// TestReconcile_SelectorChange_ReleasesTheProjectionToTheWebhook pins that a
// change to spec.secrets hands the projection back to the lineage webhook by
// dropping the managed-by marker, so the tenantresource verdict is recomputed.
func TestReconcile_SelectorChange_ReleasesTheProjectionToTheWebhook(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)

	// Stand in for the admission the real API server would have run on CREATE.
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

	got := mustProjection(t, c)
	if _, found := got.Labels[managedByCozystackLabel]; found {
		t.Errorf("a selector change must drop %q to re-admit the projection, labels=%v",
			managedByCozystackLabel, got.Labels)
	}
}

// TestReconcile_UnrelatedDefinitionChange_DoesNotRewrite pins that the digest is
// narrower than the definition's resourceVersion: an edit that cannot change
// tenant visibility must not churn the projection.
func TestReconcile_UnrelatedDefinitionChange_DoesNotRewrite(t *testing.T) {
	c := newClient(t, helmRelease(), appDef(), sentinel(),
		operatorSecret(testOperatorCA, map[string][]byte{caCertKey: []byte(testCA), caKeyKey: []byte(testKey)}),
	)
	mustReconcile(t, c)
	before := mustProjection(t, c)

	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: "postgres"}, def); err != nil {
		t.Fatalf("get definition: %v", err)
	}
	def.Spec.Application.Plural = "postgresqls" // does not touch spec.secrets
	if err := c.Update(context.TODO(), def); err != nil {
		t.Fatalf("update definition: %v", err)
	}
	mustReconcile(t, c)

	if got := mustProjection(t, c); got.ResourceVersion != before.ResourceVersion {
		t.Errorf("an edit that cannot affect tenant visibility must not rewrite the projection")
	}
}

// TestApplicationDefinitionPredicate_GatesOnSecrets pins the no-ENQUEUE half of the
// same property TestReconcile_UnrelatedDefinitionChange_DoesNotRewrite pins for the
// no-WRITE half: an edit that cannot move the selectors digest never reaches the
// mapping, so Flux's periodic no-op re-apply of every *-rd definition does not fan
// out to every sentinel in the cluster. Creates and deletes still fall through.
func TestApplicationDefinitionPredicate_GatesOnSecrets(t *testing.T) {
	base := appDef()
	for _, tc := range []struct {
		name   string
		mutate func(*cozyv1alpha1.ApplicationDefinition)
		want   bool
	}{
		{
			name:   "unrelated field change is not delivered",
			mutate: func(d *cozyv1alpha1.ApplicationDefinition) { d.Spec.Application.Plural = "postgresqls" },
			want:   false,
		},
		{
			name: "spec.secrets change is delivered",
			mutate: func(d *cozyv1alpha1.ApplicationDefinition) {
				d.Spec.Secrets.Include = []*cozyv1alpha1.ApplicationDefinitionResourceSelector{{
					LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{TenantCALabel: trueValue}},
				}}
			},
			want: true,
		},
		{
			name:   "identical definition is not delivered",
			mutate: func(*cozyv1alpha1.ApplicationDefinition) {},
			want:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldDef := base.DeepCopy()
			newDef := base.DeepCopy()
			tc.mutate(newDef)
			got := applicationDefinitionPredicate.Update(event.UpdateEvent{ObjectOld: oldDef, ObjectNew: newDef})
			if got != tc.want {
				t.Errorf("predicate delivered=%v, want %v", got, tc.want)
			}
		})
	}

	// A definition appearing or disappearing can change a verdict, so creates and
	// deletes must always fall through.
	if !applicationDefinitionPredicate.Create(event.CreateEvent{Object: base}) {
		t.Errorf("a definition create must be delivered")
	}
	if !applicationDefinitionPredicate.Delete(event.DeleteEvent{Object: base}) {
		t.Errorf("a definition delete must be delivered")
	}
}

// TestProjectionsForSourceSecret pins the watch mapping: a source Secret event
// resolves to exactly the sentinels that reference it, in its own namespace, and
// nothing else. A bug here (wrong namespace scope, wrong index field) presents as
// the same "projection never appears" symptom as a dead watch, so it is pinned
// here rather than left to the chainsaw e2e alone. It registers the real index
// function on the fake client so the field-selector query it depends on is
// exercised, not stubbed.
func TestProjectionsForSourceSecret(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithIndex(&internalv1alpha1.TenantProjection{}, sourceSecretNameField, indexBySourceSecretName).
		WithObjects(sentinel()).
		Build()
	r := newReconciler(c, record.NewFakeRecorder(1))

	meta := func(ns, name string) *metav1.PartialObjectMetadata {
		m := &metav1.PartialObjectMetadata{}
		m.SetGroupVersionKind(secretGVK)
		m.SetNamespace(ns)
		m.SetName(name)
		return m
	}

	// The sentinel references testOperatorCA in testNamespace.
	got := r.projectionsForSourceSecret(context.TODO(), meta(testNamespace, testOperatorCA))
	if len(got) != 1 || got[0].Name != testSentinel || got[0].Namespace != testNamespace {
		t.Errorf("a referenced source must map to its sentinel, got %+v", got)
	}

	// A Secret no sentinel references maps to nothing.
	if got := r.projectionsForSourceSecret(context.TODO(), meta(testNamespace, "unreferenced")); len(got) != 0 {
		t.Errorf("an unreferenced Secret must map to no sentinel, got %+v", got)
	}

	// A same-named Secret in another namespace maps to nothing: the mapping is
	// namespace-scoped, so it never crosses a tenant boundary.
	if got := r.projectionsForSourceSecret(context.TODO(), meta("tenant-other", testOperatorCA)); len(got) != 0 {
		t.Errorf("a source in another namespace must not map to this sentinel, got %+v", got)
	}
}

// TestProjectionsForOwnedProjection pins the second watch mapping: an in-place
// tamper of the canonical "<release>.tenant-ca" Secret resolves to the sentinel
// that owns it, so the owner-reference adoption gate heals it at once rather than
// on the five-minute resync. The projection's own name matches no sentinel's
// sourceSecretName, so the source-index mapping returns zero for it — which is
// exactly why this second, owner-reference mapping is needed.
func TestProjectionsForOwnedProjection(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithIndex(&internalv1alpha1.TenantProjection{}, sourceSecretNameField, indexBySourceSecretName).
		WithObjects(sentinel()).
		Build()
	r := newReconciler(c, record.NewFakeRecorder(1))

	// The projection Secret, as the controller writes it: named "<release>.tenant-ca"
	// and owner-referenced to the sentinel.
	projMeta := &metav1.PartialObjectMetadata{}
	projMeta.SetGroupVersionKind(secretGVK)
	projMeta.SetNamespace(testNamespace)
	projMeta.SetName(testProjection)
	projMeta.SetOwnerReferences([]metav1.OwnerReference{sentinelOwnerRef(sentinel())})

	// The owner-reference mapping resolves the owning sentinel from the projection.
	got := r.projectionsForOwnedProjection(context.TODO(), projMeta)
	if len(got) != 1 || got[0].Name != testSentinel || got[0].Namespace != testNamespace {
		t.Errorf("a projection must map to its owning sentinel, got %+v", got)
	}

	// The source-index mapping, by contrast, returns nothing for the projection's
	// own name: it is no sentinel's sourceSecretName. This is the gap the
	// owner-reference mapping fills.
	if got := r.projectionsForSourceSecret(context.TODO(), projMeta); len(got) != 0 {
		t.Errorf("the projection name matches no source, so the source mapping must return nothing, got %+v", got)
	}

	// A Secret with no TenantProjection owner reference maps to no sentinel.
	plain := &metav1.PartialObjectMetadata{}
	plain.SetGroupVersionKind(secretGVK)
	plain.SetNamespace(testNamespace)
	plain.SetName(testProjection)
	if got := r.projectionsForOwnedProjection(context.TODO(), plain); len(got) != 0 {
		t.Errorf("a Secret owned by no sentinel must map to nothing, got %+v", got)
	}
}

// --- Pure write-path guards (unchanged functions) ---

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
		// dashes never sees it. It is a private key by any reading.
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

// TestProjectionData pins the single write-path sanitizer: it emits exactly one
// key and refuses anything that is not a bare certificate.
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
// actual claim, as opposed to the one a header match can make: every case below
// wears correct certificate armour and no key header, yet is not a certificate.
func TestProjectionData_RefusesCertificateArmouredNonCertificate(t *testing.T) {
	for name, value := range map[string]string{
		"armour around truncated DER": "-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIQ\n-----END CERTIFICATE-----\n",
		"armour around non-base64":    "-----BEGIN CERTIFICATE-----\nROTATEDROTATEDROTATED\n-----END CERTIFICATE-----\n",
		"armour around plain text": "-----BEGIN CERTIFICATE-----\n" +
			base64.StdEncoding.EncodeToString([]byte("this is not a certificate, it is a sentence")) +
			"\n-----END CERTIFICATE-----\n",
		"header with no body":  "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n",
		"header with no close": "-----BEGIN CERTIFICATE-----\nMIIBkTCCATegAwIBAgIQ\n",
		"empty":                "",
		"whitespace only":      "   \n\t\n",
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
// on the values it exists to publish: a single CA, and a multi-certificate chain.
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
// EVERY byte it publishes, not just the first block it can find.
func TestProjectionData_RefusesTrailingGarbage(t *testing.T) {
	if _, err := projectionData([]byte(testCA + "and then some trailing bytes")); !errors.Is(err, errNotCertificate) {
		t.Errorf("projectionData(cert+garbage) error = %v, want errNotCertificate", err)
	}
}

// TestProjectionData_KeyGuardWinsOverTheParse pins the ORDER of the two checks: a
// private key wrapped in certificate armour is refused as key material, not as a
// parse failure, so the operator is told which mistake they made.
func TestProjectionData_KeyGuardWinsOverTheParse(t *testing.T) {
	if _, err := projectionData([]byte(testCA + testKey)); !errors.Is(err, errPrivateKey) {
		t.Errorf("projectionData(cert+key) error = %v, want errPrivateKey", err)
	}
}

// TestProjectionData_StripsKeyMaterialSmuggledAroundBlocks is the write path's
// deepest guarantee: the projection is REBUILT from the certificate blocks it
// validated, never copied from the input. A raw DER or JWK key wears no PEM
// private-key header, so it walks past containsPrivateKey and pem.Decode skips it
// — only re-encoding the parsed certificates makes it structurally unreachable.
func TestProjectionData_StripsKeyMaterialSmuggledAroundBlocks(t *testing.T) {
	derKey := mustPrivateKeyDER()
	jwkKey := `{"kty":"EC","crv":"P-256","d":"c2VjcmV0LXByaXZhdGUta2V5LW1hdGVyaWFs"}`

	concat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"leading DER key", concat(derKey, []byte("\n"), []byte(testCA)), testCA},
		{"leading JWK key", []byte(jwkKey + "\n" + testCA), testCA},
		{"interstitial DER key", concat([]byte(testCA), derKey, []byte("\n"), []byte(testCARot)), testCA + testCARot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projectionData(tc.in)
			if err != nil {
				t.Fatalf("projectionData(%s) = %v, want accepted and sanitized", tc.name, err)
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
// `openssl x509 -text` prints before each certificate is not republished.
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

// TestOwnedSolelyBy separates the two questions the drift check must not
// conflate: is the sentinel among the owners, and is it the only one.
func TestOwnedSolelyBy(t *testing.T) {
	want := sentinelOwnerRef(sentinel())
	other := metav1.OwnerReference{
		APIVersion: "v1", Kind: "ConfigMap", Name: "squatter",
		UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
	}
	for name, tc := range map[string]struct {
		refs []metav1.OwnerReference
		want bool
	}{
		"exactly the sentinel":   {refs: []metav1.OwnerReference{want}, want: true},
		"sentinel plus a second": {refs: []metav1.OwnerReference{want, other}},
		"second plus sentinel":   {refs: []metav1.OwnerReference{other, want}},
		"someone else entirely":  {refs: []metav1.OwnerReference{other}},
		"no owners":              {refs: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ownedSolelyBy(tc.refs, want); got != tc.want {
				t.Errorf("ownedSolelyBy(%d refs) = %v, want %v", len(tc.refs), got, tc.want)
			}
		})
	}
}

// TestProjectionNameCannotCollideWithAnEngineCASecret pins the property the
// canonical name exists to have: no engine CA Secret, for any release, can ever
// produce the dotted "<release>.tenant-ca" name.
func TestProjectionNameCannotCollideWithAnEngineCASecret(t *testing.T) {
	engineSuffixes := []string{"-ca", "-ca-cert", "-cluster-ca-cert", "-clients-ca-cert", "-ssl", "-tls"}
	prefixes := []string{"", "postgres-", "kafka-", "clickhouse-", "http-cache-"}
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
			if errs := validation.IsDNS1035Label(app); len(errs) > 0 {
				t.Fatalf("test fixture %q is not a legal application name: %v", app, errs)
			}
			projection := prefix + app + projectionSuffix
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
