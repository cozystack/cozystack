package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	av1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ca "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func req(op av1.Operation, pool, old map[string]any) ca.Request {
	raw, _ := json.Marshal(map[string]any{"apiVersion": "apps.cozystack.io/v1alpha1", "kind": "KubernetesNodes", "metadata": map[string]any{"name": "k-md0", "namespace": "tenant-test"}, "spec": pool})
	var oldRaw []byte
	if old != nil {
		oldRaw, _ = json.Marshal(map[string]any{"spec": old})
	}
	return ca.Request{AdmissionRequest: av1.AdmissionRequest{Operation: op, Resource: metav1.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "kubernetesnodeses"}, Kind: metav1.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "KubernetesNodes"}, Namespace: "tenant-test", Name: "k-md0", Object: runtime.RawExtension{Raw: raw}, OldObject: runtime.RawExtension{Raw: oldRaw}}}
}
func reader(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := workers.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}
func wc(name, bootstrap string) *workers.WorkerClass {
	return &workers.WorkerClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: workers.WorkerClassSpec{Bootstrap: workers.Bootstrap(bootstrap)}}
}

func TestDefaultMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		op        av1.Operation
		pool, old map[string]any
		want      string
		patch     bool
	}{
		{"create default", av1.Create, map[string]any{}, nil, "talos", true},
		{"create explicit", av1.Create, map[string]any{"workerClassName": "custom"}, nil, "custom", false},
		{"create empty", av1.Create, map[string]any{"workerClassName": ""}, nil, "talos", true},
		{"legacy omission", av1.Update, map[string]any{}, map[string]any{}, "", false},
		{"legacy empty", av1.Update, map[string]any{"workerClassName": ""}, map[string]any{}, "", false},
		{"classified omission", av1.Update, map[string]any{}, map[string]any{"workerClassName": "custom"}, "custom", true},
		{"classified empty", av1.Update, map[string]any{"workerClassName": ""}, map[string]any{"workerClassName": "custom"}, "custom", true},
		{"new selection", av1.Update, map[string]any{"workerClassName": "other"}, map[string]any{"workerClassName": "custom"}, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := (&Mutator{DefaultWorkerClass: "talos"}).Handle(context.Background(), req(tc.op, tc.pool, tc.old))
			if !resp.Allowed {
				t.Fatalf("mutation denied: %v", resp.Result)
			}
			if (len(resp.Patches) > 0) != tc.patch {
				t.Fatalf("patches=%v want patch=%v", resp.Patches, tc.patch)
			}
			if tc.patch {
				if len(resp.Patches) != 1 || resp.Patches[0].Path != "/spec/workerClassName" || fmt.Sprint(resp.Patches[0].Value) != tc.want {
					t.Fatalf("patch=%v want %s", resp.Patches, tc.want)
				}
			}
		})
	}
}

func TestPlatformDefaultOnlyAffectsCreates(t *testing.T) {
	m := &Mutator{DefaultWorkerClass: "kairos"}
	resp := m.Handle(context.Background(), req(av1.Create, map[string]any{}, nil))
	if !resp.Allowed || len(resp.Patches) != 1 || fmt.Sprint(resp.Patches[0].Value) != "kairos" {
		t.Fatalf("create=%v", resp)
	}
	resp = m.Handle(context.Background(), req(av1.Update, map[string]any{}, map[string]any{"workerClassName": "talos"}))
	if !resp.Allowed || fmt.Sprint(resp.Patches[0].Value) != "talos" {
		t.Fatalf("update=%v", resp)
	}
}

func TestValidation(t *testing.T) {
	deleting := wc("deleting", "talos")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"workerclasses.cozystack.io/in-use"}
	r := reader(t, wc("talos-custom", "talos"), wc("kairos-custom", "kairos"), deleting)
	v := &Validator{Reader: r}
	for _, tc := range []struct {
		name      string
		op        av1.Operation
		pool, old map[string]any
		allow     bool
		message   string
	}{
		{"create built-in", av1.Create, map[string]any{"workerClassName": "talos"}, nil, true, ""},
		{"create missing class field", av1.Create, map[string]any{}, nil, false, "workerClassName"},
		{"custom missing", av1.Create, map[string]any{"workerClassName": "missing"}, nil, false, "missing"},
		{"custom deleting create", av1.Create, map[string]any{"workerClassName": "deleting"}, nil, false, "deleting"},
		{"custom deleting target", av1.Update, map[string]any{"workerClassName": "deleting"}, map[string]any{"workerClassName": "talos"}, false, "deleting"},
		{"stay on deleting", av1.Update, map[string]any{"workerClassName": "deleting"}, map[string]any{"workerClassName": "deleting"}, true, ""},
		{"legacy update", av1.Update, map[string]any{}, map[string]any{}, true, ""},
		{"cannot unclassify", av1.Update, map[string]any{}, map[string]any{"workerClassName": "talos-custom"}, false, "workerClassName"},
		{"same family", av1.Update, map[string]any{"workerClassName": "talos-custom"}, map[string]any{"workerClassName": "talos"}, true, ""},
		{"different family", av1.Update, map[string]any{"workerClassName": "kairos-custom"}, map[string]any{"workerClassName": "talos-custom"}, false, "bootstrap"},
		{"legacy cannot become Kairos", av1.Update, map[string]any{"workerClassName": "kairos"}, map[string]any{}, false, "bootstrap"},
		{"Talos SSH rejected", av1.Create, map[string]any{"workerClassName": "talos", "sshKeys": []any{"key"}}, nil, false, "sshKeys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := v.Handle(context.Background(), req(tc.op, tc.pool, tc.old))
			if resp.Allowed != tc.allow {
				t.Fatalf("allowed=%v result=%v", resp.Allowed, resp.Result)
			}
			if tc.message != "" && (resp.Result == nil || !strings.Contains(resp.Result.Message, tc.message)) {
				t.Fatalf("message=%v want %q", resp.Result, tc.message)
			}
		})
	}
}

func TestLookupFailureIsClosedButLegacyNeedsNoLookup(t *testing.T) {
	r := interceptor.NewClient(reader(t), interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Group: "cozystack.io", Resource: "workerclasses"}, "custom", fmt.Errorf("read denied"))
	}})
	v := &Validator{Reader: r}
	resp := v.Handle(context.Background(), req(av1.Create, map[string]any{"workerClassName": "custom"}, nil))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failed open: %v", resp)
	}
	resp = v.Handle(context.Background(), req(av1.Update, map[string]any{}, map[string]any{}))
	if !resp.Allowed {
		t.Fatalf("legacy should not read class: %v", resp)
	}
}

func TestMalformedRequestsFailClosed(t *testing.T) {
	handlers := []ca.Handler{&Mutator{DefaultWorkerClass: "talos"}, &Validator{Reader: reader(t)}}
	for _, h := range handlers {
		for _, r := range []ca.Request{req(av1.Update, map[string]any{}, nil), req(av1.Create, map[string]any{"workerClassName": 123}, nil)} {
			if response := h.Handle(context.Background(), r); response.Allowed {
				t.Errorf("malformed request allowed by %T", h)
			}
		}
	}
}
