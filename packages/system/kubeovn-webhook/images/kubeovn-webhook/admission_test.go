package main

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type patchOp struct {
	Op    string            `json:"op"`
	Path  string            `json:"path"`
	Value map[string]string `json:"value"`
}

// withNamespace stubs the namespace lookup and the global flags for one test.
func withNamespace(t *testing.T, annotations map[string]string, err error, routes string, portSecurity bool) {
	t.Helper()
	get, r, ps := getNamespaceAnnotations, RoutesGlobal, PortSecurityGlobal
	getNamespaceAnnotations = func(string) (map[string]string, error) { return annotations, err }
	RoutesGlobal, PortSecurityGlobal = routes, portSecurity
	t.Cleanup(func() { getNamespaceAnnotations, RoutesGlobal, PortSecurityGlobal = get, r, ps })
}

func review(t *testing.T, op admissionv1.Operation, kind string, podAnnotations map[string]string) []byte {
	t.Helper()
	pod, err := json.Marshal(corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", Annotations: podAnnotations}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       types.UID("req-uid"),
			Kind:      metav1.GroupVersionKind{Version: "v1", Kind: kind},
			Operation: op,
			Namespace: "ns",
			Object:    runtime.RawExtension{Raw: pod},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mutate(t *testing.T, body []byte) (*httptest.ResponseRecorder, admissionv1.AdmissionReview) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleMutatePods(rec, httptest.NewRequest(http.MethodPost, "/mutate-pods", strings.NewReader(string(body))))
	var out admissionv1.AdmissionReview
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, out
}

func patchOf(t *testing.T, resp *admissionv1.AdmissionResponse) []patchOp {
	t.Helper()
	if resp == nil {
		t.Fatal("no response")
	}
	if !resp.Allowed {
		t.Fatalf("pod not allowed: %+v", resp.Result)
	}
	if resp.Patch == nil {
		return nil
	}
	if resp.PatchType == nil || *resp.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Fatalf("patchType = %v, want JSONPatch", resp.PatchType)
	}
	var ops []patchOp
	if err := json.Unmarshal(resp.Patch, &ops); err != nil {
		t.Fatalf("decode patch %q: %v", resp.Patch, err)
	}
	return ops
}

func TestMutatePodsAddsAnnotationsFromNamespace(t *testing.T) {
	withNamespace(t, map[string]string{AnnotationRoutes: "ns-routes", AnnotationPortSecurity: "false"}, nil, "global-routes", true)
	rec, out := mutate(t, review(t, admissionv1.Create, "Pod", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if out.APIVersion != "admission.k8s.io/v1" || out.Kind != "AdmissionReview" {
		t.Errorf("response TypeMeta = %+v", out.TypeMeta)
	}
	if out.Response.UID != "req-uid" {
		t.Errorf("UID = %q, want the request UID", out.Response.UID)
	}
	ops := patchOf(t, out.Response)
	want := map[string]string{AnnotationRoutes: "ns-routes", AnnotationPortSecurity: "false"}
	if len(ops) != 1 || ops[0].Op != "add" || ops[0].Path != "/metadata/annotations" || !maps.Equal(ops[0].Value, want) {
		t.Fatalf("patch = %+v, want one add of %v at /metadata/annotations", ops, want)
	}
}

func TestMutatePodsFallsBackToGlobalFlags(t *testing.T) {
	withNamespace(t, nil, nil, "global-routes", true)
	_, out := mutate(t, review(t, admissionv1.Create, "Pod", map[string]string{"keep": "me"}))
	ops := patchOf(t, out.Response)
	want := map[string]string{"keep": "me", AnnotationRoutes: "global-routes", AnnotationPortSecurity: "true"}
	if len(ops) != 1 || ops[0].Op != "replace" || ops[0].Path != "/metadata/annotations" || !maps.Equal(ops[0].Value, want) {
		t.Fatalf("patch = %+v, want one replace with %v", ops, want)
	}
}

func TestMutatePodsKeepsPodAnnotations(t *testing.T) {
	withNamespace(t, map[string]string{AnnotationRoutes: "ns-routes", AnnotationPortSecurity: "true"}, nil, "", false)
	_, out := mutate(t, review(t, admissionv1.Create, "Pod", map[string]string{AnnotationRoutes: "pod-routes", AnnotationPortSecurity: "false"}))
	if ops := patchOf(t, out.Response); ops != nil {
		t.Fatalf("patch = %+v, want none when the pod sets both annotations", ops)
	}
}

func TestMutatePodsKeepsPodRoutesWhileAddingPortSecurity(t *testing.T) {
	withNamespace(t, map[string]string{AnnotationRoutes: "ns-routes", AnnotationPortSecurity: "true"}, nil, "", false)
	_, out := mutate(t, review(t, admissionv1.Create, "Pod", map[string]string{AnnotationRoutes: "pod-routes"}))
	ops := patchOf(t, out.Response)
	want := map[string]string{AnnotationRoutes: "pod-routes", AnnotationPortSecurity: "true"}
	if len(ops) != 1 || !maps.Equal(ops[0].Value, want) {
		t.Fatalf("patch = %+v, want annotations %v", ops, want)
	}
}

func TestMutatePodsNothingToAdd(t *testing.T) {
	withNamespace(t, map[string]string{}, nil, "", false)
	_, out := mutate(t, review(t, admissionv1.Create, "Pod", nil))
	if ops := patchOf(t, out.Response); ops != nil {
		t.Fatalf("patch = %+v, want none", ops)
	}
}

func TestMutatePodsPassesThroughOtherRequests(t *testing.T) {
	withNamespace(t, map[string]string{AnnotationRoutes: "ns-routes"}, nil, "global-routes", true)
	for _, tc := range []struct {
		op   admissionv1.Operation
		kind string
	}{
		{admissionv1.Update, "Pod"},
		{admissionv1.Create, "Deployment"},
	} {
		_, out := mutate(t, review(t, tc.op, tc.kind, nil))
		if ops := patchOf(t, out.Response); ops != nil {
			t.Errorf("%s %s: patch = %+v, want none", tc.op, tc.kind, ops)
		}
		if out.Response.UID != "req-uid" {
			t.Errorf("%s %s: UID = %q", tc.op, tc.kind, out.Response.UID)
		}
	}
}

func TestMutatePodsAllowsWhenNamespaceLookupFails(t *testing.T) {
	withNamespace(t, nil, errors.New("forbidden"), "global-routes", true)
	_, out := mutate(t, review(t, admissionv1.Create, "Pod", nil))
	if ops := patchOf(t, out.Response); ops != nil {
		t.Fatalf("patch = %+v, want none when the namespace cannot be read", ops)
	}
}

func TestMutatePodsRejectsMalformedBody(t *testing.T) {
	rec, _ := mutate(t, []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
