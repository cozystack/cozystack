// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

// The schema defaults backup.enabled to true, so the Static backup account
// is seeded unless the application turns it off.
const credentialsSchema = `{"type":"object","properties":{"backup":{"type":"object","default":{},"properties":{"enabled":{"type":"boolean","default":true}}},"users":{"type":"object","additionalProperties":{"type":"object","properties":{"password":{"type":"string"}}}}}}`

func credentialsBlock() *cozyv1alpha1.ApplicationDefinitionCredentials {
	return &cozyv1alpha1.ApplicationDefinitionCredentials{
		Format:     cozyv1alpha1.CredentialFormat{Name: "scram-sha-256"},
		ValuesPath: "_accounts",
		Accounts: []cozyv1alpha1.CredentialAccountSource{
			{Kind: cozyv1alpha1.CredentialAccountsValuesMap, Path: "users"},
			{Kind: cozyv1alpha1.CredentialAccountsStatic, Name: "backup", When: "backup.enabled"},
			{Kind: cozyv1alpha1.CredentialAccountsStatic, Name: "postgres", Owner: cozyv1alpha1.CredentialAccountOwnerPlatform},
		},
	}
}

func newCredentialsREST(t *testing.T, objects ...client.Object) (*REST, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Resource{
		Application: config.ApplicationConfig{Kind: postgresKind, Singular: "postgres", Plural: "postgreses", OpenAPISchema: credentialsSchema},
		Release:     config.ReleaseConfig{Prefix: "postgres-"},
		Credentials: credentialsBlock(),
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, &config.ResourceConfig{Resources: []config.Resource{*cfg}}); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return NewREST(c, c, cfg), c
}

func credentialsCtx() context.Context {
	return request.WithUser(request.WithNamespace(context.Background(), "tenant-foo"), &user.DefaultInfo{Name: "jane@example.org"})
}

func postgresApp(spec string) *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: postgresKind},
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "tenant-foo"},
		Spec:       &apiextv1.JSON{Raw: []byte(spec)},
	}
}

func accountSecrets(t *testing.T, c client.Client) map[string]*corev1.Secret {
	t.Helper()
	list := &corev1.SecretList{}
	if err := c.List(context.Background(), list, client.InNamespace("tenant-foo"), client.MatchingLabels{account.AccountLabel: "true"}); err != nil {
		t.Fatal(err)
	}
	out := map[string]*corev1.Secret{}
	for i := range list.Items {
		out[list.Items[i].Name] = &list.Items[i]
	}
	return out
}

func getRelease(t *testing.T, c client.Client) *helmv2.HelmRelease {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "postgres-orders"}, hr); err != nil {
		t.Fatal(err)
	}
	return hr
}

func TestCreateSeedsAccountsAndOwnsThem(t *testing.T) {
	r, c := newCredentialsREST(t)
	if _, err := r.Create(credentialsCtx(), postgresApp(`{"users":{"web":{},"postgres":{}}}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hr := getRelease(t, c)
	secrets := accountSecrets(t, c)
	// web from the values, backup from the defaulted flag; postgres is the
	// platform's and gets no account Secret.
	if len(secrets) != 2 || secrets["postgres-orders.web.account"] == nil || secrets["postgres-orders.backup.account"] == nil {
		t.Fatalf("secrets = %v", keys(secrets))
	}
	for name, sec := range secrets {
		if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != hr.Name || sec.OwnerReferences[0].Kind != helmv2.HelmReleaseKind {
			t.Fatalf("%s owners = %v", name, sec.OwnerReferences)
		}
		if account.ReadRecord(sec).By != "jane@example.org" {
			t.Fatalf("%s record = %+v", name, account.ReadRecord(sec))
		}
	}
	if len(hr.Spec.ValuesFrom) != 3 || hr.Spec.ValuesFrom[0].Name != "cozystack-values" || hr.Spec.ValuesFrom[1].Name != "postgres-orders.web.account" || !strings.HasPrefix(hr.Spec.ValuesFrom[1].TargetPath, "_accounts.u") {
		t.Fatalf("valuesFrom = %+v", hr.Spec.ValuesFrom)
	}
}

func TestCreateRefusesCollidingNames(t *testing.T) {
	r, c := newCredentialsREST(t)
	_, err := r.Create(credentialsCtx(), postgresApp(`{"users":{"web_app":{},"web-app":{}}}`), nil, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), `both map to the name "web-app"`) {
		t.Fatalf("expected Invalid naming the collision, got %v", err)
	}
	if n := len(accountSecrets(t, c)); n != 0 {
		t.Fatalf("a refused write seeds nothing, got %d Secrets", n)
	}
}

func TestCreateFailureRemovesSeeded(t *testing.T) {
	existing := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders", Namespace: "tenant-foo"}}
	r, c := newCredentialsREST(t, existing)
	if _, err := r.Create(credentialsCtx(), postgresApp(`{"users":{"web":{}}}`), nil, &metav1.CreateOptions{}); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("expected AlreadyExists, got %v", err)
	}
	if n := len(accountSecrets(t, c)); n != 0 {
		t.Fatalf("Secrets seeded for a release that was not created must go, got %d", n)
	}
}

func TestUpdateSeedsAddedAndPrunesRemoved(t *testing.T) {
	r, c := newCredentialsREST(t)
	ctx := credentialsCtx()
	if _, err := r.Create(ctx, postgresApp(`{"users":{"web":{},"api":{}}}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	webBefore := accountSecrets(t, c)["postgres-orders.web.account"]

	updated := postgresApp(`{"users":{"web":{},"reports":{}},"backup":{"enabled":false}}`)
	if _, _, err := r.Update(ctx, "orders", newDefaultUpdatedObjectInfo(updated), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	secrets := accountSecrets(t, c)
	if len(secrets) != 2 || secrets["postgres-orders.web.account"] == nil || secrets["postgres-orders.reports.account"] == nil {
		t.Fatalf("secrets = %v, want web kept, reports added, api and backup pruned", keys(secrets))
	}
	if string(secrets["postgres-orders.web.account"].Data[account.PasswordKey]) != string(webBefore.Data[account.PasswordKey]) {
		t.Fatal("a kept account's Secret must survive the update untouched")
	}
	hr := getRelease(t, c)
	reports := secrets["postgres-orders.reports.account"]
	if len(reports.OwnerReferences) != 1 || reports.OwnerReferences[0].UID != hr.UID {
		t.Fatalf("added account's owner = %v", reports.OwnerReferences)
	}
	if len(hr.Spec.ValuesFrom) != 3 {
		t.Fatalf("valuesFrom = %+v", hr.Spec.ValuesFrom)
	}
}

func TestDeleteRefusesSuspendedRelease(t *testing.T) {
	r, c := newCredentialsREST(t)
	ctx := credentialsCtx()
	if _, err := r.Create(ctx, postgresApp(`{"users":{"web":{}}}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hr := getRelease(t, c)
	hr.Spec.Suspend = true
	if err := c.Update(ctx, hr); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.Delete(ctx, "orders", nil, &metav1.DeleteOptions{})
	if !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("expected Conflict on a suspended release, got %v", err)
	}
	hr = getRelease(t, c)
	hr.Spec.Suspend = false
	if err := c.Update(ctx, hr); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Delete(ctx, "orders", nil, &metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestWarnMintedPasswords(t *testing.T) {
	r, _ := newCredentialsREST(t)
	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(credentialsCtx(), rec)
	if _, err := r.Create(ctx, postgresApp(`{"users":{"Web":{"password":"hackme"},"api":{}}}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.warnings) != 1 || !strings.Contains(rec.warnings[0], `spec.users["Web"].password is ignored`) || !strings.Contains(rec.warnings[0], "postgres-orders.web") {
		t.Fatalf("warnings = %v", rec.warnings)
	}
}

func keys(m map[string]*corev1.Secret) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
