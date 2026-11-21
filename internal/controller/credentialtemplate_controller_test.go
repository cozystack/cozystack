// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

const (
	ctNamespace = "tenant-foo"
	ctRelease   = "postgres-orders"
	ctName      = ctRelease + ".web"
)

type ctFixture struct {
	c        client.Client
	r        *CredentialTemplateReconciler
	recorder *record.FakeRecorder
}

func newCTFixture(t *testing.T, seed bool, objs ...client.Object) *ctFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, helmv2.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: ctRelease, Namespace: ctNamespace}}
	if seed {
		hr.Annotations = map[string]string{account.SeedAnnotation: "true"}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(append(objs, hr)...).
		WithStatusSubresource(&cozyv1alpha1.CredentialTemplate{}).
		Build()
	rec := record.NewFakeRecorder(10)
	return &ctFixture{
		c:        c,
		recorder: rec,
		r: &CredentialTemplateReconciler{
			Client: c, Reader: c, Scheme: scheme, Recorder: rec,
			Now: func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) },
		},
	}
}

func ctTemplate(formatName string) *cozyv1alpha1.CredentialTemplate {
	return &cozyv1alpha1.CredentialTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctName, Namespace: ctNamespace, UID: "tmpl-uid",
			Annotations: map[string]string{account.ReleaseNameAnnotation: ctRelease},
		},
		Spec: cozyv1alpha1.CredentialTemplateSpec{
			Username: "web",
			Format:   cozyv1alpha1.CredentialTemplateFormat{Name: formatName},
			Secret:   cozyv1alpha1.CredentialTemplateSecret{Labels: map[string]string{"cnpg.io/reload": "true"}},
		},
	}
}

func (f *ctFixture) reconcile(t *testing.T) {
	t.Helper()
	if _, err := f.r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ctNamespace, Name: ctName}}); err != nil {
		t.Fatal(err)
	}
}

func (f *ctFixture) secret(t *testing.T) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: ctName + account.SecretSuffix}, sec)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

func (f *ctFixture) template(t *testing.T) *cozyv1alpha1.CredentialTemplate {
	t.Helper()
	tmpl := &cozyv1alpha1.CredentialTemplate{}
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: ctName}, tmpl); err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func TestCredentialTemplateSeedsAMarkedRelease(t *testing.T) {
	f := newCTFixture(t, true, ctTemplate("scram-sha-256"))
	f.reconcile(t)
	sec := f.secret(t)
	if sec == nil {
		t.Fatal("no Secret seeded")
	}
	if ref := metav1.GetControllerOf(sec); ref == nil || ref.Name != ctName {
		t.Errorf("controller = %v, want the template", ref)
	}
	if string(sec.Data[account.UsernameKey]) != "web" || len(sec.Data[account.PasswordKey]) == 0 {
		t.Errorf("data = %v", sec.Data)
	}
	if sec.Labels["cnpg.io/reload"] != "true" || sec.Labels[account.AccountLabel] != "true" || sec.Annotations[account.FormatAnnotation] != "scram-sha-256" {
		t.Errorf("labels %v annotations %v", sec.Labels, sec.Annotations)
	}
	if rec := account.ReadRecord(sec); rec.Origin != corev1alpha1.CredentialOriginNotIssued || rec.By != CredentialControllerActor {
		t.Errorf("record = %+v", rec)
	}
	st := f.template(t).Status
	if st.SecretResourceVersion != sec.ResourceVersion || st.Password.Origin != "NotIssued" || len(st.History) != 1 {
		t.Errorf("status = %+v, want the Secret's record mirrored", st)
	}
}

func TestCredentialTemplateLeavesAnUnmarkedReleaseToAMint(t *testing.T) {
	f := newCTFixture(t, false, ctTemplate("scram-sha-256"))
	f.reconcile(t)
	if f.secret(t) != nil {
		t.Fatal("a Secret was seeded on a release whose clients may hold the old password")
	}
	if st := f.template(t).Status; st.SecretResourceVersion != "" || st.Password.Origin != "NotIssued" {
		t.Errorf("status = %+v", st)
	}
}

func TestCredentialTemplateReseedsWhenTheFormatChanges(t *testing.T) {
	f := newCTFixture(t, true, ctTemplate("scram-sha-256"))
	f.reconcile(t)
	before := f.secret(t)

	tmpl := f.template(t)
	tmpl.Spec.Format.Name = "sha256"
	if err := f.c.Update(context.Background(), tmpl); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	after := f.secret(t)
	if after.Annotations[account.FormatAnnotation] != "sha256" || string(after.Data[account.PasswordKey]) == string(before.Data[account.PasswordKey]) {
		t.Errorf("Secret not seeded again in the new format: %v", after.Annotations)
	}
	if rec := account.ReadRecord(after); rec.Origin != corev1alpha1.CredentialOriginNotIssued || len(rec.History) != 2 {
		t.Errorf("record = %+v, want a second Create on top of the first", rec)
	}
	select {
	case ev := <-f.recorder.Events:
		if ev == "" {
			t.Error("empty event")
		}
	default:
		t.Error("no event recorded for the reset")
	}
}

func TestCredentialTemplateAdoptsAMigratedSecret(t *testing.T) {
	migrated := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctName + account.SecretSuffix, Namespace: ctNamespace,
			Labels: map[string]string{account.AccountLabel: "true"},
		},
		Data: map[string][]byte{account.UsernameKey: []byte("web"), account.PasswordKey: []byte("SCRAM-SHA-256$migrated")},
	}
	account.WriteRecord(migrated, account.Record{}.Apply(corev1alpha1.CredentialOperationMigrate, metav1.Now(), "migration"))
	f := newCTFixture(t, true, ctTemplate("scram-sha-256"), migrated)
	f.reconcile(t)
	sec := f.secret(t)
	if ref := metav1.GetControllerOf(sec); ref == nil || ref.Name != ctName {
		t.Fatalf("controller = %v, want the template", ref)
	}
	if string(sec.Data[account.PasswordKey]) != "SCRAM-SHA-256$migrated" {
		t.Error("the migrated verifier was replaced")
	}
	if sec.Labels["cnpg.io/reload"] != "true" || sec.Annotations[account.FormatAnnotation] != "scram-sha-256" {
		t.Errorf("labels %v annotations %v", sec.Labels, sec.Annotations)
	}
	if st := f.template(t).Status; st.Password.Origin != "Migrated" {
		t.Errorf("origin = %q, want Migrated", st.Password.Origin)
	}
}

func TestCredentialTemplateLeavesAForeignSecretAlone(t *testing.T) {
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ctName + account.SecretSuffix, Namespace: ctNamespace},
		Data:       map[string][]byte{"key": []byte("value")},
	}
	f := newCTFixture(t, true, ctTemplate("scram-sha-256"), foreign)
	f.reconcile(t)
	sec := f.secret(t)
	if metav1.GetControllerOf(sec) != nil || string(sec.Data["key"]) != "value" {
		t.Error("the controller touched a Secret it did not create")
	}
	if !meta.IsStatusConditionTrue(f.template(t).Status.Conditions, cozyv1alpha1.CredentialTemplateSecretConflict) {
		t.Error("no SecretConflict condition")
	}
}

func TestCredentialTemplateReportsAnUnknownFormat(t *testing.T) {
	f := newCTFixture(t, true, ctTemplate("apr1"))
	f.reconcile(t)
	if f.secret(t) != nil {
		t.Error("a Secret was seeded in a format the controller does not have")
	}
	if !meta.IsStatusConditionTrue(f.template(t).Status.Conditions, cozyv1alpha1.CredentialTemplateUnknownFormat) {
		t.Error("no UnknownFormat condition")
	}
}

func TestCredentialTemplateMirrorsAMint(t *testing.T) {
	f := newCTFixture(t, true, ctTemplate("scram-sha-256"))
	f.reconcile(t)
	sec := f.secret(t)
	account.WriteRecord(sec, account.ReadRecord(sec).Apply(corev1alpha1.CredentialOperationMint, metav1.Now(), "jane"))
	if err := f.c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	st := f.template(t).Status
	if st.Password.Origin != "Minted" || st.Password.By != "jane" || st.SecretResourceVersion != f.secret(t).ResourceVersion {
		t.Errorf("status = %+v, want the mint mirrored", st)
	}
}

func TestDesiredValuesFrom(t *testing.T) {
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-logs"}}
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{
		{Kind: "Secret", Name: "cozystack-values"},
		account.ValuesFrom("clickhouse-logs.gone.account", "_accounts", "gone"),
	}
	mk := func(name, user, path, release string) cozyv1alpha1.CredentialTemplate {
		return cozyv1alpha1.CredentialTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{account.ReleaseNameAnnotation: release}},
			Spec:       cozyv1alpha1.CredentialTemplateSpec{Username: user, ValuesPath: path},
		}
	}
	got := DesiredValuesFrom(hr, []cozyv1alpha1.CredentialTemplate{
		mk("clickhouse-logs.web", "web", "_accounts", "clickhouse-logs"),
		mk("clickhouse-logs.api", "api", "_accounts", "clickhouse-logs"),
		mk("clickhouse-logs.nopath", "nopath", "", "clickhouse-logs"),
		mk("other.web", "web", "_accounts", "other"),
	})
	if len(got) != 3 || got[0].Name != "cozystack-values" || got[1].Name != "clickhouse-logs.api.account" || got[2].Name != "clickhouse-logs.web.account" {
		t.Errorf("valuesFrom = %+v, want the platform values, then api and web, and no entry for a removed account", got)
	}
}

func TestCredentialValuesKeepsAnEntryWhoseSecretExists(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, helmv2.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	pending := account.ValuesFrom("clickhouse-logs.api.account", "_accounts", "api")
	gone := account.ValuesFrom("clickhouse-logs.gone.account", "_accounts", "gone")
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-logs", Namespace: ctNamespace}}
	hr.Spec.ValuesFrom = []helmv2.ValuesReference{{Kind: "Secret", Name: "cozystack-values"}, pending, gone}
	tmpl := &cozyv1alpha1.CredentialTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-logs.web", Namespace: ctNamespace, Annotations: map[string]string{account.ReleaseNameAnnotation: "clickhouse-logs"}},
		Spec:       cozyv1alpha1.CredentialTemplateSpec{Username: "web", ValuesPath: "_accounts"},
	}
	// The conversion wrote api's Secret before the chart rendered its template.
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "clickhouse-logs.api.account", Namespace: ctNamespace}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr, tmpl, sec).Build()
	r := &CredentialValuesReconciler{Client: c, Reader: c}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ctNamespace, Name: "clickhouse-logs"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 {
		t.Error("a kept entry must be looked at again")
	}
	got := &helmv2.HelmRelease{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ctNamespace, Name: "clickhouse-logs"}, got); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ref := range got.Spec.ValuesFrom {
		names[ref.Name] = true
	}
	if !names["clickhouse-logs.web.account"] || !names["clickhouse-logs.api.account"] || names["clickhouse-logs.gone.account"] {
		t.Errorf("valuesFrom = %+v, want web (template), api (Secret, template pending), and no gone", got.Spec.ValuesFrom)
	}
}
