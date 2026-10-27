// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/credentials/format"
)

func newMigrator(t *testing.T, objs ...client.Object) *migrator {
	t.Helper()
	return newMigratorWith(t, nil, objs...)
}

// newMigratorWith builds the migrator on a client whose calls go through funcs.
func newMigratorWith(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *migrator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&helmv2.HelmRelease{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	o := &options{
		migration: 60, kind: "Postgres", formatName: format.ScramSHA256, accountsPath: "users",
		labels: labelFlags{"cnpg.io/reload": "true"}, sourceSuffix: "-credentials",
		stateNamespace: "cozy-system", actor: "migration", timeout: -time.Second,
	}
	return &migrator{c: c, o: o, now: func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }}
}

func release(values string, chartVersion string, ready bool) *helmv2.HelmRelease {
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders", Namespace: "tenant-foo", UID: "hr-uid", Labels: map[string]string{
			appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
			appsv1alpha1.ApplicationKindLabel:  "Postgres",
			appsv1alpha1.ApplicationNameLabel:  "orders",
		}},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(values)}},
	}
	if chartVersion != "" {
		hr.Status.History = helmv2.Snapshots{{Version: 1, ChartVersion: chartVersion, Status: "deployed"}}
	}
	if ready {
		hr.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "x", LastTransitionTime: metav1.Now()}}
	}
	return hr
}

func sourceSecret(data map[string]string) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders-credentials", Namespace: "tenant-foo"}, Data: map[string][]byte{}}
	for k, v := range data {
		sec.Data[k] = []byte(v)
	}
	return sec
}

func (m *migrator) secret(t *testing.T, name string) (*corev1.Secret, bool) {
	t.Helper()
	sec := &corev1.Secret{}
	err := m.c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: name}, sec)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sec, true
}

func TestSeedRecordsAndMigrates(t *testing.T) {
	ctx := context.Background()
	m := newMigrator(t, release(`{"users":{"web":{},"Api.1":{}}}`, "0.1.0", true), sourceSecret(map[string]string{"web": "hunter2", "Api.1": "secret"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := m.loadState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := st.Releases["tenant-foo/postgres-orders"]
	if rec == nil || rec.ChartVersion != "0.1.0" || rec.Done || len(st.Skipped) != 0 {
		t.Fatalf("state = %+v", st)
	}
	for _, c := range []struct{ secret, user, plain string }{{"postgres-orders.web.account", "web", "hunter2"}, {"postgres-orders.api-1.account", "Api.1", "secret"}} {
		sec, ok := m.secret(t, c.secret)
		if !ok {
			t.Fatalf("%s not written", c.secret)
		}
		if string(sec.Data[account.UsernameKey]) != c.user || !strings.HasPrefix(string(sec.Data[account.PasswordKey]), "SCRAM-SHA-256$4096:") {
			t.Fatalf("%s data = %q", c.secret, sec.Data)
		}
		if strings.Contains(string(sec.Data[account.PasswordKey]), c.plain) {
			t.Fatal("the plaintext must not be stored")
		}
		if sec.Labels["cnpg.io/reload"] != "true" || sec.Labels[account.AccountLabel] != "true" || len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != "hr-uid" {
			t.Fatalf("%s meta = %+v", c.secret, sec.ObjectMeta)
		}
		r := account.ReadRecord(sec)
		if r.Origin != corev1alpha1.CredentialOriginMigrated || r.By != "migration" || len(r.History) != 1 || r.History[0].Operation != corev1alpha1.CredentialOperationMigrate {
			t.Fatalf("%s record = %+v", c.secret, r)
		}
	}
	// A second run keeps what it wrote.
	before, _ := m.secret(t, "postgres-orders.web.account")
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := m.secret(t, "postgres-orders.web.account")
	if string(after.Data[account.PasswordKey]) != string(before.Data[account.PasswordKey]) {
		t.Fatal("seed must be idempotent")
	}
}

func TestSeedLeavesOutUnresolvableAndRecordsMissingSource(t *testing.T) {
	ctx := context.Background()
	colliding := release(`{"users":{"web_app":{},"web-app":{}}}`, "0.1.0", true)
	m := newMigrator(t, colliding)
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := m.loadState(ctx)
	if why := st.Skipped["tenant-foo/postgres-orders"]; !strings.Contains(why, "both map to the name") {
		t.Fatalf("skipped = %v", st.Skipped)
	}
	evs := &corev1.EventList{}
	if err := m.c.List(ctx, evs, client.InNamespace("tenant-foo")); err != nil || len(evs.Items) != 1 || evs.Items[0].Reason != reasonSkipped {
		t.Fatalf("events = %+v err=%v", evs.Items, err)
	}

	// No source Secret: recorded, nothing seeded, not skipped.
	m = newMigrator(t, release(`{"users":{"web":{}}}`, "0.1.0", true))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = m.loadState(ctx)
	if st.Releases["tenant-foo/postgres-orders"] == nil || len(st.Skipped) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if _, ok := m.secret(t, "postgres-orders.web.account"); ok {
		t.Fatal("nothing to seed from")
	}
}

func TestFinishWaitsForTheSwitchThenStripsAndRetires(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{"password":"leftover"}},"size":"10Gi"}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	// Still on the recorded chart version: left out, reported, state kept.
	err := m.finish(ctx)
	if err == nil || !strings.Contains(err.Error(), "1 release(s) left out") {
		t.Fatalf("finish on an unswitched release: %v", err)
	}
	if _, ok := m.secret(t, "postgres-orders-credentials"); !ok {
		t.Fatal("the source must survive until the chart has switched")
	}

	// Switched and Ready.
	cur := &helmv2.HelmRelease{}
	if err := m.c.Get(ctx, client.ObjectKeyFromObject(hr), cur); err != nil {
		t.Fatal(err)
	}
	cur.Status.History = helmv2.Snapshots{{Version: 2, ChartVersion: "0.2.0", Status: "deployed"}}
	if err := m.c.Status().Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := m.loadState(ctx)
	if rec := st.Releases["tenant-foo/postgres-orders"]; rec == nil || !rec.Done || len(st.Skipped) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if err := m.c.Get(ctx, client.ObjectKeyFromObject(hr), cur); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(cur.Spec.Values.Raw, &values); err != nil {
		t.Fatal(err)
	}
	if _, has := values["users"].(map[string]any)["web"].(map[string]any)["password"]; has || values["size"] != "10Gi" {
		t.Fatalf("values = %v", values)
	}
	if _, ok := m.secret(t, "postgres-orders-credentials"); ok {
		t.Fatal("the source Secret of a kind without platform accounts must be deleted")
	}
	if _, ok := m.secret(t, "postgres-orders.web.account"); !ok {
		t.Fatal("the account Secret must stay")
	}
}

func TestRetireSourceKeepsPlatformKeys(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2", "root": "toor"}))
	m.o.platformKeys = stringFlags{"root"}
	if err := m.retireSource(ctx, hr); err != nil {
		t.Fatal(err)
	}
	sec, ok := m.secret(t, "postgres-orders-credentials")
	if !ok || string(sec.Data["root"]) != "toor" || len(sec.Data) != 1 {
		t.Fatalf("source = %v", sec)
	}
}

// A transient API error must fail the hook, so the upgrade retries: left out
// as a skip, the chart would switch and take the plaintext with it.
func TestSeedFailsClosedOnAnAPIError(t *testing.T) {
	ctx := context.Background()
	m := newMigratorWith(t, &interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if sec, ok := obj.(*corev1.Secret); ok && strings.HasSuffix(sec.Name, ".account") {
				return apierrors.NewServiceUnavailable("etcd is busy")
			}
			return cl.Create(ctx, obj, opts...)
		},
	}, release(`{"users":{"web":{}}}`, "0.1.0", true), sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err == nil || !strings.Contains(err.Error(), "etcd is busy") {
		t.Fatalf("seed = %v, want the API error", err)
	}
	st, err := m.loadState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Skipped) != 0 {
		t.Fatalf("a transient error must not be recorded as a skip: %v", st.Skipped)
	}
}

func TestFinishHoldsAReleaseWithNoRecordedVersion(t *testing.T) {
	ctx := context.Background()
	m := newMigrator(t, release(`{"users":{"web":{}}}`, "", true), sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	m.switchChart(t, "0.2.0", true)
	if err := m.finish(ctx); err == nil {
		t.Fatal("finish must report a release it cannot confirm switched")
	}
	if _, ok := m.secret(t, "postgres-orders-credentials"); !ok {
		t.Fatal("the source must stay while the switch cannot be confirmed")
	}
}

func TestFinishWaitsForReady(t *testing.T) {
	ctx := context.Background()
	m := newMigrator(t, release(`{"users":{"web":{"password":"leftover"}}}`, "0.1.0", true), sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	// On the new chart, but the upgrade has not succeeded.
	m.switchChart(t, "0.2.0", false)
	if err := m.finish(ctx); err == nil {
		t.Fatal("finish must hold a release that is not Ready")
	}
	if _, ok := m.secret(t, "postgres-orders-credentials"); !ok {
		t.Fatal("the source must stay until the release is Ready on the new chart")
	}
}

func TestFinishTreatsADeletedReleaseAsDone(t *testing.T) {
	ctx := context.Background()
	hr := release(`{"users":{"web":{}}}`, "0.1.0", true)
	m := newMigrator(t, hr, sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.c.Delete(ctx, hr); err != nil {
		t.Fatal(err)
	}
	if err := m.finish(ctx); err != nil {
		t.Fatalf("finish = %v, want nothing left to convert", err)
	}
}

func TestSeedKeepsTheFirstRecordedVersion(t *testing.T) {
	ctx := context.Background()
	m := newMigrator(t, release(`{"users":{"web":{}}}`, "0.1.0", true), sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	// The hook runs again after the chart has moved on, as a retried upgrade does.
	m.switchChart(t, "0.2.0", true)
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := m.loadState(ctx)
	if v := st.Releases["tenant-foo/postgres-orders"].ChartVersion; v != "0.1.0" {
		t.Fatalf("recorded version = %q, want the first one", v)
	}
}

func TestSeedReplacesAForeignSecretAndSkipsAMissingPlaintext(t *testing.T) {
	ctx := context.Background()
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "postgres-orders.web.account", Namespace: "tenant-foo"},
		Data: map[string][]byte{account.UsernameKey: []byte("web"), account.PasswordKey: []byte("planted")}}
	m := newMigrator(t, release(`{"users":{"web":{},"api":{}}}`, "0.1.0", true), foreign, sourceSecret(map[string]string{"web": "hunter2"}))
	if err := m.seed(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := m.secret(t, "postgres-orders.web.account")
	if string(sec.Data[account.PasswordKey]) == "planted" || !account.IsAccountSecret(sec) {
		t.Fatal("a Secret that is not the release's own must be replaced, or the operator applies its verifier")
	}
	if _, ok := m.secret(t, "postgres-orders.api.account"); ok {
		t.Fatal("an account with no plaintext to migrate is left to its first mint")
	}
}

// switchChart moves the release to another chart version, Ready or not.
func (m *migrator) switchChart(t *testing.T, version string, ready bool) {
	t.Helper()
	ctx := context.Background()
	cur := &helmv2.HelmRelease{}
	if err := m.c.Get(ctx, types.NamespacedName{Namespace: "tenant-foo", Name: "postgres-orders"}, cur); err != nil {
		t.Fatal(err)
	}
	cur.Status.History = helmv2.Snapshots{{Version: 2, ChartVersion: version, Status: "deployed"}}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	cur.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: status, Reason: "x", LastTransitionTime: metav1.Now()}}
	if err := m.c.Status().Update(ctx, cur); err != nil {
		t.Fatal(err)
	}
}
