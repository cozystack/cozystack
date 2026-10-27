// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/specschema"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func testBlock() *cozyv1alpha1.ApplicationDefinitionCredentials {
	return &cozyv1alpha1.ApplicationDefinitionCredentials{
		Format:       cozyv1alpha1.CredentialFormat{Name: "scram-sha-256"},
		SecretLabels: map[string]string{"k8s.mariadb.com/watch": ""},
		ValuesPath:   "_accounts",
		Accounts: []cozyv1alpha1.CredentialAccountSource{
			{Kind: cozyv1alpha1.CredentialAccountsValuesMap, Path: "users"},
			{Kind: cozyv1alpha1.CredentialAccountsStatic, Name: "backup", When: "backup.enabled"},
			{Kind: cozyv1alpha1.CredentialAccountsStatic, Name: "root", Owner: cozyv1alpha1.CredentialAccountOwnerPlatform},
		},
	}
}

func testApp() App {
	return App{Namespace: "tenant-foo", Release: "postgres-orders", Group: "apps.cozystack.io", Kind: "Postgres", Name: "orders", Block: testBlock()}
}

func release(uid string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders", Namespace: "tenant-foo", UID: types.UID(uid)}}
}

func accounts(names ...string) []account.Account {
	var out []account.Account
	for _, n := range names {
		out = append(out, account.Account{Username: n, Mapped: strings.ToLower(n)})
	}
	return out
}

func getSecret(t *testing.T, c client.Client, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: name}, sec); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return sec
}

func TestEnsureSeedsAndKeepsTheirs(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	app := testApp()
	now := metav1.Now()

	written, err := Ensure(ctx, c, app, accounts("web"), nil, now, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0] != "postgres-orders.web.account" {
		t.Fatalf("written = %v", written)
	}
	sec := getSecret(t, c, "postgres-orders.web.account")
	if sec.Type != corev1.SecretTypeBasicAuth || string(sec.Data[account.UsernameKey]) != "web" {
		t.Fatalf("seeded Secret = %+v", sec)
	}
	if !strings.HasPrefix(string(sec.Data[account.PasswordKey]), "SCRAM-SHA-256$4096:") {
		t.Fatalf("password not stored in the kind's format: %q", sec.Data[account.PasswordKey])
	}
	if sec.Labels[account.AccountLabel] != "true" || sec.Labels["k8s.mariadb.com/watch"] != "" || sec.Labels["apps.cozystack.io/application.name"] != "orders" {
		t.Fatalf("labels = %v", sec.Labels)
	}
	rec := account.ReadRecord(sec)
	if rec.Origin != corev1alpha1.CredentialOriginNotIssued || rec.By != "jane" || len(rec.History) != 1 || rec.History[0].Operation != corev1alpha1.CredentialOperationCreate {
		t.Fatalf("record = %+v", rec)
	}
	first := string(sec.Data[account.PasswordKey])

	// An update, with the owner known, seeds only what it is given and leaves
	// an owned Secret alone.
	hr := release("uid-1")
	if err := SetOwner(ctx, c, "tenant-foo", written, OwnerReference(hr)); err != nil {
		t.Fatal(err)
	}
	owner := OwnerReference(hr)
	written, err = Ensure(ctx, c, app, accounts("web", "api"), &owner, now, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0] != "postgres-orders.api.account" {
		t.Fatalf("an update seeds only the added account, written = %v", written)
	}
	sec = getSecret(t, c, "postgres-orders.web.account")
	if string(sec.Data[account.PasswordKey]) != first {
		t.Fatal("an owned Secret must survive a later write untouched")
	}
	api := getSecret(t, c, "postgres-orders.api.account")
	if len(api.OwnerReferences) != 1 || api.OwnerReferences[0].UID != "uid-1" {
		t.Fatalf("a Secret seeded on update carries the owner from the start: %v", api.OwnerReferences)
	}
}

func TestEnsureReplacesStale(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	app := testApp()
	now := metav1.Now()

	// Owned by a HelmRelease that is gone: a leftover of a deleted application
	// of the same name, replaced on create so the account gets a new identity.
	old := OwnerReference(release("uid-old"))
	if _, err := Ensure(ctx, c, app, accounts("web"), &old, now, "jane"); err != nil {
		t.Fatal(err)
	}
	before := getSecret(t, c, "postgres-orders.web.account")
	written, err := Ensure(ctx, c, app, accounts("web"), nil, now, "jane")
	if err != nil {
		t.Fatal(err)
	}
	// The fake client assigns no UIDs, so a replacement shows as a new random
	// password and no owner.
	after := getSecret(t, c, "postgres-orders.web.account")
	if len(written) != 1 || string(after.Data[account.PasswordKey]) == string(before.Data[account.PasswordKey]) || len(after.OwnerReferences) != 0 {
		t.Fatalf("expected a fresh unowned Secret, written=%v owners=%v", written, after.OwnerReferences)
	}

	// Same name, another username: not theirs either.
	live := OwnerReference(release("uid-live"))
	if err := SetOwner(ctx, c, "tenant-foo", written, live); err != nil {
		t.Fatal(err)
	}
	written, err = Ensure(ctx, c, app, []account.Account{{Username: "Web", Mapped: "web"}}, &live, now, "jane")
	if err != nil {
		t.Fatal(err)
	}
	renamed := getSecret(t, c, "postgres-orders.web.account")
	if len(written) != 1 || string(renamed.Data[account.PasswordKey]) == string(after.Data[account.PasswordKey]) || string(renamed.Data[account.UsernameKey]) != "Web" {
		t.Fatalf("expected the Secret replaced for the new username, got %q", renamed.Data[account.UsernameKey])
	}
}

func TestPruneDeletesRemovedOnly(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	app := testApp()
	owner := OwnerReference(release("uid-1"))
	if _, err := Ensure(ctx, c, app, accounts("web", "api"), &owner, metav1.Now(), "jane"); err != nil {
		t.Fatal(err)
	}
	// A Secret of another application in the namespace is never touched.
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "postgres-billing.web.account", Namespace: "tenant-foo", Labels: account.Labels(app.Block, app.Group, app.Kind, "billing")}}
	if err := c.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	deleted, err := Prune(ctx, c, app, accounts("api"))
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "postgres-orders.web.account" {
		t.Fatalf("deleted = %v", deleted)
	}
	left, err := List(ctx, c, app)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name != "postgres-orders.api.account" {
		t.Fatalf("left = %v", left)
	}
	getSecret(t, c, "postgres-billing.web.account")
}

func TestValuesFrom(t *testing.T) {
	refs := ValuesFrom(nil, "postgres-orders", accounts("web"))
	if len(refs) != 1 || refs[0].Name != "cozystack-values" {
		t.Fatalf("a kind without a block carries the platform values only: %+v", refs)
	}
	block := testBlock()
	block.ValuesPath = ""
	if refs := ValuesFrom(block, "postgres-orders", accounts("web")); len(refs) != 1 {
		t.Fatalf("a block without a valuesPath adds nothing: %+v", refs)
	}
	refs = ValuesFrom(testBlock(), "postgres-orders", []account.Account{{Username: "Web.1", Mapped: "web-1"}})
	if len(refs) != 2 {
		t.Fatalf("refs = %+v", refs)
	}
	got := refs[1]
	if got.Kind != "Secret" || got.Name != "postgres-orders.web-1.account" || got.ValuesKey != account.PasswordKey || got.TargetPath != "_accounts."+DigestKey("Web.1") {
		t.Fatalf("ref = %+v", got)
	}
	if DigestKey("Web.1") == DigestKey("web.1") || !strings.HasPrefix(DigestKey("x"), "u") {
		t.Fatal("the digest key is of the original user name")
	}
}

func TestResolveAccountsAppliesDefaults(t *testing.T) {
	schema, err := specschema.Build(`{"type":"object","properties":{"backup":{"type":"object","default":{},"properties":{"enabled":{"type":"boolean","default":true}}},"users":{"type":"object","additionalProperties":{"type":"object"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	values := &apiextv1.JSON{Raw: []byte(`{"users":{"web":{},"root":{}}}`)}
	got, err := ResolveAccounts(testBlock(), schema, values)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Username)
	}
	// root is a platform account and is skipped; backup is on by default.
	if strings.Join(names, ",") != "web,backup" {
		t.Fatalf("accounts = %v", names)
	}
	got, err = ResolveAccounts(testBlock(), nil, values)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("without the schema the defaulted flag is off: %v", got)
	}
	if got, err := ResolveAccounts(nil, schema, values); err != nil || got != nil {
		t.Fatalf("no block, no accounts: %v %v", got, err)
	}
}

func TestEnsureCreateReplacesLeftovers(t *testing.T) {
	ctx := context.Background()
	gone := OwnerReference(release("uid-gone"))
	live := release("uid-live")
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(live).Build()
	app := testApp()
	now := metav1.Now()

	// Left by an earlier create that never got its owner, and by a deleted
	// application of the same name whose HelmRelease is gone.
	if _, err := Ensure(ctx, c, app, accounts("web"), nil, now, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(ctx, c, app, accounts("api"), &gone, now, "x"); err != nil {
		t.Fatal(err)
	}
	unowned := getSecret(t, c, "postgres-orders.web.account")
	stale := getSecret(t, c, "postgres-orders.api.account")

	written, err := Ensure(ctx, c, app, accounts("web", "api"), nil, now, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("both leftovers must be replaced and handed to SetOwner, written = %v", written)
	}
	if string(getSecret(t, c, "postgres-orders.web.account").Data[account.PasswordKey]) == string(unowned.Data[account.PasswordKey]) {
		t.Fatal("an unowned leftover kept its verifier")
	}
	if string(getSecret(t, c, "postgres-orders.api.account").Data[account.PasswordKey]) == string(stale.Data[account.PasswordKey]) {
		t.Fatal("a Secret of a gone release kept its verifier")
	}
}

func TestEnsureCreateRefusesLiveOwner(t *testing.T) {
	ctx := context.Background()
	live := release("uid-live")
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(live).Build()
	app := testApp()
	owner := OwnerReference(live)
	if _, err := Ensure(ctx, c, app, accounts("web"), &owner, metav1.Now(), "x"); err != nil {
		t.Fatal(err)
	}
	before := getSecret(t, c, "postgres-orders.web.account")

	written, err := Ensure(ctx, c, app, accounts("web"), nil, metav1.Now(), "jane")
	if !errors.Is(err, ErrLiveOwner) || len(written) != 0 {
		t.Fatalf("written=%v err=%v, want ErrLiveOwner and nothing written", written, err)
	}
	after := getSecret(t, c, "postgres-orders.web.account")
	if string(after.Data[account.PasswordKey]) != string(before.Data[account.PasswordKey]) || len(after.OwnerReferences) != 1 {
		t.Fatal("the live application's Secret must be left untouched")
	}
}

// The application labels alone do not make a Secret an account Secret: the
// lineage webhook puts them on every Secret of the application, and Prune
// must never delete the chart's own.
func TestListAndPruneSeeOnlyAccountSecrets(t *testing.T) {
	ctx := context.Background()
	app := testApp()
	chartOwned := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "postgres-orders-credentials", Namespace: "tenant-foo",
		Labels: map[string]string{
			"apps.cozystack.io/application.group": app.Group,
			"apps.cozystack.io/application.kind":  app.Kind,
			"apps.cozystack.io/application.name":  app.Name,
		},
	}}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(chartOwned).Build()
	if _, err := Ensure(ctx, c, app, accounts("web"), nil, metav1.Now(), "x"); err != nil {
		t.Fatal(err)
	}
	listed, err := List(ctx, c, app)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "postgres-orders.web.account" {
		t.Fatalf("listed = %v", listed)
	}
	if _, err := Prune(ctx, c, app, nil); err != nil {
		t.Fatal(err)
	}
	getSecret(t, c, "postgres-orders-credentials")
}

func TestAdded(t *testing.T) {
	got := Added(accounts("web", "api"), []account.Account{{Username: "web", Mapped: "web"}, {Username: "Api", Mapped: "api"}, {Username: "new", Mapped: "new"}})
	var names []string
	for _, a := range got {
		names = append(names, a.Username)
	}
	// Api is a different user from api, however the name maps.
	if strings.Join(names, ",") != "Api,new" {
		t.Fatalf("added = %v", names)
	}
}
