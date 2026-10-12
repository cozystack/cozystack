package lineagecontrollerwebhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func appDef(name, kind string) *cozyv1alpha1.ApplicationDefinition {
	d := &cozyv1alpha1.ApplicationDefinition{
		TypeMeta:   metav1.TypeMeta{APIVersion: "cozystack.io/v1alpha1", Kind: "ApplicationDefinition"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	d.Spec.Application.Kind = kind
	return d
}

func admissionReq(t *testing.T, op admissionv1.Operation, obj, old *cozyv1alpha1.ApplicationDefinition) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: op, Name: obj.Name, Object: runtime.RawExtension{Raw: raw}}}
	if old != nil {
		oldRaw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		req.OldObject = runtime.RawExtension{Raw: oldRaw}
	}
	return req
}

func TestAppDefKindValidator(t *testing.T) {
	scheme := newWebhookScheme(t)
	existing := appDef("redis", "Redis")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing, appDef("shadow", "Redis")).Build()
	v := NewApplicationDefinitionKindValidator(cl, scheme)

	cases := []struct {
		name    string
		req     admission.Request
		allowed bool
	}{
		{"create with a kind another definition owns is refused", admissionReq(t, admissionv1.Create, appDef("tap-redis", "Redis"), nil), false},
		{"create with a free kind is allowed", admissionReq(t, admissionv1.Create, appDef("pg", "Postgres"), nil), true},
		{"update of the owner itself is allowed", admissionReq(t, admissionv1.Update, appDef("redis", "Redis"), appDef("redis", "Redis")), true},
		// A duplicate that predates the check keeps upgrading; ownership, not
		// admission, keeps it from managing the kind's releases.
		{"update of a pre-existing duplicate that keeps its kind is allowed", admissionReq(t, admissionv1.Update, appDef("shadow", "Redis"), appDef("shadow", "Redis")), true},
		{"update switching to a claimed kind is refused", admissionReq(t, admissionv1.Update, appDef("pg", "Redis"), appDef("pg", "Postgres")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := v.Handle(context.TODO(), tc.req)
			if resp.Allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v (%v)", resp.Allowed, tc.allowed, resp.Result)
			}
		})
	}
}

func helmOwned(d *cozyv1alpha1.ApplicationDefinition, release string) *cozyv1alpha1.ApplicationDefinition {
	d.Annotations = map[string]string{"meta.helm.sh/release-name": release, "meta.helm.sh/release-namespace": "cozy-system"}
	return d
}

// A Helm release that renames its definition object creates the new one before
// it deletes the old one, so both briefly declare the kind. Refusing that would
// fail the platform upgrade that carries the rename.
func TestAppDefKindValidatorAllowsRenameWithinOneHelmRelease(t *testing.T) {
	scheme := newWebhookScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(helmOwned(appDef("redis", "Redis"), "redis-rd")).Build()
	v := NewApplicationDefinitionKindValidator(cl, scheme)

	if resp := v.Handle(context.TODO(), admissionReq(t, admissionv1.Create, helmOwned(appDef("redis-v2", "Redis"), "redis-rd"), nil)); !resp.Allowed {
		t.Fatalf("rename within one Helm release refused: %v", resp.Result)
	}
	resp := v.Handle(context.TODO(), admissionReq(t, admissionv1.Create, helmOwned(appDef("tap-redis", "Redis"), "tap-redis-rd"), nil))
	if resp.Allowed {
		t.Fatal("a definition from another Helm release must still be refused")
	}
	otherNS := helmOwned(appDef("redis-elsewhere", "Redis"), "redis-rd")
	otherNS.Annotations["meta.helm.sh/release-namespace"] = "tenant-foo"
	if resp := v.Handle(context.TODO(), admissionReq(t, admissionv1.Create, otherNS, nil)); resp.Allowed {
		t.Fatal("a release of the same name in another namespace is a different release and must be refused")
	}
	if !strings.Contains(resp.Result.Message, "redis") {
		t.Fatalf("the refusal must name the definition that declares the kind, got %q", resp.Result.Message)
	}
}

// The check is feedback, not the guarantee, so failing to read the existing
// definitions must let the request through rather than refuse it: a 500 answer
// is a verdict, which failurePolicy Ignore does not cover.
func TestAppDefKindValidatorFailsOpenOnListError(t *testing.T) {
	scheme := newWebhookScheme(t)
	cl := interceptor.NewClient(fake.NewClientBuilder().WithScheme(scheme).Build(), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.NewServiceUnavailable("cache not started")
		},
	})
	v := NewApplicationDefinitionKindValidator(cl, scheme)
	resp := v.Handle(context.TODO(), admissionReq(t, admissionv1.Create, appDef("tap-redis", "Redis"), nil))
	if !resp.Allowed {
		t.Fatalf("a failed read must not refuse the request: %v", resp.Result)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("letting a request through unchecked must say so in a warning")
	}
}
