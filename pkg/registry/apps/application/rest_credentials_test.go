// SPDX-License-Identifier: Apache-2.0

package application

import (
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

func newCredentialsTestREST(t *testing.T, objs ...client.Object) (*REST, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	resourceCfg := &config.ResourceConfig{Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: "Postgres"}}}}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &REST{
		c:             c,
		w:             c,
		gvr:           schema.GroupVersionResource{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "postgreses"},
		gvk:           schema.GroupVersionKind{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: "Postgres"},
		kindName:      "Postgres",
		releaseConfig: config.ReleaseConfig{Prefix: "postgres-"},
	}, c
}

func accountTemplate(release string) *cozyv1alpha1.CredentialTemplate {
	return &cozyv1alpha1.CredentialTemplate{ObjectMeta: metav1.ObjectMeta{
		Name: release + ".web", Namespace: "tenant-foo",
		Annotations: map[string]string{account.ReleaseNameAnnotation: release},
	}}
}

// TestUpdate_KeepsAccountValuesFromAndTheSeedMark pins that an Application
// write leaves the CredentialTemplate controller's state on the release: the
// valuesFrom entries that carry each account's stored value into the chart,
// and the mark that lets the controller seed. The rebuilt release lists only
// the platform values, so dropping the entries would take the accounts out of
// the chart until the controller put them back.
func TestUpdate_KeepsAccountValuesFromAndTheSeedMark(t *testing.T) {
	hr := suspendTestHelmRelease(false)
	hr.Annotations = map[string]string{account.SeedAnnotation: "true"}
	entry := account.ValuesFrom("postgres-pg.web.account", "_accounts", "web")
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}, entry}
	r, c := newCredentialsTestREST(t, hr)

	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Update(ctx, "pg", newDefaultUpdatedObjectInfo(suspendTestApplication()), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got := &helmv2.HelmRelease{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-foo", Name: "postgres-pg"}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.ValuesFrom) != 2 || got.Spec.ValuesFrom[1] != entry {
		t.Errorf("valuesFrom = %+v, want the platform values and the account entry", got.Spec.ValuesFrom)
	}
	if got.Annotations[account.SeedAnnotation] != "true" {
		t.Error("the seed mark was dropped")
	}
}

func TestMarkForSeeding(t *testing.T) {
	hr := &helmv2.HelmRelease{}
	markForSeeding(hr)
	if hr.Annotations[account.SeedAnnotation] != "true" {
		t.Errorf("annotations = %v", hr.Annotations)
	}
}

// TestDelete_RefusesASuspendedReleaseWithAccounts: Flux skips the uninstall of
// a suspended release, so deleting it would leave the database running after
// its account Secrets went with the templates.
func TestDelete_RefusesASuspendedReleaseWithAccounts(t *testing.T) {
	r, _ := newCredentialsTestREST(t, suspendTestHelmRelease(true), accountTemplate("postgres-pg"))
	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Delete(ctx, "pg", nil, &metav1.DeleteOptions{}); !apierrors.IsConflict(err) {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

func TestDelete_AllowsASuspendedReleaseWithoutAccounts(t *testing.T) {
	r, _ := newCredentialsTestREST(t, suspendTestHelmRelease(true), accountTemplate("postgres-other"))
	ctx := request.WithNamespace(context.Background(), "tenant-foo")
	if _, _, err := r.Delete(ctx, "pg", nil, &metav1.DeleteOptions{}); apierrors.IsConflict(err) {
		t.Fatalf("err = %v, a release without accounts must delete", err)
	}
}
