// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
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
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
)

const (
	ns      = "tenant-foo"
	release = "clickhouse-logs"
	cred    = release + ".web"
)

// fixture is a fake cluster with one application release and the templates
// its chart rendered. writes counts every write a request makes, so a test
// can hold the API to one object per request.
type fixture struct {
	c      client.Client
	r      *REST
	ctx    context.Context
	writes []string
}

func newFixture(t *testing.T, objs ...client.Object) *fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, helmv2.AddToScheme, cozyv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Name: release, Namespace: ns, UID: "hr-uid",
		Labels: map[string]string{
			appsv1alpha1.ApplicationGroupLabel: appsv1alpha1.GroupName,
			appsv1alpha1.ApplicationKindLabel:  "ClickHouse",
			appsv1alpha1.ApplicationNameLabel:  "logs",
		},
	}}
	f := &fixture{}
	record := func(verb string) func(ctx context.Context, c client.WithWatch, obj client.Object, _ ...any) {
		return func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...any) {
			if _, isEvent := obj.(*corev1.Event); !isEvent {
				f.writes = append(f.writes, verb+" "+obj.GetName())
			}
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(append(objs, hr)...).
		WithStatusSubresource(&cozyv1alpha1.CredentialTemplate{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				record("create")(ctx, c, obj)
				return c.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				record("update")(ctx, c, obj)
				return c.Update(ctx, obj, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				record("delete")(ctx, c, obj)
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	f.c = c
	f.r = NewREST(c, c)
	f.r.now = func() time.Time { return time.Date(2026, 10, 2, 9, 14, 7, 0, time.UTC) }
	f.ctx = request.WithUser(request.WithNamespace(context.Background(), ns), &user.DefaultInfo{Name: "jane@example.org"})
	return f
}

func template(name, username string) *cozyv1alpha1.CredentialTemplate {
	return &cozyv1alpha1.CredentialTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID("uid-" + name),
			Annotations: map[string]string{account.ReleaseNameAnnotation: release},
		},
		Spec: cozyv1alpha1.CredentialTemplateSpec{
			Username: username,
			Format:   cozyv1alpha1.CredentialTemplateFormat{Name: "sha256"},
			Secret:   cozyv1alpha1.CredentialTemplateSecret{Labels: map[string]string{"reconcile.fluxcd.io/watch": "Enabled"}},
		},
	}
}

// seeded returns a template with its Secret, as the controller leaves them:
// the Secret seeded and the template's status mirroring its version.
func (f *fixture) seeded(t *testing.T, name, username string) (*cozyv1alpha1.CredentialTemplate, *corev1.Secret) {
	t.Helper()
	tmpl := template(name, username)
	if err := f.c.Create(f.ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(f.r.now())
	sec := buildSecret(tmpl, "seed", account.Record{}.Apply(corev1alpha1.CredentialOperationCreate, at, "controller"))
	if err := f.c.Create(f.ctx, sec); err != nil {
		t.Fatal(err)
	}
	tmpl.Status.SecretResourceVersion = sec.ResourceVersion
	tmpl.Status.Password = cozyv1alpha1.CredentialTemplatePasswordState{Origin: "NotIssued", By: "controller", At: &at}
	tmpl.Status.History = []cozyv1alpha1.CredentialTemplateHistoryEntry{{Operation: "Create", At: at, By: "controller"}}
	if err := f.c.Status().Update(f.ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	f.writes = nil
	return tmpl, sec
}

func (f *fixture) secret(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	if err := f.c.Get(f.ctx, types.NamespacedName{Namespace: ns, Name: name}, sec); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return sec
}

func (f *fixture) do(op *OperationREST, name, precondition string, dryRun bool) (*corev1alpha1.CredentialRequest, error) {
	req := &corev1alpha1.CredentialRequest{}
	if precondition != "" {
		req.Spec.Preconditions = &corev1alpha1.CredentialRequestPreconditions{ResourceVersion: precondition}
	}
	opts := &metav1.CreateOptions{}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	out, err := op.Create(f.ctx, name, req, nil, opts)
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

func TestGetShowsTheMirroredRecord(t *testing.T) {
	f := newFixture(t)
	tmpl, sec := f.seeded(t, cred, "web")
	obj, err := f.r.Get(f.ctx, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := obj.(*corev1alpha1.Credential)
	if c.UID != tmpl.UID || c.ResourceVersion != sec.ResourceVersion {
		t.Errorf("uid %q version %q, want the template's uid and the Secret's version", c.UID, c.ResourceVersion)
	}
	if c.Spec.User != "web" || c.Spec.Application.Kind != "ClickHouse" || c.Spec.Application.Name != "logs" {
		t.Errorf("spec = %+v", c.Spec)
	}
	if c.Status.Password.Origin != corev1alpha1.CredentialOriginNotIssued || len(c.Status.History) != 1 {
		t.Errorf("status = %+v", c.Status)
	}
}

func TestGetAndListSkipATemplateWithoutALiveRelease(t *testing.T) {
	orphan := template(release+"-gone.web", "web")
	orphan.Annotations[account.ReleaseNameAnnotation] = release + "-gone"
	unrendered := template(release+".api", "api")
	delete(unrendered.Annotations, account.ReleaseNameAnnotation)
	f := newFixture(t, orphan, unrendered)
	f.seeded(t, cred, "web")
	for _, name := range []string{orphan.Name, unrendered.Name} {
		if _, err := f.r.Get(f.ctx, name, nil); !apierrors.IsNotFound(err) {
			t.Errorf("get %s: err = %v, want NotFound", name, err)
		}
	}
	obj, err := f.r.List(f.ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	items := obj.(*corev1alpha1.CredentialList).Items
	if len(items) != 1 || items[0].Name != cred {
		t.Errorf("list = %v, want only %s", items, cred)
	}
}

func TestListShowsAnAccountWithoutASecretAsNotIssued(t *testing.T) {
	f := newFixture(t, template(cred, "web"))
	obj, err := f.r.List(f.ctx, &metainternal.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	items := obj.(*corev1alpha1.CredentialList).Items
	if len(items) != 1 || items[0].Status.Password.Origin != corev1alpha1.CredentialOriginNotIssued || items[0].ResourceVersion != "" {
		t.Errorf("list = %+v", items)
	}
}

func TestMintWritesOneSecretUnderTheMirroredVersion(t *testing.T) {
	f := newFixture(t)
	tmpl, _ := f.seeded(t, cred, "web")
	res, err := f.do(NewMintREST(f.r), cred, tmpl.Status.SecretResourceVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || f.writes[0] != "update "+cred+account.SecretSuffix {
		t.Errorf("writes = %v, want one update of the account Secret", f.writes)
	}
	if len(res.Status.Password) != 32 || res.Status.Username != "web" {
		t.Fatalf("answer = %+v", res.Status)
	}
	sec := f.secret(t, cred+account.SecretSuffix)
	sum := sha256.Sum256([]byte(res.Status.Password))
	if string(sec.Data[account.PasswordKey]) != hex.EncodeToString(sum[:]) {
		t.Error("the Secret does not hold the verifier of the minted password")
	}
	if sec.Labels["reconcile.fluxcd.io/watch"] != "Enabled" || sec.Labels[account.AccountLabel] != "true" {
		t.Errorf("labels = %v", sec.Labels)
	}
	if ref := metav1.GetControllerOf(sec); ref == nil || ref.UID != tmpl.UID {
		t.Errorf("controller = %v, want the template", ref)
	}
	rec := account.ReadRecord(sec)
	if rec.Origin != corev1alpha1.CredentialOriginMinted || rec.By != "jane@example.org" || len(rec.History) != 2 {
		t.Errorf("record = %+v, want Minted by the caller on top of the mirrored history", rec)
	}
	evs := f.events(t)
	if len(evs) != 1 || evs[0].Reason != ReasonCredentialIssued {
		t.Fatalf("events = %v", evs)
	}
	if strings.Contains(evs[0].Message, res.Status.Password) {
		t.Error("the event carries the password")
	}
}

func TestMintCreatesTheSecretOfAnAccountThatHasNone(t *testing.T) {
	f := newFixture(t, template(cred, "web"))
	res, err := f.do(NewMintREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || f.writes[0] != "create "+cred+account.SecretSuffix {
		t.Errorf("writes = %v, want one create of the account Secret", f.writes)
	}
	if res.Status.Password == "" {
		t.Error("no password")
	}
}

func TestMintConflictsWhenTheStatusLagsTheSecret(t *testing.T) {
	f := newFixture(t)
	tmpl, sec := f.seeded(t, cred, "web")
	// Somebody minted after the status was mirrored.
	sec.Data[account.PasswordKey] = []byte("other")
	if err := f.c.Update(f.ctx, sec); err != nil {
		t.Fatal(err)
	}
	before := f.secret(t, sec.Name)
	f.writes = nil
	res, err := f.do(NewMintREST(f.r), cred, "", false)
	if !apierrors.IsConflict(err) || res != nil {
		t.Fatalf("err = %v, answer = %v, want a conflict and no password", err, res)
	}
	if after := f.secret(t, sec.Name); string(after.Data[account.PasswordKey]) != string(before.Data[account.PasswordKey]) {
		t.Error("the Secret was overwritten")
	}
	_ = tmpl
}

func TestMintConflictsOnAStalePrecondition(t *testing.T) {
	f := newFixture(t)
	f.seeded(t, cred, "web")
	if _, err := f.do(NewMintREST(f.r), cred, "999999", false); !apierrors.IsConflict(err) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

func TestMintRefusesAConflictingSecretAndAnUnknownFormat(t *testing.T) {
	conflict := template(cred, "web")
	conflict.Status.Conditions = []metav1.Condition{{Type: cozyv1alpha1.CredentialTemplateSecretConflict, Status: metav1.ConditionTrue, Reason: "ForeignSecret", LastTransitionTime: metav1.Now()}}
	unknown := template(release+".api", "api")
	unknown.Spec.Format.Name = "apr1"
	f := newFixture(t, conflict, unknown)
	for _, name := range []string{conflict.Name, unknown.Name} {
		if _, err := f.do(NewMintREST(f.r), name, "", false); !apierrors.IsConflict(err) {
			t.Errorf("mint %s: err = %v, want a conflict", name, err)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

func TestMintOnAMissingTemplateIsNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.do(NewMintREST(f.r), release+".nobody", "", false); !apierrors.IsNotFound(err) {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestRevokeReturnsNoPassword(t *testing.T) {
	f := newFixture(t)
	f.seeded(t, cred, "web")
	res, err := f.do(NewRevokeREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Password != "" {
		t.Error("a revoke returned a password")
	}
	if rec := account.ReadRecord(f.secret(t, cred+account.SecretSuffix)); rec.Origin != corev1alpha1.CredentialOriginRevoked {
		t.Errorf("origin = %s, want Revoked", rec.Origin)
	}
	if evs := f.events(t); len(evs) != 1 || evs[0].Reason != ReasonCredentialRevoked {
		t.Errorf("events = %v", evs)
	}
}

func TestDryRunMintAndRevokeWriteNothing(t *testing.T) {
	f := newFixture(t)
	f.seeded(t, cred, "web")
	for _, op := range []*OperationREST{NewMintREST(f.r), NewRevokeREST(f.r)} {
		res, err := f.do(op, cred, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status.Password != "" {
			t.Error("a dry run returned a password")
		}
	}
	if len(f.writes) != 0 || len(f.events(t)) != 0 {
		t.Errorf("writes = %v, events = %v, want none", f.writes, f.events(t))
	}
}

func TestMintLogsNoPassword(t *testing.T) {
	var buf bytes.Buffer
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	_ = fs.Set("v", "10")
	_ = fs.Set("logtostderr", "false")
	klog.SetOutput(&buf)
	defer func() {
		klog.SetOutput(io.Discard)
		_ = fs.Set("logtostderr", "true")
		_ = fs.Set("v", "0")
	}()

	f := newFixture(t)
	f.seeded(t, cred, "web")
	res, err := f.do(NewMintREST(f.r), cred, "", false)
	if err != nil {
		t.Fatal(err)
	}
	klog.Flush()
	if strings.Contains(buf.String(), res.Status.Password) {
		t.Fatal("the minted password reached the log")
	}
}
