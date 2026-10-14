// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/store"
)

const ns = "tenant-foo"

func testBlock() *cozyv1alpha1.ApplicationDefinitionCredentials {
	return &cozyv1alpha1.ApplicationDefinitionCredentials{
		Format: cozyv1alpha1.CredentialFormat{Name: "sha256"},
		Accounts: []cozyv1alpha1.CredentialAccountSource{
			{Kind: cozyv1alpha1.CredentialAccountsValuesMap, Path: "users"},
		},
	}
}

// fixture is a fake cluster with one converted Postgres application whose
// declared accounts are fixed by the test.
type fixture struct {
	c       client.Client
	r       *REST
	hr      *helmv2.HelmRelease
	ctx     context.Context
	declare []account.Account
}

func newFixture(t *testing.T, objs ...client.Object) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name: "postgres-orders", Namespace: ns, UID: "hr-uid",
		Labels: map[string]string{
			appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
			appsv1alpha1.ApplicationKindLabel:  "Postgres",
			appsv1alpha1.ApplicationNameLabel:  "orders",
		},
	}}
	f := &fixture{hr: hr}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, hr)...).Build()
	f.c = c
	f.r = NewREST(c, map[string]Kind{"Postgres": {
		Block:    testBlock(),
		Accounts: func(context.Context, *helmv2.HelmRelease) ([]account.Account, error) { return f.declare, nil },
	}})
	f.r.now = func() time.Time { return time.Date(2026, 10, 2, 9, 14, 7, 0, time.UTC) }
	f.ctx = request.WithUser(request.WithNamespace(context.Background(), ns), &user.DefaultInfo{Name: "jane@example.org"})
	return f
}

func (f *fixture) app() store.App {
	return store.App{Namespace: ns, Release: "postgres-orders", Group: appsv1alpha1.GroupName, Kind: "Postgres", Name: "orders", Block: testBlock()}
}

// seed writes an owned account Secret for user and returns it as stored.
func (f *fixture) seed(t *testing.T, username string) *corev1.Secret {
	t.Helper()
	owner := store.OwnerReference(f.hr)
	mapped, _ := account.MapName(username)
	sec, err := store.SeedSecret(f.app(), account.Account{Username: username, Mapped: mapped}, &owner, metav1.NewTime(f.r.now()), "seeder")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(f.ctx, sec); err != nil {
		t.Fatal(err)
	}
	return f.secret(t, sec.Name)
}

func (f *fixture) secret(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	if err := f.c.Get(f.ctx, types.NamespacedName{Namespace: ns, Name: name}, sec); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return sec
}

func (f *fixture) mint(t *testing.T, name, precondition string) (*corev1alpha1.CredentialRequest, error) {
	t.Helper()
	req := &corev1alpha1.CredentialRequest{}
	if precondition != "" {
		req.Spec.Preconditions = &corev1alpha1.CredentialRequestPreconditions{ResourceVersion: precondition}
	}
	out, err := NewMintREST(f.r).Create(f.ctx, name, req, nil, &metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return out.(*corev1alpha1.CredentialRequest), nil
}

func (f *fixture) events(t *testing.T) []corev1.Event {
	t.Helper()
	list := &corev1.EventList{}
	if err := f.c.List(f.ctx, list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestGetShowsRecordNeverData(t *testing.T) {
	f := newFixture(t)
	sec := f.seed(t, "Web")

	obj, err := f.r.Get(f.ctx, "postgres-orders.web", &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cred := obj.(*corev1alpha1.Credential)
	if cred.Name != "postgres-orders.web" || cred.Spec.User != "Web" || cred.Spec.Application.Name != "orders" || cred.Spec.Application.Kind != "Postgres" {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.ResourceVersion != sec.ResourceVersion {
		t.Fatalf("resourceVersion is the Secret's: %q != %q", cred.ResourceVersion, sec.ResourceVersion)
	}
	if cred.Status.Password.Origin != corev1alpha1.CredentialOriginNotIssued || cred.Status.Password.By != "seeder" {
		t.Fatalf("status = %+v", cred.Status)
	}
	table, err := f.r.ConvertToTable(f.ctx, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Cells[1] != "Postgres/orders" || table.Rows[0].Cells[3] != "NotIssued" {
		t.Fatalf("table = %+v", table.Rows)
	}
	for _, name := range []string{"postgres-orders.nobody", "postgres-orders", "postgres-orders.web.account"} {
		if _, err := f.r.Get(f.ctx, name, &metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("Get(%q) = %v, want NotFound", name, err)
		}
	}
}

func TestListSkipsSecretsNoLiveReleaseOwns(t *testing.T) {
	gone := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "postgres-gone", Namespace: ns, UID: "gone-uid"}}
	f := newFixture(t)
	f.seed(t, "web")
	// Owned by a release that no longer exists.
	orphan, _ := store.SeedSecret(store.App{Namespace: ns, Release: "postgres-gone", Group: appsv1alpha1.GroupName, Kind: "Postgres", Name: "gone", Block: testBlock()}, account.Account{Username: "web", Mapped: "web"}, ptr(store.OwnerReference(gone)), metav1.Now(), "x")
	if err := f.c.Create(f.ctx, orphan); err != nil {
		t.Fatal(err)
	}
	// A Secret of an unconverted kind carrying the marker by accident.
	other := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "redis-cache", Namespace: ns, UID: "redis-uid", Labels: map[string]string{appsv1alpha1.ApplicationKindLabel: "Redis", appsv1alpha1.ApplicationNameLabel: "cache"}}}
	if err := f.c.Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	stray, _ := store.SeedSecret(store.App{Namespace: ns, Release: "redis-cache", Kind: "Redis", Name: "cache", Block: testBlock()}, account.Account{Username: "default", Mapped: "default"}, ptr(store.OwnerReference(other)), metav1.Now(), "x")
	if err := f.c.Create(f.ctx, stray); err != nil {
		t.Fatal(err)
	}

	obj, err := f.r.List(f.ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list := obj.(*corev1alpha1.CredentialList)
	if len(list.Items) != 1 || list.Items[0].Name != "postgres-orders.web" {
		t.Fatalf("list = %+v", list.Items)
	}
}

func TestMintReplacesPasswordOnceAndRecords(t *testing.T) {
	f := newFixture(t)
	before := f.seed(t, "web")

	res, err := f.mint(t, "postgres-orders.web", before.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Username != "web" || len(res.Status.Password) != 32 || res.Status.IssuedAt.Time != f.r.now() {
		t.Fatalf("response = %+v", res.Status)
	}
	after := f.secret(t, before.Name)
	if string(after.Data[account.PasswordKey]) == string(before.Data[account.PasswordKey]) {
		t.Fatal("stored value unchanged")
	}
	if strings.Contains(string(after.Data[account.PasswordKey]), res.Status.Password) {
		t.Fatal("the plaintext must not be stored")
	}
	rec := account.ReadRecord(after)
	if rec.Origin != corev1alpha1.CredentialOriginMinted || rec.By != "jane@example.org" || len(rec.History) != 2 || rec.History[0].Operation != corev1alpha1.CredentialOperationMint {
		t.Fatalf("record = %+v", rec)
	}
	evs := f.events(t)
	if len(evs) != 1 || evs[0].Reason != store.ReasonCredentialIssued || evs[0].InvolvedObject.Kind != "Postgres" || evs[0].InvolvedObject.Name != "orders" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Message != "password of user web issued by jane@example.org" || strings.Contains(evs[0].Message, res.Status.Password) {
		t.Fatalf("event message = %q", evs[0].Message)
	}

	// The stale precondition is the one from before the mint: conflict,
	// naming who issued in between, and nothing written.
	_, err = f.mint(t, "postgres-orders.web", before.ResourceVersion)
	if !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "password issued by jane@example.org at 2026-10-02T09:14:07Z") {
		t.Fatalf("expected conflict naming the issuer, got %v", err)
	}
	if string(f.secret(t, before.Name).Data[account.PasswordKey]) != string(after.Data[account.PasswordKey]) {
		t.Fatal("a refused mint must not write")
	}
	// No precondition: always proceeds.
	if _, err := f.mint(t, "postgres-orders.web", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeReturnsNoPassword(t *testing.T) {
	f := newFixture(t)
	before := f.seed(t, "web")
	out, err := NewRevokeREST(f.r).Create(f.ctx, "postgres-orders.web", &corev1alpha1.CredentialRequest{}, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*corev1alpha1.CredentialRequest)
	if res.Status.Password != "" || res.Status.Username != "web" {
		t.Fatalf("response = %+v", res.Status)
	}
	after := f.secret(t, before.Name)
	if string(after.Data[account.PasswordKey]) == string(before.Data[account.PasswordKey]) {
		t.Fatal("revoke must replace the stored value")
	}
	if rec := account.ReadRecord(after); rec.Origin != corev1alpha1.CredentialOriginRevoked {
		t.Fatalf("record = %+v", rec)
	}
	if evs := f.events(t); len(evs) != 1 || evs[0].Reason != store.ReasonCredentialRevoked {
		t.Fatalf("events = %+v", evs)
	}
}

func TestMintCreatesMissingDeclaredAccount(t *testing.T) {
	f := newFixture(t)
	f.declare = []account.Account{{Username: "Web", Mapped: "web"}}

	// Not declared: does not exist.
	if _, err := f.mint(t, "postgres-orders.api", ""); !apierrors.IsNotFound(err) {
		t.Fatalf("undeclared account: %v", err)
	}
	// A stale Secret at the name, from a deleted application, is replaced.
	gone := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders", Namespace: ns, UID: "old-uid"}}
	stale, _ := store.SeedSecret(f.app(), account.Account{Username: "Web", Mapped: "web"}, ptr(store.OwnerReference(gone)), metav1.Now(), "x")
	if err := f.c.Create(f.ctx, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Get(f.ctx, "postgres-orders.web", &metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a stale Secret must not be served: %v", err)
	}
	// A precondition on a missing Secret is ignored.
	res, err := f.mint(t, "postgres-orders.web", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Username != "Web" {
		t.Fatalf("username must be the declared one: %+v", res.Status)
	}
	sec := f.secret(t, "postgres-orders.web.account")
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != f.hr.UID || string(sec.Data[account.UsernameKey]) != "Web" {
		t.Fatalf("created Secret = %+v", sec.ObjectMeta)
	}
	rec := account.ReadRecord(sec)
	if rec.Origin != corev1alpha1.CredentialOriginMinted || len(rec.History) != 1 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestUnconvertedKindHasNoCredentials(t *testing.T) {
	f := newFixture(t)
	f.hr.Labels[appsv1alpha1.ApplicationKindLabel] = "Redis"
	if err := f.c.Update(f.ctx, f.hr); err != nil {
		t.Fatal(err)
	}
	f.seed(t, "web")
	if _, err := f.r.Get(f.ctx, "postgres-orders.web", &metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Get = %v", err)
	}
	if _, err := f.mint(t, "postgres-orders.web", ""); !apierrors.IsNotFound(err) {
		t.Fatalf("mint = %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
