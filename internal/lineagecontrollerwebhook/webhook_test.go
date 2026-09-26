package lineagecontrollerwebhook

import (
	"context"
	"sort"
	"testing"

	schedulerapi "github.com/cozystack/cozystack-scheduler/pkg/apis/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const podWithNumericFields = `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "harbor-demo-core-0",
    "namespace": "tenant-demo",
    "labels": {"helm.toolkit.fluxcd.io/name": "harbor-demo"},
    "annotations": {"existing": "kept"}
  },
  "spec": {
    "terminationGracePeriodSeconds": 30,
    "activeDeadlineSeconds": 9007199254740993,
    "containers": [{
      "name": "core",
      "image": "goharbor/harbor-core:v2.11.0",
      "ports": [{"containerPort": 8080, "protocol": "TCP"}],
      "resources": {
        "limits": {"cpu": "500m", "memory": "512Mi"},
        "requests": {"cpu": "0.1", "memory": "128Mi"}
      }
    }]
  }
}`

func newHandleTestWebhook(t *testing.T) *LineageControllerWebhook {
	t.Helper()
	scheme := newWebhookScheme(t)
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "tenant-demo",
		Labels: map[string]string{schedulerapi.SchedulingClassLabel: "gpu"},
	}}

	hrGVK := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	appGVK := schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Harbor"}
	scGVK := schema.GroupVersionKind{Group: schedulerapi.Group, Version: schedulerapi.Version, Kind: "SchedulingClass"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(hrGVK, meta.RESTScopeNamespace)
	mapper.Add(appGVK, meta.RESTScopeNamespace)

	obj := func(gvk schema.GroupVersionKind, namespace, name string, labels map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(namespace)
		u.SetName(name)
		u.SetLabels(labels)
		return u
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		obj(hrGVK, "tenant-demo", "harbor-demo", map[string]string{
			ManagerKindKey:  "Harbor",
			ManagerGroupKey: "apps.cozystack.io",
			ManagerNameKey:  "demo",
		}),
		obj(appGVK, "tenant-demo", "demo", nil),
		obj(scGVK, "", "gpu", nil),
	)

	w := &LineageControllerWebhook{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build(),
		Scheme:    scheme,
		dynClient: dyn,
		mapper:    mapper,
	}
	w.initConfig()
	return w
}

// TestHandle_PatchTouchesOnlyLineageAndScheduling pins that the JSON
// patch computed from the re-marshalled Pod contains the lineage labels
// and scheduler fields and nothing else: numbers the webhook never
// touched, including an int64 beyond float64 precision, must not show
// up as replace operations.
func TestHandle_PatchTouchesOnlyLineageAndScheduling(t *testing.T) {
	w := newHandleTestWebhook(t)

	resp := w.Handle(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Kind:      metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: "tenant-demo",
		Name:      "harbor-demo-core-0",
		Operation: admissionv1.Create,
		Object:    runtime.RawExtension{Raw: []byte(podWithNumericFields)},
	}})

	if !resp.Allowed {
		t.Fatalf("expected allowed response, got %+v", resp.Result)
	}

	want := map[string]any{
		"add /metadata/labels/internal.cozystack.io~1managed-by-cozystack":   "true",
		"add /metadata/labels/apps.cozystack.io~1application.group":          "apps.cozystack.io",
		"add /metadata/labels/apps.cozystack.io~1application.kind":           "Harbor",
		"add /metadata/labels/apps.cozystack.io~1application.name":           "demo",
		"add /metadata/labels/internal.cozystack.io~1tenantresource":         "false",
		"add /metadata/annotations/scheduler.cozystack.io~1scheduling-class": "gpu",
		"add /spec/schedulerName":                                            schedulerapi.SchedulerName,
	}

	got := map[string]any{}
	var keys []string
	for _, p := range resp.Patches {
		k := p.Operation + " " + p.Path
		got[k] = p.Value
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(resp.Patches) != len(want) {
		t.Errorf("expected %d patch operations, got %d: %v", len(want), len(resp.Patches), keys)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("patch %q = %#v, want %#v", k, got[k], v)
		}
	}
	for _, p := range resp.Patches {
		if _, ok := want[p.Operation+" "+p.Path]; !ok {
			t.Errorf("unexpected patch operation %s %s = %#v", p.Operation, p.Path, p.Value)
		}
	}
}
